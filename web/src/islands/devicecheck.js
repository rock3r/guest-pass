import { render } from "preact";
import { useState, useRef, useEffect } from "preact/hooks";
import { Room } from "../rtc/room.js";
import { Publisher } from "../rtc/publisher.js";

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
  // disconnected — so the guest is never told they're live before the signaling WS is up.
  const [pubState, setPubState] = useState("connecting");
  // onAir is the three-state on-air SELF pill (D-24), reflected from OBS via the server:
  // status-unavailable (no OBS signal — the default) | on-air | not-on-air. streaming is the
  // global "we're live" broadcast reflection. Both are read-only reflections, never asserted.
  const [onAir, setOnAir] = useState("status-unavailable");
  const [streaming, setStreaming] = useState(false);
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
  /** @type {{current: import("../rtc/room.js").Room|null}} */
  const roomRef = useRef(null);
  /** @type {{current: import("../rtc/publisher.js").Publisher|null}} */
  const pubRef = useRef(null);

  // stopStream releases the camera/mic so the device light goes off. Called before a retry
  // (so we never leak a prior stream), after a successful entry (the greenroom re-acquires
  // its own media in PR-7), and on unmount.
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

  // Tear down on unmount: stop publishing, close the signaling WS, release the camera, and
  // mark cancelled so a still-pending getUserMedia releases its stream when it resolves.
  useEffect(
    () => () => {
      cancelledRef.current = true;
      if (pubRef.current) pubRef.current.close();
      if (roomRef.current) roomRef.current.close();
      stopStream();
    },
    [],
  );

  // startPublishing keeps the already-running preview stream and publishes it to the
  // greenroom over the guest's pass WS, so consumers (host monitor, OBS source) can render
  // the guest over P2P. The server only relays the opaque SDP/ICE (D-23).
  function startPublishing() {
    const room = new Room(`pass=${encodeURIComponent(passTokenFromPath())}`);
    roomRef.current = room;
    const publisher = new Publisher(room, /** @type {MediaStream} */ (streamRef.current));
    pubRef.current = publisher;
    room.on("signal", (f) => publisher.onSignal(f));
    // On-air self pill + global "we're live" reflection (D-24): the server forwards the OBS
    // source's reflection for this guest's slot, and the broadcast-level streaming state.
    room.on("onair", (f) => setOnAir(f.onAir || "status-unavailable"));
    room.on("streaming", (f) => setStreaming(!!f.active));
    // Apply a refreshed ICE config (rotated TURN credential, EN-4) to live consumers.
    room.onIce((servers) => publisher.applyIceServers(servers));
    // Only claim "live" once the signaling WS is actually up; on any disconnect — an abrupt
    // socket close or a server {t:terminate} that closes it — stop publishing (drop the dead
    // peer connections) and surface a reconnect state.
    room.ready.then(() => setPubState("live")).catch(() => setPubState("disconnected"));
    room.onClose(() => {
      publisher.close();
      setPubState("disconnected");
      // The signaling link is gone, so this client no longer has a live OBS reflection: degrade
      // the on-air pill and the global indicator rather than keep asserting their last values (D-24).
      setOnAir("status-unavailable");
      setStreaming(false);
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

  if (phase === "entered") {
    // Three-state on-air pill (D-24) — reflected from OBS, never asserted. "status-unavailable"
    // means no OBS signal at all (e.g. the host isn't running an OBS source for you yet).
    const onAirLabel =
      onAir === "on-air"
        ? "On air"
        : onAir === "not-on-air"
          ? "Not on air"
          : "On-air status unavailable";
    return (
      <div class="dc-entered" data-entered="1" data-pub={pubState}>
        {pubState === "live" ? (
          <p>You're in — your camera is live in the greenroom.</p>
        ) : pubState === "disconnected" ? (
          <p>You're in, but the greenroom connection dropped. Refresh the page to rejoin.</p>
        ) : (
          <p>You're in — connecting your camera to the greenroom…</p>
        )}
        <p class="dc-onair" data-onair={onAir}>
          {onAirLabel}
          {onAir === "status-unavailable" ? (
            <span class="dc-onair-hint"> — check the actual stream to confirm.</span>
          ) : null}
        </p>
        {streaming ? (
          <p class="dc-live" data-live="1">
            The broadcast is live.
          </p>
        ) : null}
      </div>
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
