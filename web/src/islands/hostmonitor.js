import { render } from "preact";
import { useState, useRef, useEffect } from "preact/hooks";
import { Room } from "../rtc/room.js";
import { PeerLink } from "../rtc/peerlink.js";

/**
 * HostMonitor is the thin M2 host-monitor surface (PD-1): it connects to the greenroom
 * signaling WS as the host (session cookie), watches the roster for a guest, consumes that
 * guest's camera over P2P, and renders it in one tile. The full multi-guest greenroom grid,
 * moderation, and on-air UI are M3. A Reconnect control re-runs ICE (a path can drop on a
 * network change); a failed path also auto-restarts.
 *
 * @returns {import("preact").VNode}
 */
function HostMonitor() {
  const [guestId, setGuestId] = useState(/** @type {string|null} */ (null));
  const [state, setState] = useState("connecting"); // connecting | waiting | live | error
  /** @type {{current: HTMLVideoElement|null}} */
  const videoRef = useRef(null);
  /** @type {{current: import("../rtc/room.js").Room|null}} */
  const roomRef = useRef(null);
  /** @type {{current: import("../rtc/peerlink.js").PeerLink|null}} */
  const linkRef = useRef(null);
  const guestRef = useRef(/** @type {string|null} */ (null));

  useEffect(() => {
    const room = new Room(""); // host: the session cookie authenticates the WS
    roomRef.current = room;
    const isGuest = (role) => role === "guest" || role === "cohost";

    function connectTo(id) {
      if (linkRef.current || !id) return; // M2 shows a single tile
      guestRef.current = id;
      setGuestId(id);
      const link = new PeerLink(room, id, room.iceServers);
      linkRef.current = link;
      link.pc.ontrack = (e) => {
        if (videoRef.current) videoRef.current.srcObject = e.streams[0];
        setState("live");
      };
      link.pc.oniceconnectionstatechange = () => {
        if (link.pc.iceConnectionState === "failed") link.restartIce();
      };
      link.offer();
    }

    function teardown() {
      if (linkRef.current) linkRef.current.close();
      linkRef.current = null;
      guestRef.current = null;
      if (videoRef.current) videoRef.current.srcObject = null;
      setGuestId(null);
      setState("waiting");
    }

    // Apply a refreshed ICE config (rotated TURN credential, EN-4) to the live consumer.
    room.onIce((servers) => {
      if (linkRef.current) {
        try {
          linkRef.current.pc.setConfiguration({ iceServers: servers });
        } catch (_) {
          /* ignore */
        }
      }
    });

    room.onClose(() => {
      teardown();
      setState("error");
    });

    room.ready.then(() => setState((s) => (s === "connecting" ? "waiting" : s))).catch(() => setState("error"));
    room.on("roster", (f) => {
      for (const p of f.peers || []) {
        if (isGuest(p.role)) {
          connectTo(p.id);
          break;
        }
      }
    });
    room.on("peer-joined", (f) => {
      if (f.peer && isGuest(f.peer.role)) connectTo(f.peer.id);
    });
    room.on("peer-left", (f) => {
      // M2 shows a single tile; when the shown guest leaves we wait for the next join. Picking
      // among several remaining guests is the multi-guest greenroom grid, deferred to M3 (PD-1).
      if (f.peerId === guestRef.current) teardown();
    });
    room.on("signal", (f) => {
      if (linkRef.current && f.from === guestRef.current) linkRef.current.onSignal(f);
    });

    return () => {
      teardown();
      room.close();
    };
  }, []);

  function reconnect() {
    if (linkRef.current) linkRef.current.restartIce();
  }

  return (
    <div class="host-monitor">
      <h2>Greenroom monitor</h2>
      {guestId ? (
        <div class="hm-tile-wrap">
          <video
            ref={videoRef}
            class="hm-tile"
            data-guest={guestId}
            data-state={state}
            autoplay
            playsinline
            muted
          />
          <button type="button" class="hm-reconnect" onClick={reconnect}>
            Reconnect
          </button>
        </div>
      ) : (
        <p class="hm-empty" data-state={state}>
          Waiting for a guest to join…
        </p>
      )}
    </div>
  );
}

/**
 * mountHostMonitor renders the host-monitor island into root.
 * @param {HTMLElement} root
 */
export function mountHostMonitor(root) {
  render(<HostMonitor />, root);
}
