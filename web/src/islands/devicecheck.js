import "./devicecheck.css";
import { render } from "preact";
import { useState, useRef, useEffect } from "preact/hooks";
import { Publisher } from "../rtc/publisher.js";
import { PeerLink } from "../rtc/peerlink.js";
import { ReconnectingSession, TERMINAL_REASONS } from "../rtc/session.js";
import { MeshManager, isMeshRole } from "../rtc/mesh.js";
import { DegradationController } from "../rtc/degradation.js";
import { ConnectivityWatch } from "../rtc/connectivity.js";
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

// sessionStorage keys for the guest's chosen camera/mic (M5.5 device picker). The pick sticks
// across a retry or a reload within the same tab session — deviceIds are origin-stable once
// permission is granted, so re-using them is safe and saves the guest re-choosing every visit.
const CAM_KEY = "gp.device.cam";
const MIC_KEY = "gp.device.mic";

/**
 * loadDevice / storeDevice are best-effort: a private-mode browser throws on sessionStorage access,
 * in which case the selection is simply held in component state for the life of the island and the
 * browser default is used on the next load — never a thrown error.
 * @param {string} key
 * @returns {string}
 */
function loadDevice(key) {
  try {
    return sessionStorage.getItem(key) || "";
  } catch {
    return "";
  }
}

/**
 * storeDevice persists a chosen deviceId; a no-op (swallowed) when storage is unavailable.
 * @param {string} key
 * @param {string} id
 */
function storeDevice(key, id) {
  try {
    if (id) sessionStorage.setItem(key, id);
  } catch {
    /* storage blocked (private mode) — the pick stays session-local, which is fine */
  }
}

// Grace before a screen capture that the server says is no longer pooled (screenShare:"") with no
// share lock is released (D-21/AC-13). A transient reconnect briefly projects screenShare:"" +
// canScreen:false on the JOIN roster before the eligibility re-seed roster arrives in the same
// handshake; the genuine "revoked while disconnected" case never re-seeds. A follow-up roster within
// this window supersedes (recover into the pool); if none arrives we're genuinely stranded → release.
const SHARE_RECONCILE_MS = 2000;

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
  // netBlocked is the D-38 "your network blocks peer-to-peer" state: set when the connectivity
  // watchdog sees the guest is publishing with a consumer/peer trying, yet NO P2P connection ever
  // establishes (symmetric NAT / UDP-blocking firewall on a STUN-only v1). It takes render
  // precedence over the in-session view so the guest gets a clear screen instead of a false
  // "you're live" silent hang. Cleared on recovery (a pc connects after) or the Retry action.
  const [netBlocked, setNetBlocked] = useState(false);
  // The guest LEFT voluntarily (M5.5/AC-2, DESIGN §6 guest-left). Distinct from a terminal terminate
  // (the host/system ended it) — the guest chose to go, and can rejoin while the stream is still live
  // (D-40). Wins over the in-session view; the rejoin path returns to the device-check preview.
  const [left, setLeft] = useState(false);
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
  // This guest's OWN screenshare self-state (AC-13), read back from its self roster entry's
  // screenShare pointer: "" not sharing, "backstage" capturing-but-not-selected, "live" the host
  // promoted this sharer to the screen slot. The sharer never asserts "live" optimistically — it is
  // derived solely from the server-folded self pointer (screen-roster is host-only, EN-8).
  const [screenShare, setScreenShare] = useState(/** @type {string} */ (""));
  // The live screen share to render for everyone (AC-11): {id, name, stream} of the host-selected
  // live sharer (consumed over a screen-channel PeerLink), or null when no share is live. A guest
  // learns the live sharer from the roster's screenShare:"live" fold (the screen-roster is host-only).
  const [liveScreen, setLiveScreen] = useState(/** @type {{id:string,name:string,stream:MediaStream}|null} */ (null));
  const [error, setError] = useState("");
  // Which KIND of device-check failure to render (M5.5/AC-2, DESIGN §6): "blocked" (camera/mic
  // permission denied — cam-blocked), "no-devices" (none attached), "unsupported" (no WebRTC /
  // in-app webview), or "" (a generic, retryable failure). Drives distinct copy on the error screen;
  // "unsupported" is the only one with no "Try again" (a retry can't fix a missing API).
  const [errorKind, setErrorKind] = useState(/** @type {""|"blocked"|"no-devices"|"unsupported"} */ (""));
  // Pre-join device picker (M5.5/AC-5): the guest chooses a camera + microphone before going live,
  // and the choice persists into the published stream — they never re-pick once in session. The
  // chosen ids drive getUserMedia's deviceId constraint. Refs are the source of truth for the async
  // re-acquire (no stale render closure); the state mirrors drive the <select> values + option list.
  // An audio-OUTPUT picker is deliberately omitted: the guest surface has no audible sink (every
  // <video> is muted, there is no <audio> element), so setSinkId would have nothing to attach to.
  const [devices, setDevices] = useState(
    /** @type {{cams: Array<{id:string,label:string}>, mics: Array<{id:string,label:string}>}} */ ({ cams: [], mics: [] }),
  );
  const [camId, setCamId] = useState("");
  const [micId, setMicId] = useState("");
  // True while a device switch is re-acquiring: it holds Enter + the dropdowns disabled so the guest
  // can't go live mid-switch, which makes the device they just chose the one actually published
  // (and is the user-facing half of the switch-vs-enter guard; enteringRef is the code backstop).
  const [switching, setSwitching] = useState(false);
  // Set when a device switch fails (the chosen device was unplugged/busy) but a working preview is
  // still live: we keep that preview and show an inline notice rather than dropping to the error
  // screen with the old camera/mic running behind it. Cleared on the next switch attempt.
  const [switchError, setSwitchError] = useState(false);
  const camIdRef = useRef("");
  const micIdRef = useRef("");
  // Set the instant entry begins, so a device switch still acquiring when the guest hits Enter does
  // NOT swap (and stop the tracks of) the stream the publisher just took live (a dead/old stream on
  // air). Cleared whenever we return to a fresh, switchable preview (a retry or network-retry).
  const enteringRef = useRef(false);
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
  /** @type {{current: import("../rtc/connectivity.js").ConnectivityWatch|null}} */
  const watchRef = useRef(null);
  // The guest's video-only screen-capture stream while it is sharing (D-21/AC-13); null when not.
  // Held here so the sharer can stop it on screen-stop, on a host force-no-share/revoke pull (the
  // server clears its roster screenShare pointer → we stop capturing), and on teardown.
  /** @type {{current: MediaStream|null}} */
  const screenStreamRef = useRef(null);
  // In-flight guard for the screen-capture request, so a rapid double-click can't start two
  // concurrent getDisplayMedia calls (the second stream would leak — mirrors requestingRef on cam).
  const screenRequestingRef = useRef(false);
  // Our SECOND publisher (D-21): publishes screenStreamRef on the "screen" channel so the host rail
  // (and the live render + /s/screen) can consume our screen track distinct from the camera. Created
  // on screen-start, closed on every stop/pull/teardown — its lifecycle tracks the capture.
  /** @type {{current: import("../rtc/publisher.js").Publisher|null}} */
  const screenPubRef = useRef(null);
  // The CONSUMER side of the screenshare for THIS client: a screen-channel PeerLink to whoever the
  // roster marks as the live sharer (screenShare:"live"), so the live share renders for everyone
  // (AC-11) — not just the host. Keyed by the sharer's peer id; at most one (the single live slot).
  /** @type {{current: Map<string, import("../rtc/peerlink.js").PeerLink>}} */
  const screenConsumersRef = useRef(new Map());
  // The sharer id currently rendered in liveScreen, so a re-select to a DIFFERENT sharer clears the
  // stale render immediately (rather than showing the previous, now-frozen, screen until the new
  // track arrives). "" when nothing is rendered.
  const liveScreenIdRef = useRef("");
  // The current signaling room (set by setup() on each (re)connect), so the component-level screen
  // publish/consume helpers can build links/publishers on the LIVE room without re-running setup.
  /** @type {{current: import("../rtc/room.js").Room|null}} */
  const roomRef = useRef(null);
  // Pending timer id for the deferred "release a stranded capture" reconcile (see SHARE_RECONCILE_MS).
  /** @type {{current: ReturnType<typeof setTimeout>|null}} */
  const pendingShareReconcileRef = useRef(null);
  // Live mirror of pubState, so async/once-registered closures (the post-picker send + the screen
  // track's `onended`, both captured at share-start) consult the CURRENT connection state instead of
  // a stale "live" — a send on a dropped/reconnecting socket would throw.
  const pubStateRef = useRef("connecting");
  // Set once the session ends for good (onTerminal). The screen picker can resolve AFTER a terminal,
  // and pubStateRef may still read "live" (no non-live onState precedes every terminal), so the
  // post-picker re-validation consults this to avoid capturing behind the terminal screen.
  const terminatedRef = useRef(false);
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

  // mediaConstraints builds the getUserMedia constraints from the current selection: an explicit
  // deviceId when the guest has chosen one, else the browser default (`true`). Read from the refs so
  // an async re-acquire always uses the latest pick, not a stale render closure.
  function mediaConstraints() {
    return {
      video: camIdRef.current ? { deviceId: { exact: camIdRef.current } } : true,
      audio: micIdRef.current ? { deviceId: { exact: micIdRef.current } } : true,
    };
  }

  // getStream captures the current camera+mic selection, retrying once on the browser default if a
  // stored/selected device is gone (OverconstrainedError) so a stale saved id never hard-fails the
  // check — syncSelectedFromTracks then re-points the dropdowns at whatever was actually captured.
  async function getStream() {
    try {
      return await navigator.mediaDevices.getUserMedia(mediaConstraints());
    } catch (e) {
      if (e && /** @type {Error} */ (e).name === "OverconstrainedError") {
        camIdRef.current = "";
        micIdRef.current = "";
        return await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
      }
      throw e;
    }
  }

  // installStream swaps a freshly captured stream into the preview, stopping the PRIOR stream only
  // after the new one is in place so a device switch never flashes black (and the old device's
  // capture light goes off). Points the live <video> at it when the preview is already mounted.
  function installStream(stream) {
    const prev = streamRef.current;
    streamRef.current = stream;
    if (prev && prev !== stream) prev.getTracks().forEach((t) => t.stop());
    if (videoRef.current) videoRef.current.srcObject = stream;
  }

  // syncSelectedFromTracks points the dropdowns at the devices the captured stream is ACTUALLY using
  // (getUserMedia honours a chosen id, or falls back to the default when it's gone) and persists
  // those ids, so the selects never show a device that isn't live and the pick sticks for the session.
  function syncSelectedFromTracks(stream) {
    const vt = stream.getVideoTracks()[0];
    const at = stream.getAudioTracks()[0];
    const vid = vt && vt.getSettings ? vt.getSettings().deviceId || "" : "";
    const aid = at && at.getSettings ? at.getSettings().deviceId || "" : "";
    if (vid) {
      camIdRef.current = vid;
      setCamId(vid);
      storeDevice(CAM_KEY, vid);
    }
    if (aid) {
      micIdRef.current = aid;
      setMicId(aid);
      storeDevice(MIC_KEY, aid);
    }
  }

  // refreshDevices lists the available cameras + microphones for the dropdowns (AC-5). Device LABELS
  // are only populated AFTER getUserMedia grants permission, so this runs post-capture; entries with
  // no deviceId (no permission / phantom) are dropped so the selects only ever offer real devices.
  async function refreshDevices() {
    if (!navigator.mediaDevices || !navigator.mediaDevices.enumerateDevices) return;
    let list;
    try {
      list = await navigator.mediaDevices.enumerateDevices();
    } catch {
      return; // enumeration unsupported/blocked — the preview still works on the default device
    }
    if (cancelledRef.current) return;
    const cams = [];
    const mics = [];
    for (const d of list) {
      if (!d.deviceId) continue;
      if (d.kind === "videoinput") cams.push({ id: d.deviceId, label: d.label || "Camera" });
      else if (d.kind === "audioinput") mics.push({ id: d.deviceId, label: d.label || "Microphone" });
    }
    setDevices({ cams, mics });
  }

  // mediaErrorKind maps a getUserMedia rejection to the device-check error variant (DESIGN §6). A
  // permission denial → cam-blocked; no attached devices → no-devices; everything else → generic.
  function mediaErrorKind(e) {
    const name = (e && /** @type {Error} */ (e).name) || "";
    if (name === "NotAllowedError" || name === "SecurityError") return "blocked";
    if (name === "NotFoundError" || name === "DevicesNotFoundError") return "no-devices";
    return "";
  }

  // webrtcSupported reports whether this browser can capture media AND open a peer connection. A
  // missing API (old browser, or an in-app webview that strips WebRTC) → the unsupported screen,
  // surfaced up front so the guest isn't sent into a permission prompt that can never succeed.
  function webrtcSupported() {
    return !!(
      navigator.mediaDevices &&
      navigator.mediaDevices.getUserMedia &&
      typeof window.RTCPeerConnection !== "undefined"
    );
  }

  async function startCheck() {
    if (requestingRef.current) return; // a getUserMedia is already in flight
    enteringRef.current = false; // a fresh check (incl. a retry) is back to a switchable preview
    if (!webrtcSupported()) {
      setErrorKind("unsupported");
      setPhase("error");
      return;
    }
    requestingRef.current = true;
    stopStream(); // release any previous stream (e.g. on retry) before requesting a new one
    setPhase("requesting");
    try {
      const stream = await getStream();
      if (cancelledRef.current) {
        // The island unmounted while the prompt was pending — don't keep the camera on.
        stream.getTracks().forEach((t) => t.stop());
        return;
      }
      installStream(stream);
      syncSelectedFromTracks(stream); // point the dropdowns at the devices actually captured
      await refreshDevices(); // labels resolve only post-permission — list the choices now
      setPhase("preview");
    } catch (e) {
      setErrorKind(mediaErrorKind(e));
      setError((e && /** @type {Error} */ (e).name) || String(e));
      setPhase("error");
    } finally {
      requestingRef.current = false;
    }
  }

  // switchDevice re-acquires the preview on a newly chosen camera or mic (AC-5). It runs only in the
  // preview phase (the picker isn't shown once entered), records the pick in ref + state + session
  // storage, then swaps the stream in place. A failed re-acquire surfaces the error screen, the same
  // as the initial check. Guarded against a concurrent acquire (a rapid change while one is in flight).
  async function switchDevice(kind, id) {
    if (requestingRef.current || enteringRef.current) return; // acquiring, or entry already locked the stream
    if (kind === "cam") {
      camIdRef.current = id;
      setCamId(id);
      storeDevice(CAM_KEY, id);
    } else {
      micIdRef.current = id;
      setMicId(id);
      storeDevice(MIC_KEY, id);
    }
    setSwitchError(false); // clear any prior failed-switch notice on a fresh attempt
    requestingRef.current = true;
    setSwitching(true); // hold Enter + the dropdowns until the new device is live (honour the pick)
    try {
      const stream = await getStream();
      // The guest hit Enter (or the island unmounted) while we were acquiring: the publisher has
      // already taken the prior stream live, so installing this one would stop the published tracks
      // (dead air). Release the just-captured stream and leave the live one untouched. We optimistically
      // moved the picker + storage to the new device at the top, so resync them back to the device the
      // KEPT stream actually uses — otherwise the persisted pick (and the dropdown after a network retry)
      // would name a device the live stream isn't using. Skip on unmount (no state to touch).
      if (cancelledRef.current || enteringRef.current) {
        stream.getTracks().forEach((t) => t.stop());
        if (!cancelledRef.current && streamRef.current) syncSelectedFromTracks(streamRef.current);
        return;
      }
      installStream(stream); // stops the prior capture once the new device is live (no black flash)
      syncSelectedFromTracks(stream);
      await refreshDevices();
    } catch (e) {
      // The chosen device couldn't be acquired (unplugged/busy). If a working preview is still live,
      // STAY on it: roll the picker + storage back to the device that stream uses and show an inline
      // notice — never drop to the error screen with the old camera/mic still running behind it. Only
      // fall back to the full error screen when there is no live stream to keep (defensive; switchDevice
      // runs from preview, so there normally is one).
      if (!cancelledRef.current && streamRef.current) {
        syncSelectedFromTracks(streamRef.current);
        setSwitchError(true);
      } else {
        setError((e && /** @type {Error} */ (e).name) || String(e));
        setPhase("error");
      }
    } finally {
      requestingRef.current = false;
      setSwitching(false);
    }
  }

  // Attach the captured stream once the <video> element is in the DOM (preview phase).
  useEffect(() => {
    if (phase === "preview" && videoRef.current && streamRef.current) {
      videoRef.current.srcObject = streamRef.current;
    }
  }, [phase]);

  // Tear down on unmount: stop the reconnecting session (which closes the publisher + WS and
  // halts retries), release the camera AND any screen capture (so getDisplayMedia tracks + the OS
  // screen-capture indicator don't outlive the island), and mark cancelled so a still-pending
  // getUserMedia/getDisplayMedia releases its stream when it resolves.
  useEffect(
    () => () => {
      cancelledRef.current = true;
      if (sessionRef.current) sessionRef.current.close();
      stopStream();
      stopScreenCapture();
      closeScreenConsumers();
    },
    [],
  );

  // Seed the picker from this tab's session storage so a reload / retry reuses the guest's earlier
  // pick before the first capture (mediaConstraints reads the refs). Best-effort (see loadDevice).
  useEffect(() => {
    const c = loadDevice(CAM_KEY);
    const m = loadDevice(MIC_KEY);
    if (c) {
      camIdRef.current = c;
      setCamId(c);
    }
    if (m) {
      micIdRef.current = m;
      setMicId(m);
    }
  }, []);

  // Keep the dropdowns fresh when the guest plugs in / unplugs a device while on the check screen.
  // Only the device LIST is refreshed (labels need the permission we already hold once previewing);
  // the active capture is untouched. Registered once and cleaned up on unmount.
  useEffect(() => {
    const md = navigator.mediaDevices;
    if (!md || !md.addEventListener) return undefined;
    const onChange = () => {
      refreshDevices();
    };
    md.addEventListener("devicechange", onChange);
    return () => md.removeEventListener("devicechange", onChange);
  }, []);

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
    // The screen-share senders join the ladder too, so a high-res share doesn't add uncapped encoders
    // that bypass the cpu/bandwidth budget (D-21/AD-21). The live share is on-air (D-34: screenshare >
    // guest cams), so they are PROTECTED like the program — never hard-disabled by cpu shedding — but
    // still bandwidth-stepped when constrained and counted in the stats sampler. Present only while sharing.
    const screenPub = screenPubRef.current;
    if (screenPub) {
      for (const id of Object.keys(screenPub.pcs)) {
        const sender = screenPub.pcs[id].getSenders().find((s) => s.track && s.track.kind === "video");
        if (sender) targets.push({ key: "scrn:" + id, priority: 3, protected: true, sender });
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
    // (Re)starting a publishing session means we are connecting until the new socket opens. Reset
    // pubState so a RE-entry (e.g. Retry from the network-blocked screen) doesn't inherit a stale
    // "live" from the prior attempt — ReconnectingSession.close() runs teardown but never fires
    // onState, so without this the live-gated send helpers + GuestSession could act on a socket that
    // is still CONNECTING and throw on WebSocket.send (the "never live before the WS is up" invariant).
    setPubState("connecting");
    pubStateRef.current = "connecting"; // keep the ref mirror in lockstep (close() never fires onState)
    sessionRef.current = new ReconnectingSession({
      query: `pass=${encodeURIComponent(passTokenFromPath())}`,
      setup: (room) => {
        roomRef.current = room; // the live room for the component-level screen publish/consume helpers
        // D-38 network-blocked watchdog: watch every consumer/peer pc this guest creates. On a
        // STUN-only path behind symmetric NAT / a UDP-blocking firewall NONE ever connects, so the
        // guest would otherwise sit on a false "you're live" — surface the network-blocked screen
        // instead. onRecovered clears it if a connection eventually comes through (a slow network).
        const watch = new ConnectivityWatch({
          onBlocked: () => {
            // The network-blocked overlay (D-38) takes render precedence over the in-session view,
            // hiding the .gs-screen control — and a STUN-only blocked path can't carry the share
            // anyway. D-38 is a MEDIA (P2P) failure: the SIGNALING socket is typically still live, so
            // the server still has us in the preview pool — use stopScreenShare (best-effort
            // {t:screen-stop} + local release), not a bare local stop, so the pool drops us too.
            stopScreenShare();
            setNetBlocked(true);
          },
          onRecovered: () => setNetBlocked(false),
        });
        watchRef.current = watch;
        const publisher = new Publisher(
          room,
          /** @type {MediaStream} */ (streamRef.current),
          (pc, id) => watch.track(pc, "pub:" + id),
          (id) => watch.untrack("pub:" + id),
        );
        pubRef.current = publisher;
        // The backstage mesh (D-10): one bidirectional P2P link to each other guest/co-host for the
        // thumbnails. The Publisher serves the one-way consumers (host monitor, OBS sources).
        const mesh = new MeshManager(
          room,
          () => streamRef.current,
          syncThumbnails,
          (pc, id) => watch.track(pc, "mesh:" + id),
          (id) => watch.untrack("mesh:" + id),
        );
        meshRef.current = mesh;
        // Route each relayed signal by the sender's roster role: a guest/co-host (not us) is a mesh
        // peer; the host or an OBS source consumes us over the Publisher. Deterministic-offerer mesh
        // (lower id offers) means we only ever receive a mesh ANSWER/ICE from a higher-id peer and a
        // mesh OFFER from a lower-id peer, so a single connection per pair — no ambiguity (D-23).
        room.on("signal", (f) => {
          // Screen channel (D-21): a peer pair can run a second P2P link for the screenshare track.
          // Route by `from`: an answer/ICE from the live sharer we're consuming goes to that consumer
          // link; anything else on the screen channel is a consumer (host / OBS) negotiating OUR
          // screen track → our screen Publisher (created only while we share).
          if (f.ch === "screen") {
            // An OFFER is always a consumer negotiating OUR screen → the publisher. It is ALSO proof
            // that any consumer link WE still hold to that peer is stale: a live re-select made us the
            // sharer they now consume, so we are no longer consuming them. Close that stale link NOW so
            // the peer's subsequent trickled ICE routes to the publisher (the ICE branch below sends to
            // a consumer link first), not into a dead link — otherwise the new connection can stay blank.
            const isOffer = f.sdp && f.sdp.type === "offer";
            if (isOffer) {
              const stale = screenConsumersRef.current.get(f.from);
              if (stale) {
                stale.close();
                screenConsumersRef.current.delete(f.from);
                if (liveScreenIdRef.current === f.from) {
                  liveScreenIdRef.current = "";
                  setLiveScreen(null);
                }
              }
              if (screenPubRef.current) screenPubRef.current.onSignal(f);
              return;
            }
            const consumer = screenConsumersRef.current.get(f.from);
            if (consumer) consumer.onSignal(f);
            else if (screenPubRef.current) screenPubRef.current.onSignal(f);
            return;
          }
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
            // Screenshare self-state (AC-13), folded into our OWN entry by the server. If it clears to
            // "" while we still hold a capture, reconcile against WHY. Any fresh roster supersedes a
            // prior deferred decision, so cancel a pending reconcile first and re-decide:
            //  - a share LOCK (set by BOTH host-pull paths: force-no-share directly, and an eligibility
            //    revoke that runs the same side-effect WHILE WE'RE PRESENT) → stop capturing locally
            //    (cooperative source-side stop — no {t:screen-stop} echo, the server already dropped us);
            //  - else if we're still eligible + live → a transient reconnect dropped us from the pool
            //    (the server ran `leave` on the old socket); re-assert {t:screen-start} to recover
            //    (parity with the camera republish);
            //  - else AMBIGUOUS (no lock, not eligible now): either the brief join roster before the
            //    eligibility re-seed (recovers within the handshake) OR a revoke that landed while we
            //    were disconnected (no lock created — we were absent). Defer; a follow-up roster
            //    supersedes, and if none arrives we're genuinely stranded (a capture the now-hidden
            //    .gs-screen control can't stop) → release it (codex).
            const share = me.screenShare || "";
            setScreenShare(share);
            if (pendingShareReconcileRef.current) {
              clearTimeout(pendingShareReconcileRef.current);
              pendingShareReconcileRef.current = null;
            }
            if (share === "" && screenStreamRef.current) {
              if (locked.includes("share")) {
                stopScreenCapture();
              } else if (canStartShare()) {
                // Recover into the pool. A throw here means the socket died again mid-recovery;
                // swallow and keep the capture — the next reconnect's roster re-asserts, or
                // onTerminal releases it. (Unlike the start path, we already hold a valid capture.)
                try {
                  sessionRef.current.send({ t: "screen-start" });
                } catch {
                  /* socket mid-close; a later roster re-asserts or onTerminal releases */
                }
              } else {
                pendingShareReconcileRef.current = setTimeout(() => {
                  pendingShareReconcileRef.current = null;
                  if (screenStreamRef.current && !canStartShare()) stopScreenCapture();
                }, SHARE_RECONCILE_MS);
              }
            }
            // While we are sharing but NOT the live share, only the host may consume our screen (the
            // backstage rail); force-drop any other established consumer so a viewer that watched us
            // live can't keep receiving our now-backstage screen (the relay gate only blocks new
            // signals). A no-op while live (everyone may consume) or when not publishing.
            if (screenStreamRef.current && share !== "live") pruneScreenConsumersToHost();
          }
          mesh.sync(selfIdRef.current, ps); // open/drop mesh links for the current backstage set
          // RF-8 (receiver-side): detach each OTHER peer's force-suppressed thumbnail track from the
          // mesh, independent of whether that peer cooperates at source — driven by the roster locks.
          for (const p of ps) {
            if (p.id !== selfIdRef.current && isMeshRole(p.role)) {
              mesh.setLocks(p.id, (p.locks || []).map((l) => l.kind));
            }
          }
          syncThumbnails();
          syncLiveScreen(ps); // open/drop the live-share consumer link (AC-11: live share for everyone)
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
          // RF-8: a (re)joining peer may already be force-locked (locks ride peer-joined too) — enforce.
          if (isMeshRole(f.peer.role) && f.peer.id !== selfIdRef.current) {
            mesh.setLocks(f.peer.id, (f.peer.locks || []).map((l) => l.kind));
          }
          syncThumbnails();
        });
        room.on("peer-left", (f) => {
          peersRef.current = peersRef.current.filter((p) => p.id !== f.peerId);
          setPeers(peersRef.current);
          // D-38: if the departed peer was a Publisher CONSUMER (the host monitor, peer "host"), drop
          // its pc so the connectivity watchdog stops counting a never-connected consumer that left
          // (a no-op for a mesh peer — the Publisher serves none — which mesh.sync below untracks).
          publisher.dropConsumer(f.peerId);
          // A departed peer is also a possible SCREEN consumer (the host rail / a live-share viewer)
          // and/or the live SHARER we were rendering — drop its screen-publisher pc and close any
          // screen consumer link to it, so no dead screen pc lingers.
          if (screenPubRef.current) screenPubRef.current.dropConsumer(f.peerId);
          const goneScreen = screenConsumersRef.current.get(f.peerId);
          if (goneScreen) {
            goneScreen.close();
            screenConsumersRef.current.delete(f.peerId);
            if (liveScreenIdRef.current === f.peerId) {
              liveScreenIdRef.current = "";
              setLiveScreen(null);
            }
          }
          mesh.sync(selfIdRef.current, peersRef.current);
          syncThumbnails();
        });
        // D-38: an OBS source consuming this guest departed (sources get no peer-left — they're hidden
        // from guest rosters, EN-13). Drop its Publisher pc so the watchdog untracks it; the peer id is
        // the same "src-<label>" the guest answered on the source's offer. The screen source ("src-
        // screen") consumes the screen Publisher, so drop it there too.
        room.on("consumer-left", (f) => {
          publisher.dropConsumer(f.peerId);
          if (screenPubRef.current) screenPubRef.current.dropConsumer(f.peerId);
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
          // The screen-channel connections share the rotated TURN credential (EN-4): our screen
          // Publisher (when sharing) and any live-share consumer link must refresh too, or they lose
          // relay access on rotation.
          if (screenPubRef.current) screenPubRef.current.applyIceServers(servers);
          for (const link of screenConsumersRef.current.values()) {
            try {
              link.pc.setConfiguration({ iceServers: servers });
            } catch {
              /* setConfiguration unsupported / pc closed — ignore */
            }
          }
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
        // Program quality ceiling (D-19/AC-8): the server delivers the stream ceiling on join and
        // re-broadcasts it when the host adjusts it live. Cap our program/monitor encoder at it (and
        // clamp degradation recovery to it). The ceiling is re-delivered on every reconnect.
        room.on("ceiling", (f) => {
          if (degRef.current) degRef.current.setCeiling({ maxRes: f.maxRes, maxFps: f.maxFps, maxBitrateKbps: f.maxBitrateKbps });
        });
        // Per-source program-resolution override (D-19/AC-8): an OBS source's ?res, relayed to us as
        // the bound occupant. Cap the sender feeding THAT source tighter; res<=0 clears it. The server
        // stamps the channel (D-21) so a /s/screen override caps our SCREEN sender (scrn:<sourceId>)
        // and a cam/host source our camera (pub:<sourceId>). f.peerId is the source's id.
        room.on("source-quality", (f) => {
          if (degRef.current) degRef.current.setSourceOverride((f.ch === "screen" ? "scrn:" : "pub:") + f.peerId, f.res);
        });
        // Test seam (no behavior, no secrets): expose the current encoding params of our PROGRAM/
        // monitor (protected) senders so a browser test can assert the quality ceiling (D-19/AC-8)
        // actually capped the program encoder. Mesh thumbnails are excluded — they ride their own
        // shedding, not the program ceiling.
        if (typeof window !== "undefined") {
          window.__gpPubEncodings = () =>
            degradationTargets()
              .filter((t) => t.protected)
              .map((t) => {
                const e = (t.sender.getParameters().encodings || [{}])[0] || {};
                return { key: t.key, scaleResolutionDownBy: e.scaleResolutionDownBy, maxFramerate: e.maxFramerate, maxBitrate: e.maxBitrate };
              });
        }
        // If a screen capture survived a reconnect, re-publish it on this fresh room so the share
        // recovers (parity with the camera Publisher above); the roster re-assert re-adds us to the
        // pool. The live-share consumer links re-open from the next roster (syncLiveScreen).
        if (screenStreamRef.current) startScreenPublisher();
      },
      teardown: () => {
        // The link dropped (or we're closing): stop the degradation sampler, stop publishing + tear
        // down the mesh (drop the dead peer connections), clear the thumbnails, and degrade the
        // reflected on-air + "we're live" + own-degradation state rather than assert stale values
        // (D-24). A reconnect re-arms all of it from the fresh roster + replay.
        if (degRef.current) degRef.current.stop();
        if (watchRef.current) watchRef.current.stop();
        if (pubRef.current) pubRef.current.close();
        if (meshRef.current) meshRef.current.close();
        // Drop the dead-room screen publisher + live-share consumer links; the capture stream is KEPT
        // (re-published on reconnect by setup), and the consumer links re-open from the fresh roster.
        closeScreenPublisher();
        closeScreenConsumers();
        setThumbnails([]);
        setOnAir("status-unavailable");
        setStreaming(false);
        setSelfDegraded(null);
      },
      onState: (st) => {
        pubStateRef.current = st; // keep the ref mirror current for async screen-capture closures
        setPubState(st); // "live" once up, "reconnecting" while a drop retries
      },
      onTerminal: (reason) => {
        // The session is over for good — release the camera/mic so the device light goes off behind
        // the error screen (the session won't reconnect, so nothing re-publishes this stream). The
        // screen capture is released too: no reconnect means no roster to drive the cooperative stop.
        terminatedRef.current = true; // a pending picker that resolves after this must not capture
        stopStream();
        stopScreenCapture();
        closeScreenConsumers(); // stop rendering any live share behind the terminal screen
        setTerminated(reason); // kicked/expired/revoked/session-ended/token-rotated/unreachable
      },
    });
  }

  async function enter() {
    // Lock the preview stream against an in-flight device switch (set synchronously, before any
    // await): from here the stream we hold is the one the publisher will take live, and a late
    // switch resolving mid-entry must not swap or stop it.
    enteringRef.current = true;
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
      // Leave enteringRef set: the error screen's only exit is "Try again" → startCheck, which
      // resets it. Holding it through the error phase makes a still-in-flight switch bail (it can't
      // install behind the error UI), and the picker isn't rendered here so no new switch can start.
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
  // Stop the local screen capture and clear the held stream. Used by an explicit stop, by the native
  // browser "Stop sharing" affordance (the track's `ended` event), by the host-pull roster sync, and
  // on teardown. Idempotent — safe to call when nothing is captured.
  function stopScreenCapture() {
    if (pendingShareReconcileRef.current) {
      clearTimeout(pendingShareReconcileRef.current);
      pendingShareReconcileRef.current = null;
    }
    closeScreenPublisher(); // drop our screen-channel publisher (consumers see the track end)
    if (screenStreamRef.current) {
      screenStreamRef.current.getTracks().forEach((t) => t.stop());
      screenStreamRef.current = null;
    }
  }
  // startScreenPublisher (re)creates our screen-channel Publisher on the CURRENT room, publishing the
  // held capture (D-21). Called when capture starts and again on reconnect (a fresh room) so the
  // share survives a blip. A no-op without a room or a capture. The screen pcs are deliberately NOT
  // watched by the D-38 watchdog (the camera owns connectivity detection) nor the degradation ladder.
  function startScreenPublisher() {
    if (!roomRef.current || !screenStreamRef.current) return;
    closeScreenPublisher();
    screenPubRef.current = new Publisher(roomRef.current, screenStreamRef.current, undefined, undefined, "screen");
  }
  function closeScreenPublisher() {
    if (screenPubRef.current) {
      screenPubRef.current.close();
      screenPubRef.current = null;
    }
  }
  // pruneScreenConsumersToHost drops every established screen-publisher consumer pc EXCEPT the host's
  // (D-21/EN-7). The relay authorization only blocks NEW screen signals, so when we stop being the
  // live sharer (live → backstage) an already-connected viewer would otherwise keep receiving our now
  // backstage screen at the media layer if it doesn't voluntarily close its link. Only the host may
  // consume a backstage preview (the host-only rail), so everyone else is force-dropped here. Host
  // consumer ids are resolved from the roster (the host is a participant, visible to guests).
  function pruneScreenConsumersToHost() {
    const pub = screenPubRef.current;
    if (!pub) return;
    const hostIds = new Set(peersRef.current.filter((p) => p.role === "host").map((p) => p.id));
    for (const id of pub.consumerIds()) {
      if (!hostIds.has(id)) pub.dropConsumer(id);
    }
  }
  // closeScreenConsumers tears down every live-share consumer link (the screen we render from another
  // peer) and clears the rendered live share. Called on each disconnect (the links bind to the dropped
  // room; a fresh roster re-opens them on the new room via syncLiveScreen) and on unmount/terminal.
  function closeScreenConsumers() {
    for (const link of screenConsumersRef.current.values()) link.close();
    screenConsumersRef.current.clear();
    liveScreenIdRef.current = "";
    setLiveScreen(null);
  }
  // syncLiveScreen reconciles THIS client's live-share consumer link against the roster (AC-11): open
  // a screen-channel PeerLink to the peer the server marks live (screenShare:"live", visible to ALL —
  // the screen-roster itself is host-only), render its track, and drop the link when the live share
  // clears or moves. Never consumes our OWN screen (we publish it; our self-state shows it instead).
  function syncLiveScreen(ps) {
    const room = roomRef.current;
    const livePeer = ps.find((p) => p.screenShare === "live" && p.id !== selfIdRef.current);
    const keep = livePeer ? livePeer.id : "";
    for (const [id, link] of screenConsumersRef.current) {
      if (id !== keep) {
        link.close();
        screenConsumersRef.current.delete(id);
      }
    }
    // Clear a stale render the instant the live sharer changes or clears, so we never show the
    // previous sharer's now-frozen screen while the new link negotiates (or after a take-off-air).
    if (liveScreenIdRef.current && liveScreenIdRef.current !== keep) {
      liveScreenIdRef.current = "";
      setLiveScreen(null);
    }
    if (!keep || !room) return;
    if (!screenConsumersRef.current.has(keep)) {
      const link = new PeerLink(room, keep, room.iceServers, "screen");
      screenConsumersRef.current.set(keep, link);
      link.pc.ontrack = (e) => {
        liveScreenIdRef.current = keep;
        setLiveScreen({ id: keep, name: nameOf(keep), stream: e.streams[0] });
      };
      link.pc.oniceconnectionstatechange = () => {
        if (link.pc.iceConnectionState === "failed") link.restartIce();
      };
      link.offer();
    } else {
      // Keep the live link across roster updates; refresh just the nameplate (a host rename).
      setLiveScreen((prev) => (prev && prev.id === keep ? { ...prev, name: nameOf(keep) } : prev));
    }
  }
  // nameOf resolves a peer id to its current roster display name (for the live-share nameplate).
  function nameOf(id) {
    const p = peersRef.current.find((x) => x.id === id);
    return (p && p.name) || "";
  }
  // sessionLive reports whether the signaling socket is usable right now (open + publishing). Read at
  // CALL time (via pubStateRef) so async + once-registered screen-capture closures never send on a
  // dropped/reconnecting/CONNECTING socket, which would throw.
  function sessionLive() {
    return !!sessionRef.current && pubStateRef.current === "live";
  }
  // stopScreenShare announces {t:screen-stop} (best-effort) and ALWAYS releases the local capture.
  // The send is wrapped because the WS can be mid-close — out of OPEN before onClose flips pubStateRef
  // away from "live" — so sessionLive() can still pass yet ws.send() throw; the local teardown must
  // run regardless, or the getDisplayMedia tracks (and the OS indicator) leak. The server drops us
  // from the pool on disconnect anyway, so a missed screen-stop is harmless.
  function stopScreenShare() {
    try {
      if (sessionLive()) sessionRef.current.send({ t: "screen-stop" });
    } catch {
      /* socket already closing/closed — fall through to the unconditional local stop */
    }
    stopScreenCapture();
  }
  // canStartShare reports whether STARTING a screen capture is allowed right now, from refs (so it's
  // correct after an `await`): the session must be live + not terminated/unmounting, and this guest's
  // OWN current roster entry must still be screenshare-eligible and not force-no-share'd (EN-7 — the
  // server rejects an ineligible/locked screen-start anyway, but capturing locally would leak behind a
  // share control that the revoke just hid). The host can revoke while the picker is open, so this is
  // re-checked after getDisplayMedia resolves, not only before.
  function canStartShare() {
    if (!sessionLive() || cancelledRef.current || terminatedRef.current) return false;
    const me = peersRef.current.find((p) => p.self || p.id === selfIdRef.current);
    if (!me || !me.canScreen) return false;
    return !(me.locks || []).some((l) => l.kind === "share");
  }
  // Screenshare capture toggle (AC-13/D-21). Video-only getDisplayMedia (D-41) — start grabs the
  // display surface, registers the native-stop `ended` handler, and announces {t:screen-start} so the
  // server adds us to the backstage preview pool; stop tears the capture down and announces
  // {t:screen-stop}. The host alone promotes a sharer to the live slot (host-only {t:screen-select}),
  // so starting only ever yields the "backstage" self-state until the server says otherwise.
  async function toggleScreen() {
    if (!sessionRef.current) return;
    if (screenStreamRef.current) {
      stopScreenShare(); // best-effort {t:screen-stop} + unconditional local capture release
      return;
    }
    if (!canStartShare()) return;
    if (screenRequestingRef.current) return; // a getDisplayMedia is already in flight (double-click)
    screenRequestingRef.current = true;
    let stream;
    try {
      stream = await navigator.mediaDevices.getDisplayMedia({ video: true });
    } catch {
      return; // user cancelled the picker or capture failed — stay idle, no frame sent
    } finally {
      screenRequestingRef.current = false;
    }
    // The picker can resolve long after it opened: if we unmounted, the session dropped/terminated,
    // the host revoked eligibility / force-no-share'd us, or another capture already won the slot
    // meanwhile, release this stream instead of leaking it (and don't send on a dead/rejecting
    // socket). Mirrors the cancelledRef guard on the camera's getUserMedia.
    if (screenStreamRef.current || !canStartShare()) {
      stream.getTracks().forEach((t) => t.stop());
      return;
    }
    screenStreamRef.current = stream;
    startScreenPublisher(); // publish the capture on the screen channel so consumers can render it
    // Native browser "Stop sharing" — mirror it back to the server so the pool drops us (only if the
    // socket is still live at that moment — pubStateRef, not a stale capture-time pubState).
    const vt = stream.getVideoTracks()[0];
    if (vt) {
      vt.onended = () => stopScreenShare(); // native "Stop sharing": same best-effort stop + release
    }
    // Announce we're sharing. The send can throw in the mid-close window (socket out of OPEN before
    // onClose flips pubStateRef) — the server then never registered us, so release the just-captured
    // stream rather than leaving it running orphaned with no pool entry.
    try {
      sessionRef.current.send({ t: "screen-start" });
    } catch {
      stopScreenCapture();
    }
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

  // retryNetwork is the network-blocked screen's Retry (D-38): tear down the publishing session (the
  // session close() runs teardown → stops the watch + closes the dead pcs) and return to the
  // device-check preview, so the guest can switch networks (Wi-Fi → phone hotspot) and re-enter. The
  // camera stays live for the preview, and POST /enter is idempotent, so re-entering just re-publishes.
  // The camera is kept for the preview, but any screen capture is released: this path closes the
  // session without unmounting / onTerminal / a roster clear, so it's the only place a held capture
  // would otherwise leak (the getDisplayMedia tracks + the OS indicator).
  function retryNetwork() {
    if (sessionRef.current) {
      sessionRef.current.close();
      sessionRef.current = null;
    }
    stopScreenCapture();
    enteringRef.current = false; // back to a switchable preview — re-allow device changes
    setNetBlocked(false);
    setPhase("preview");
  }

  // leave is the in-session "Leave the greenroom" action (DESIGN §6 guest-left): tear down the
  // publishing session and release the camera + any screen capture (the guest is done, so the device
  // lights go off), then show the guest-left screen. Voluntary — NOT a terminate; the guest can rejoin
  // while the stream is live (D-40). Mirrors retryNetwork's teardown, but stops the camera too.
  function leave() {
    if (sessionRef.current) {
      sessionRef.current.close();
      sessionRef.current = null;
    }
    stopScreenCapture();
    stopStream(); // the guest is leaving — release the camera/mic so the capture indicator goes off
    enteringRef.current = false;
    setLeft(true);
  }

  // rejoin returns from the guest-left screen to the device-check preview so the guest can re-check
  // their camera and re-enter (POST /enter is idempotent → re-publishes). Re-acquires the camera that
  // leave() released. Rejoin only succeeds while the stream is live; otherwise the re-entry surfaces
  // the matching state (host-waiting / link-off) like any fresh entry.
  function rejoin() {
    setLeft(false);
    startCheck();
  }

  // The guest LEFT voluntarily — a clear, recoverable screen with a rejoin path (DESIGN §6 guest-left).
  // Checked before the in-session/phase screens so the leave action wins immediately.
  if (left) {
    return (
      <div class="dc-left" data-state="guest-left">
        <p class="dc-left-title">You've left the greenroom</p>
        <p>You can rejoin while the stream is still live.</p>
        <button type="button" class="dc-rejoin" onClick={rejoin}>
          Rejoin
        </button>
        {/* GDPR purge notice (D-37 §8 / AC-6), the same reassurance the terminal screens carry. */}
        <p class="dc-left-privacy" data-privacy="purge">
          Your name and email will be deleted within 24 hours of the stream ending.
        </p>
      </div>
    );
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
        {/* GDPR "after" transparency notice (D-37 §8 / AC-6): once the guest's session is over,
            reassure them their PII is short-lived. Accurate for every terminal reason (the purge
            keys off stream end), and the counterpart to the "before" notice on the invite email +
            pass page. */}
        <p class="gs-terminal-privacy" data-privacy="purge">
          Your name and email will be deleted within 24 hours of the stream ending.
        </p>
      </div>
    );
  }

  // D-38: the guest's network blocks P2P (no media connection ever formed). This wins over the
  // entered/GuestSession view so the false "you're live" is replaced with a clear, actionable screen
  // — never a silent hang. Retry returns to the device-check preview to switch networks and re-enter.
  if (netBlocked) {
    return (
      <div class="dc-netblocked">
        <p class="dc-netblocked-kicker">Can't connect</p>
        <h2 class="dc-netblocked-title">Your network blocks peer-to-peer video</h2>
        <p class="dc-netblocked-body">
          GuestPass connects you directly to the room, but this network is blocking that. Try a
          different Wi-Fi network, or switch to your phone's hotspot, then rejoin.
        </p>
        <p class="dc-netblocked-note">
          Common on locked corporate or campus networks, VPNs, or strict firewalls. A phone hotspot
          almost always works.
        </p>
        <button type="button" class="dc-netblocked-retry" onClick={retryNetwork}>
          Retry
        </button>
      </div>
    );
  }

  if (phase === "entered") {
    // The in-session guest-session surface (AC-12), sharing this island's single pass-token Room.
    return (
      <GuestSession
        pubState={pubState}
        onLeave={leave}
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
        screenShare={screenShare}
        onToggleScreen={toggleScreen}
        liveScreen={liveScreen}
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
    // Distinct copy per failure kind (DESIGN §6 cam-blocked / unsupported), so the guest gets an
    // actionable next step instead of a raw error name. "unsupported" is terminal (no retry).
    if (errorKind === "unsupported") {
      return (
        <div class="dc-error" data-error-kind="unsupported">
          <p class="dc-error-title">This browser can't join the stream</p>
          <p>
            Joining needs camera, microphone, and peer-to-peer video support. Your browser doesn't
            offer it — this often happens in an app's built-in browser.
          </p>
          <p>Open this link in the latest Chrome, Edge, Firefox, or Safari and try again.</p>
        </div>
      );
    }
    if (errorKind === "blocked") {
      return (
        <div class="dc-error" data-error-kind="blocked">
          <p class="dc-error-title">Camera and microphone are blocked</p>
          <p>
            Your browser is blocking access to your camera and mic. Allow them for this site — use
            the camera icon in the address bar, or your browser's site settings — then try again.
          </p>
          <button type="button" class="dc-retry" onClick={startCheck}>
            Try again
          </button>
        </div>
      );
    }
    if (errorKind === "no-devices") {
      return (
        <div class="dc-error" data-error-kind="no-devices">
          <p class="dc-error-title">No camera or microphone found</p>
          <p>
            We couldn't find a camera or microphone to use. Connect one, close any other app that
            might be using it, then try again.
          </p>
          <button type="button" class="dc-retry" onClick={startCheck}>
            Try again
          </button>
        </div>
      );
    }
    return (
      <div class="dc-error" data-error-kind="generic">
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
        {/* Device picker (AC-5): choose the camera + mic before going live. Switching re-acquires
            the preview on the new device; the choice persists into the published stream. */}
        <div class="dc-devices">
          <label class="dc-device">
            <span class="dc-device-label">Camera</span>
            <select
              class="dc-device-select dc-cam-select"
              value={camId}
              disabled={phase === "entering" || switching}
              onChange={(e) => switchDevice("cam", /** @type {HTMLSelectElement} */ (e.currentTarget).value)}
            >
              {devices.cams.length === 0 ? <option value="">Default camera</option> : null}
              {devices.cams.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.label}
                </option>
              ))}
            </select>
          </label>
          <label class="dc-device">
            <span class="dc-device-label">Microphone</span>
            <select
              class="dc-device-select dc-mic-select"
              value={micId}
              disabled={phase === "entering" || switching}
              onChange={(e) => switchDevice("mic", /** @type {HTMLSelectElement} */ (e.currentTarget).value)}
            >
              {devices.mics.length === 0 ? <option value="">Default microphone</option> : null}
              {devices.mics.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.label}
                </option>
              ))}
            </select>
          </label>
        </div>
        {switchError ? (
          <p class="dc-switch-error" role="status">
            Couldn't switch to that device — staying on your current one.
          </p>
        ) : null}
        <p>This is your camera preview. Only you can see it until you enter.</p>
        <button type="button" class="dc-enter" disabled={phase === "entering" || switching} onClick={enter}>
          {switching ? "Switching device…" : phase === "entering" ? "Entering…" : "Enter the greenroom"}
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
