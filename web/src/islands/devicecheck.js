import { render } from "preact";
import { useState, useRef, useEffect } from "preact/hooks";
import { Publisher } from "../rtc/publisher.js";
import { ReconnectingSession, TERMINAL_REASONS } from "../rtc/session.js";
import { GuestSession } from "./guest-session.js";

/**
 * passTokenFromPath extracts the magic-link token from the current /p/{token} URL. The
 * token is already in the guest's own address bar (it IS the magic link); the island reads
 * it only to call the explicit entry action — it is never logged or sent anywhere else.
 *
 * @returns {string}
 */
function passTokenFromPath() {
  const m = location.pathname.match(/^\/p\/([^/]+)/);
  return m ? m[1] : "";
}

/**
 * DeviceCheck is the guest's journey island (AC-5/AC-6): it requests a live camera + mic via
 * getUserMedia, shows a local preview, and — only on the explicit "enter" action — marks the
 * pass opened (EN-10) via a pass-authenticated POST. On a successful entry it keeps that same
 * camera and publishes it to the greenroom over the guest's pass WS (PR-7), so the host
 * monitor and OBS sources can render the guest over P2P. The server never sees the media (D-23).
 *
 * @returns {import("preact").VNode}
 */
function DeviceCheck() {
  /** @type {["idle"|"requesting"|"preview"|"entering"|"entered"|"error", Function]} */
  const [phase, setPhase] = useState("idle");
  // pubState reflects the greenroom publishing connection once entered: connecting | live |
  // reconnecting — so the guest is never told they're live before the signaling WS is up, and a
  // dropped socket surfaces the reconnecting overlay while the session auto-retries (AC-13).
  const [pubState, setPubState] = useState("connecting");
  // terminated holds a TERMINAL {t:terminate} reason (kicked/expired/revoked/…) once the server
  // ends this session for good (EN-9); it routes to the matching error screen and stops retrying.
  const [terminated, setTerminated] = useState("");
  // onAir is the three-state on-air SELF pill (D-24), reflected from OBS via the server:
  // status-unavailable (no OBS signal — the default) | on-air | not-on-air. streaming is the
  // global "we're live" broadcast reflection. Both are read-only reflections, never asserted.
  const [onAir, setOnAir] = useState("status-unavailable");
  const [streaming, setStreaming] = useState(false);
  // lockedMods are this guest's currently force-suppressed modalities (mic|cam|share), read from
  // its own roster entry's locks. On a lock the matching outbound track is stopped AT SOURCE
  // (RF-8); the guest-session renders the visible "muted/hidden by host" notice from these.
  const [lockedMods, setLockedMods] = useState(/** @type {string[]} */ ([]));
  // Backstage chat + roster state for the in-session guest-session view (AC-12). messages are
  // rendered ONLY from relayed {t:chat} frames and held in memory — never persisted or echoed
  // optimistically (EN-20), so a message appearing proves it round-tripped the server relay.
  // peers/selfId resolve chat sender names; handRaised is the server's roster value (raise-hand is
  // server-authoritative, not optimistic — a host dismiss lowers it the same way, PR-7/D-15).
  const [messages, setMessages] = useState(/** @type {Array<{from:string,text:string}>} */ ([]));
  const [peers, setPeers] = useState(/** @type {any[]} */ ([]));
  const [selfId, setSelfId] = useState("");
  const [handRaised, setHandRaised] = useState(false);
  const [error, setError] = useState("");
  /** @type {{current: HTMLVideoElement|null}} */
  const videoRef = useRef(null);
  /** @type {{current: MediaStream|null}} */
  const streamRef = useRef(null);
  // cancelled flips true on unmount so a getUserMedia promise that resolves AFTER the island
  // is gone releases its stream instead of leaking the camera.
  const cancelledRef = useRef(false);
  // requesting guards against re-entrant startCheck calls (e.g. a rapid double-click) so at
  // most one getUserMedia is ever in flight — two concurrent ones would leak a stream.
  const requestingRef = useRef(false);
  /** @type {{current: import("../rtc/session.js").ReconnectingSession|null}} */
  const sessionRef = useRef(null);
  /** @type {{current: import("../rtc/publisher.js").Publisher|null}} */
  const pubRef = useRef(null);

  // stopStream releases the camera/mic so the device light goes off. Called before a retry (so we
  // never leak a prior stream), on a FAILED entry, and on unmount. After a SUCCESSFUL entry the
  // stream is KEPT — it is published to the greenroom AND shown as the guest's self-view (AC-12).
  function stopStream() {
    if (streamRef.current) {
      streamRef.current.getTracks().forEach((t) => t.stop());
      streamRef.current = null;
    }
  }

  async function startCheck() {
    if (requestingRef.current) return; // a getUserMedia is already in flight
    requestingRef.current = true;
    stopStream(); // release any previous stream (e.g. on retry) before requesting a new one
    setPhase("requesting");
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
      if (cancelledRef.current) {
        // The island unmounted while the prompt was pending — don't keep the camera on.
        stream.getTracks().forEach((t) => t.stop());
        return;
      }
      streamRef.current = stream;
      setPhase("preview");
    } catch (e) {
      setError((e && /** @type {Error} */ (e).name) || String(e));
      setPhase("error");
    } finally {
      requestingRef.current = false;
    }
  }

  // Attach the captured stream once the <video> element is in the DOM (preview phase).
  useEffect(() => {
    if (phase === "preview" && videoRef.current && streamRef.current) {
      videoRef.current.srcObject = streamRef.current;
    }
  }, [phase]);

  // Tear down on unmount: stop the reconnecting session (which closes the publisher + WS and
  // halts retries), release the camera, and mark cancelled so a still-pending getUserMedia
  // releases its stream when it resolves.
  useEffect(
    () => () => {
      cancelledRef.current = true;
      if (sessionRef.current) sessionRef.current.close();
      stopStream();
    },
    [],
  );

  // startPublishing keeps the already-running preview stream and publishes it to the greenroom over
  // the guest's pass WS, so consumers (host monitor, OBS source) render the guest over P2P. The
  // server only relays the opaque SDP/ICE (D-23). It runs inside a ReconnectingSession (AC-13): a
  // dropped socket auto-retries (pubState → "reconnecting"), and a TERMINAL {t:terminate} routes to
  // the matching error screen. setup() re-wires a fresh Publisher + handlers on each (re)connection.
  function startPublishing() {
    sessionRef.current = new ReconnectingSession({
      query: `pass=${encodeURIComponent(passTokenFromPath())}`,
      setup: (room) => {
        const publisher = new Publisher(room, /** @type {MediaStream} */ (streamRef.current));
        pubRef.current = publisher;
        room.on("signal", (f) => publisher.onSignal(f));
        // On-air self pill + global "we're live" reflection (D-24): the per-guest on-air is folded
        // into the roster (PR-1 retired the interim {t:onair} frame) — read it from this client's
        // OWN entry, located via the roster's `self` marker. The broadcast-level streaming state
        // stays a room-level {t:streaming} broadcast (it's room-wide, not per-guest).
        room.on("roster", (f) => {
          setPeers(f.peers || []); // drives chat sender names + (later) backstage thumbnails
          if (f.self) setSelfId(f.self);
          const me = (f.peers || []).find((p) => p.self || p.id === f.self);
          if (!me) return;
          setOnAir(me.onAir || "status-unavailable");
          setHandRaised(!!me.handRaised); // server-authoritative raise-hand state (incl. host dismiss)
          // RF-8: stop a force-suppressed modality's outbound track AT SOURCE (and re-enable a
          // released one). The server also rejects any self-state that re-enables a locked modality,
          // so this is cooperative source-side enforcement, not the authority (EN-7).
          const locked = (me.locks || []).map((l) => l.kind);
          for (const m of ["mic", "cam", "share"]) {
            publisher.setModalityEnabled(m, !locked.includes(m));
          }
          setLockedMods(locked);
        });
        // Backstage chat relay (EN-20): append each relayed message to the in-memory log. The
        // server broadcasts to every participant INCLUDING the sender, so the guest's own messages
        // arrive here too — the panel renders only what the server relays, never an optimistic echo,
        // and the chat is never persisted or logged (the purity is the server's tested invariant).
        room.on("chat", (f) => setMessages((prev) => [...prev, { from: f.from, text: f.text }]));
        // Keep the peer-name cache fresh between full roster broadcasts: a peer joining AFTER this
        // guest arrives is announced as a {t:peer-joined} delta (existing peers don't get a fresh
        // roster), so without this a later-joiner's chat would render as a raw peer id until some
        // unrelated roster rebroadcast. Mirrors the greenroom's peer-joined/peer-left handling.
        room.on("peer-joined", (f) => {
          if (f.peer) setPeers((prev) => [...prev.filter((p) => p.id !== f.peer.id), f.peer]);
        });
        room.on("peer-left", (f) => setPeers((prev) => prev.filter((p) => p.id !== f.peerId)));
        room.on("streaming", (f) => setStreaming(!!f.active));
        // Apply a refreshed ICE config (rotated TURN credential, EN-4) to live consumers.
        room.onIce((servers) => publisher.applyIceServers(servers));
      },
      teardown: () => {
        // The link dropped (or we're closing): stop publishing (drop the dead peer connections)
        // and degrade the reflected on-air + "we're live" state rather than keep asserting their
        // last values (D-24). A successful reconnect re-arms them from the fresh roster + replay.
        if (pubRef.current) pubRef.current.close();
        setOnAir("status-unavailable");
        setStreaming(false);
      },
      onState: (st) => setPubState(st), // "live" once up, "reconnecting" while a drop retries
      onTerminal: (reason) => {
        // The session is over for good — release the camera/mic so the device light goes off behind
        // the error screen (the session won't reconnect, so nothing re-publishes this stream).
        stopStream();
        setTerminated(reason); // kicked/expired/revoked/session-ended/token-rotated/unreachable
      },
    });
  }

  async function enter() {
    setPhase("entering");
    try {
      const res = await fetch(`/p/${encodeURIComponent(passTokenFromPath())}/enter`, {
        method: "POST",
      });
      if (!res.ok) throw new Error(`entry failed (${res.status})`);
      if (cancelledRef.current) {
        // The island unmounted while the entry POST was in flight — don't start a new
        // publishing connection that nothing would ever tear down.
        stopStream();
        return;
      }
      startPublishing(); // keep the camera and publish it to the greenroom (PR-7)
      setPhase("entered");
    } catch (e) {
      stopStream(); // entry failed — don't leave the camera running behind the error UI
      setError(String((e && /** @type {Error} */ (e).message) || e));
      setPhase("error");
    }
  }

  // sendChat relays a backstage message over the live signaling room; it is rendered only when
  // the server relays it back (EN-20 — never an optimistic local echo). toggleHand flips the
  // server-authoritative raise-hand state via {t:hand} (the roster reflection drives the button).
  // Both guard on pubState === "live": Room.send calls WebSocket.send, which THROWS while the
  // socket is still CONNECTING, so a click before room.ready resolves must be a no-op (the
  // GuestSession also disables the controls until live; this is the defense-in-depth backstop).
  function sendChat(text) {
    if (sessionRef.current && pubState === "live") sessionRef.current.send({ t: "chat", text });
  }
  function toggleHand() {
    if (sessionRef.current && pubState === "live") sessionRef.current.send({ t: "hand", raised: !handRaised });
  }

  // A TERMINAL terminate (EN-9) ends the session for good — route to the matching error screen and
  // never reconnect. Checked before the phase screens so it wins over the in-session view.
  if (terminated) {
    const copy = TERMINAL_REASONS[terminated] || {
      title: "Your session ended",
      body: "This session is no longer active. Ask the host for a new link.",
    };
    return (
      <div class="gs-terminal" data-terminal={terminated}>
        <h2 class="gs-terminal-title">{copy.title}</h2>
        <p class="gs-terminal-body">{copy.body}</p>
      </div>
    );
  }

  if (phase === "entered") {
    // The in-session guest-session surface (AC-12), sharing this island's single pass-token Room.
    return (
      <GuestSession
        pubState={pubState}
        onAir={onAir}
        streaming={streaming}
        lockedMods={lockedMods}
        selfStream={streamRef.current}
        peers={peers}
        selfId={selfId}
        messages={messages}
        handRaised={handRaised}
        onSendChat={sendChat}
        onToggleHand={toggleHand}
      />
    );
  }
  if (phase === "error") {
    return (
      <div class="dc-error">
        <p>Couldn't continue: {error}. Check that your browser can access the camera and mic.</p>
        <button type="button" class="dc-retry" onClick={startCheck}>
          Try again
        </button>
      </div>
    );
  }
  if (phase === "preview" || phase === "entering") {
    return (
      <div class="dc-preview">
        <video ref={videoRef} class="dc-video" autoplay playsinline muted />
        <p>This is your camera preview. Only you can see it until you enter.</p>
        <button type="button" class="dc-enter" disabled={phase === "entering"} onClick={enter}>
          {phase === "entering" ? "Entering…" : "Enter the greenroom"}
        </button>
      </div>
    );
  }
  return (
    <button
      type="button"
      class="dc-start"
      disabled={phase === "requesting"}
      onClick={startCheck}
    >
      {phase === "requesting" ? "Requesting camera…" : "Continue to camera check"}
    </button>
  );
}

/**
 * mountDeviceCheck renders the device-check island into root.
 * @param {HTMLElement} root
 */
export function mountDeviceCheck(root) {
  render(<DeviceCheck />, root);
}
