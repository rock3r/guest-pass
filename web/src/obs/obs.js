import "./obs.css";
import { Room } from "../rtc/room.js";
import { PeerLink } from "../rtc/peerlink.js";

/**
 * OBS cam source page (EN-13): a chromeless render surface OBS loads as a browser source.
 * It is intentionally NOT a Preact island — it has no UI, just a full-bleed <video> — so it
 * stays a tiny, font-free bundle separate from the app island bundle (AD-7).
 *
 * It authenticates the signaling WS with the slot's source token, read from the URL and
 * NEVER written to the DOM (EN-15), then follows the slot's binding: a {t:slot-rebind} frame
 * names the occupant to consume, a {t:slot-unbound} clears the surface. The bound occupant's
 * camera is rendered into #obs-video over a recvonly P2P link (PeerLink, the consumer side).
 *
 * Unlike the interactive islands it never surfaces an error state: a dropped signaling socket
 * auto-reconnects with capped exponential backoff, so the source self-heals after a server
 * restart or a transient network blip without anyone touching OBS.
 */

const RECONNECT_MIN_MS = 500;
const RECONNECT_MAX_MS = 10_000;

function start() {
  /** @type {HTMLVideoElement|null} */
  const video = /** @type {any} */ (document.getElementById("obs-video"));
  // EN-15: the token authenticates the WS the page opens, it is not page state — read it
  // from the URL and keep it out of the DOM entirely.
  const token = new URLSearchParams(location.search).get("token") || "";

  // State that must survive a reconnect (a fresh Room is built on each retry).
  /** @type {PeerLink|null} */
  let link = null;
  /** @type {string|null} the peer id currently bound to this slot */
  let occupant = null;
  // The slot epoch we last acted on. A frame from an older epoch is a stale straggler and is
  // ignored (EN-3); the server's epoch is monotonic per slot, so a fresh connection's binding
  // frame is always >= this. Reset on disconnect so the reconnect's binding is always taken.
  let epoch = -1;
  let backoff = RECONNECT_MIN_MS;
  /** @type {ReturnType<typeof setTimeout>|undefined} */
  let reconnectTimer;

  function clearLink() {
    if (link) link.close();
    link = null;
    occupant = null;
    if (video) video.srcObject = null;
  }

  function connect() {
    const room = new Room("src=" + encodeURIComponent(token));

    // bind the slot to occupantPeerId by opening a recvonly link and rendering its track.
    function bind(occupantPeerId, ep) {
      if (typeof ep === "number" && ep < epoch) return; // stale epoch (EN-3)
      if (typeof ep === "number") epoch = ep;
      clearLink();
      occupant = occupantPeerId;
      const l = new PeerLink(room, occupantPeerId, room.iceServers);
      link = l;
      l.pc.ontrack = (e) => {
        if (video) video.srcObject = e.streams[0];
      };
      l.pc.oniceconnectionstatechange = () => {
        // A dropped path (NAT rebind, network change) self-heals with an ICE restart rather
        // than tearing down the link — OBS keeps the last frame until the path recovers.
        if (l.pc.iceConnectionState === "failed") l.restartIce();
      };
      l.offer();
    }

    function unbind(ep) {
      if (typeof ep === "number" && ep < epoch) return; // stale epoch (EN-3)
      if (typeof ep === "number") epoch = ep;
      clearLink();
    }

    // A rotated TURN credential (EN-4) is pushed to the live consumer without renegotiating.
    room.onIce((servers) => {
      if (link) {
        try {
          link.pc.setConfiguration({ iceServers: servers });
        } catch (_) {
          /* setConfiguration is best-effort; the next negotiation still uses the new servers */
        }
      }
    });

    room.on("slot-rebind", (f) => bind(f.occupantPeerId, f.epoch));
    room.on("slot-unbound", (f) => unbind(f.epoch));
    room.on("signal", (f) => {
      if (link && f.from === occupant) link.onSignal(f);
    });

    // A clean connection resets the backoff so the NEXT drop retries fast again.
    room.ready.then(() => {
      backoff = RECONNECT_MIN_MS;
    }).catch(() => {
      /* the onclose handler drives the reconnect; nothing to do here */
    });

    room.onClose(() => {
      clearLink();
      epoch = -1; // accept whatever the reconnect's binding frame reports
      clearTimeout(reconnectTimer);
      reconnectTimer = setTimeout(connect, backoff);
      backoff = Math.min(backoff * 2, RECONNECT_MAX_MS);
    });
  }

  connect();
}

start();
