import { render } from "preact";
import { useState, useRef, useEffect } from "preact/hooks";
import { Publisher } from "../rtc/publisher.js";
import { ReconnectingSession, TERMINAL_REASONS } from "../rtc/session.js";
import { MeshManager, isMeshRole } from "../rtc/mesh.js";
import { DegradationController } from "../rtc/degradation.js";
import { FORCE_FRAME } from "./grid-tile.js";
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
  // Backstage thumbnails (D-10): every other backstage guest/co-host rendered over a P2P mesh, plus
  // this client's own rank (viewerRole) so a co-host's thumbnail tiles show the moderation controls
  // it may use within rank (a guest's are view-only). A co-host moderates from here because the
  // host-only /greenroom isn't reachable with a pass (AC-11 "a co-host, within rank").
  const [thumbnails, setThumbnails] = useState(/** @type {Array<{id:string, entry:any, stream:MediaStream|null}>} */ ([]));
  const [viewerRole, setViewerRole] = useState("guest");
  // selfDegraded is THIS guest's own degradation state (AD-21), read back from its self roster entry
  // (the round-trip: the local controller sheds + reports {t:stats}, the server folds it, and the
  // roster reflects it here) — a guest sees only its OWN degradation (AC-15).
  const [selfDegraded, setSelfDegraded] = useState(/** @type {{dir:string,reason:string}|null} */ (null));
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
  /** @type {{current: import("../rtc/mesh.js").MeshManager|null}} */
  const meshRef = useRef(null);
  /** @type {{current: import("../rtc/degradation.js").DegradationController|null}} */
  const degRef = useRef(null);
  // Ref mirrors of the roster + own id, so the once-registered signal handler routes by the CURRENT
  // roster (a guest/co-host peer → the mesh; the host or an OBS source → the Publisher).
  /** @type {{current: any[]}} */
  const peersRef = useRef([]);
  const selfIdRef = useRef("");

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

  // syncThumbnails rebuilds the backstage thumbnail list from the current roster (every other
  // guest/co-host) and the mesh's received streams, in a stable id order so tiles don't reshuffle.
  // Read from refs (not state) so it's correct whether called from a handler or the mesh callback.
  function syncThumbnails() {
    const streams = meshRef.current ? meshRef.current.streams() : new Map();
    const tiles = peersRef.current
      .filter((p) => isMeshRole(p.role) && p.id !== selfIdRef.current)
      .map((p) => ({ id: p.id, entry: p, stream: streams.get(p.id) || null }))
      .sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
    setThumbnails(tiles);
  }

  // degradationTargets lists this guest's live video senders for the degradation sampler, tagged
  // with the shed priority (LOWER sheds FIRST): other-guest thumbnails (1) before co-host
  // thumbnails (2) before the program/monitor publish (3). The Publisher serves the host monitor +
  // OBS program; the mesh serves the backstage thumbnails (D-33 — thumbnails are the amplifier).
  function degradationTargets() {
    const targets = [];
    const pub = pubRef.current;
    if (pub) {
      for (const id of Object.keys(pub.pcs)) {
        const sender = pub.pcs[id].getSenders().find((s) => s.track && s.track.kind === "video");
        // protected: the program/monitor publish path is never hard-disabled by cpu shedding (that
        // would kill the broadcast) — only the thumbnail mesh senders are (D-33/DESIGN ladder).
        if (sender) targets.push({ key: "pub:" + id, priority: 3, protected: true, sender });
      }
    }
    const mesh = meshRef.current;
    if (mesh) {
      for (const [id, mp] of mesh.peers) {
        const sender = mp.pc.getSenders().find((s) => s.track && s.track.kind === "video");
        if (!sender) continue;
        const peer = peersRef.current.find((p) => p.id === id);
        targets.push({ key: "mesh:" + id, priority: peer && peer.role === "cohost" ? 2 : 1, sender });
      }
    }
    return targets;
  }

  // startPublishing keeps the already-running preview stream and publishes it to the greenroom over
  // the guest's pass WS, so consumers (host monitor, OBS source) render the guest over P2P. The
  // server only relays the opaque SDP/ICE (D-23). It runs inside a ReconnectingSession (AC-13): a
  // dropped socket auto-retries (pubState → "reconnecting"), and a TERMINAL {t:terminate} routes to
  // the matching error screen. setup() re-wires a fresh Publisher + mesh + handlers on each (re)connect.
  function startPublishing() {
    sessionRef.current = new ReconnectingSession({
      query: `pass=${encodeURIComponent(passTokenFromPath())}`,
      setup: (room) => {
        const publisher = new Publisher(room, /** @type {MediaStream} */ (streamRef.current));
        pubRef.current = publisher;
        // The backstage mesh (D-10): one bidirectional P2P link to each other guest/co-host for the
        // thumbnails. The Publisher serves the one-way consumers (host monitor, OBS sources).
        const mesh = new MeshManager(room, () => streamRef.current, syncThumbnails);
        meshRef.current = mesh;
        // Route each relayed signal by the sender's roster role: a guest/co-host (not us) is a mesh
        // peer; the host or an OBS source consumes us over the Publisher. Deterministic-offerer mesh
        // (lower id offers) means we only ever receive a mesh ANSWER/ICE from a higher-id peer and a
        // mesh OFFER from a lower-id peer, so a single connection per pair — no ambiguity (D-23).
        room.on("signal", (f) => {
          const peer = peersRef.current.find((p) => p.id === f.from);
          if (peer && isMeshRole(peer.role) && f.from !== selfIdRef.current) mesh.handleSignal(f);
          else publisher.onSignal(f);
        });
        // On-air self pill + global "we're live" reflection (D-24): the per-guest on-air is folded
        // into the roster (PR-1 retired the interim {t:onair} frame) — read it from this client's
        // OWN entry, located via the roster's `self` marker. The broadcast-level streaming state
        // stays a room-level {t:streaming} broadcast (it's room-wide, not per-guest).
        room.on("roster", (f) => {
          const ps = f.peers || [];
          peersRef.current = ps;
          setPeers(ps); // drives chat sender names + the thumbnail roster
          if (f.self) {
            selfIdRef.current = f.self;
            setSelfId(f.self);
          }
          const me = ps.find((p) => p.self || p.id === f.self);
          if (me) {
            setOnAir(me.onAir || "status-unavailable");
            setHandRaised(!!me.handRaised); // server-authoritative raise-hand (incl. host dismiss)
            setViewerRole(me.role); // our own rank → which thumbnail controls we may use (within rank)
            // RF-8: stop a force-suppressed modality's outbound track AT SOURCE (and re-enable a
            // released one). The server also rejects any self-state that re-enables a locked
            // modality, so this is cooperative source-side enforcement, not the authority (EN-7).
            const locked = (me.locks || []).map((l) => l.kind);
            for (const m of ["mic", "cam", "share"]) {
              publisher.setModalityEnabled(m, !locked.includes(m));
            }
            setLockedMods(locked);
            setSelfDegraded(me.degraded || null); // our own degradation, round-tripped (AD-21/AC-15)
          }
          mesh.sync(selfIdRef.current, ps); // open/drop mesh links for the current backstage set
          syncThumbnails();
        });
        // Backstage chat relay (EN-20): append each relayed message to the in-memory log. The
        // server broadcasts to every participant INCLUDING the sender, so the guest's own messages
        // arrive here too — the panel renders only what the server relays, never an optimistic echo,
        // and the chat is never persisted or logged (the purity is the server's tested invariant).
        room.on("chat", (f) => setMessages((prev) => [...prev, { from: f.from, text: f.text }]));
        // Keep the roster cache fresh between full broadcasts: a peer joining AFTER this guest is
        // announced as a {t:peer-joined} delta (existing peers don't get a fresh roster). This keeps
        // chat sender-name resolution AND the thumbnail/mesh set current. Mirrors the greenroom.
        room.on("peer-joined", (f) => {
          if (!f.peer) return;
          peersRef.current = [...peersRef.current.filter((p) => p.id !== f.peer.id), f.peer];
          setPeers(peersRef.current);
          mesh.sync(selfIdRef.current, peersRef.current);
          syncThumbnails();
        });
        room.on("peer-left", (f) => {
          peersRef.current = peersRef.current.filter((p) => p.id !== f.peerId);
          setPeers(peersRef.current);
          mesh.sync(selfIdRef.current, peersRef.current);
          syncThumbnails();
        });
        room.on("streaming", (f) => setStreaming(!!f.active));
        // Host "bump quality now" (AD-21/D-34): restore our shed senders immediately, overriding the
        // slow recover hysteresis. If the pressure persists, the next sample re-degrades.
        room.on("recover-quality", () => {
          if (degRef.current) degRef.current.recoverNow();
        });
        // Apply a refreshed ICE config (rotated TURN credential, EN-4) to every live connection.
        room.onIce((servers) => {
          publisher.applyIceServers(servers);
          mesh.applyIceServers(servers);
        });
        // Per-publisher-local degradation (AD-21): sample our OWN senders (Publisher = program/
        // monitor, highest priority; mesh = co-host/other-guest thumbnails, shed first), shed on
        // cpu/bandwidth pressure with hysteresis, and self-report {t:stats}. The server folds it and
        // the round-trip lights our own degradation. No peer controls another's encoders (D-23/EN-7).
        const deg = new DegradationController({
          getTargets: degradationTargets,
          report: (stats) => room.send({ t: "stats", signal: stats.signal, rttMs: stats.rttMs, degraded: stats.degraded }),
        });
        degRef.current = deg;
        deg.start();
      },
      teardown: () => {
        // The link dropped (or we're closing): stop the degradation sampler, stop publishing + tear
        // down the mesh (drop the dead peer connections), clear the thumbnails, and degrade the
        // reflected on-air + "we're live" + own-degradation state rather than assert stale values
        // (D-24). A reconnect re-arms all of it from the fresh roster + replay.
        if (degRef.current) degRef.current.stop();
        if (pubRef.current) pubRef.current.close();
        if (meshRef.current) meshRef.current.close();
        setThumbnails([]);
        setOnAir("status-unavailable");
        setStreaming(false);
        setSelfDegraded(null);
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

  // Backstage thumbnail moderation: a co-host (viewerRole "cohost") acts on a guest's tile within
  // rank (AC-11) — the reducer enforces authority (EN-7); these only fire when the socket is live.
  // promote/demote + hand-dismiss are host-only in the tile controls, so they never originate here
  // (a host uses /greenroom), but the tile is shared so the callbacks are wired for completeness.
  function thumbForce(id, m) {
    if (sessionRef.current && pubState === "live") sessionRef.current.send({ t: FORCE_FRAME[m], peerId: id });
  }
  function thumbRelease(id, m) {
    if (sessionRef.current && pubState === "live") sessionRef.current.send({ t: "release", peerId: id, kind: m });
  }
  function thumbRole(id, role) {
    if (sessionRef.current && pubState === "live") sessionRef.current.send({ t: "role", peerId: id, role });
  }
  function thumbDismissHand(id) {
    if (sessionRef.current && pubState === "live") sessionRef.current.send({ t: "hand", peerId: id, raised: false });
  }
  function thumbReconnect(id) {
    if (meshRef.current) meshRef.current.reconnect(id);
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
        selfDegraded={selfDegraded}
        thumbnails={thumbnails}
        viewerRole={viewerRole}
        onThumbForce={thumbForce}
        onThumbRelease={thumbRelease}
        onThumbRole={thumbRole}
        onThumbDismissHand={thumbDismissHand}
        onThumbReconnect={thumbReconnect}
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
