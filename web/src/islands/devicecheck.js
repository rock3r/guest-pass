import { render } from "preact";
import { useState, useRef, useEffect } from "preact/hooks";

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
 * DeviceCheck is the guest device-check island (AC-5): it requests a live camera + mic via
 * getUserMedia, shows a local preview, and — only on the explicit "enter" action — marks
 * the pass opened (EN-10) via a pass-authenticated POST. No media leaves the browser here;
 * the greenroom WebRTC connection is established after entry (PR-7).
 *
 * @returns {import("preact").VNode}
 */
function DeviceCheck() {
  /** @type {["idle"|"requesting"|"preview"|"entering"|"entered"|"error", Function]} */
  const [phase, setPhase] = useState("idle");
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

  // Release the camera/mic when the island unmounts so the device light goes off, and mark
  // cancelled so a still-pending getUserMedia releases its stream when it resolves.
  useEffect(
    () => () => {
      cancelledRef.current = true;
      stopStream();
    },
    [],
  );

  async function enter() {
    setPhase("entering");
    try {
      const res = await fetch(`/p/${encodeURIComponent(passTokenFromPath())}/enter`, {
        method: "POST",
      });
      if (!res.ok) throw new Error(`entry failed (${res.status})`);
      stopStream(); // the device-check preview is done; the greenroom re-acquires media (PR-7)
      setPhase("entered");
    } catch (e) {
      stopStream(); // entry failed — don't leave the camera running behind the error UI
      setError(String((e && /** @type {Error} */ (e).message) || e));
      setPhase("error");
    }
  }

  if (phase === "entered") {
    return (
      <p class="dc-entered" data-entered="1">
        You're in. Waiting for the host to start the greenroom…
      </p>
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
