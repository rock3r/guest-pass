import "./greenroom.css";
import { render } from "preact";
import { useState, useRef, useEffect } from "preact/hooks";
import { Room } from "../rtc/room.js";
import { PeerLink } from "../rtc/peerlink.js";

/**
 * Greenroom is the host's live multi-guest monitoring grid (D-10/AC-10): it connects to the
 * signaling WS as the host (session cookie), and for every guest/co-host in the role-filtered
 * roster it consumes that peer's camera over P2P and renders a tile. Each tile shows the name,
 * the three-state on-air pill (D-24), a force-lock notice (D-13) and signal bars — all read from
 * the roster entry, so a roster re-broadcast updates a tile WITHOUT churning its live P2P link.
 * The moderation CONTROLS (force/release, promote/demote, hand-raise actions) land in PR-10;
 * this island is the read-only grid. Functional-first styling (D-B).
 */

const isGuestRole = (role) => role === "guest" || role === "cohost";

/**
 * onAirLabel maps the three-state on-air to its pill copy (D-24). status-unavailable means no
 * live OBS signal — never asserted as on/off.
 * @param {string} onAir
 */
function onAirLabel(onAir) {
  if (onAir === "on-air") return "On air";
  if (onAir === "not-on-air") return "Not on air";
  return "On-air status unavailable";
}

// Per-modality force-lock notice copy (M3 plan default), shown when a lock is active.
const LOCK_COPY = {
  mic: "Muted by host",
  cam: "Camera turned off by host",
  share: "Screen share stopped by host",
};

/**
 * lockNotices renders the distinct force-lock notices for a roster entry's locks.
 * @param {Array<{kind:string}>} [locks]
 * @returns {string[]}
 */
function lockNotices(locks) {
  return (locks || []).map((l) => LOCK_COPY[l.kind]).filter(Boolean);
}

/**
 * Tile renders one guest's P2P video plus its roster-driven status chrome. The stream attaches
 * via an effect so a re-render (e.g. an on-air change) never reloads the <video>. A failed ICE
 * path auto-restarts; the Reconnect control forces an ICE restart for a stuck tile.
 * @param {{entry:any, stream:MediaStream|null, onReconnect:()=>void}} props
 * @returns {import("preact").VNode}
 */
function Tile({ entry, stream, onReconnect }) {
  /** @type {{current: HTMLVideoElement|null}} */
  const videoRef = useRef(null);
  useEffect(() => {
    if (videoRef.current) videoRef.current.srcObject = stream || null;
  }, [stream]);
  const notices = lockNotices(entry.locks);
  return (
    <div class="gr-tile" data-guest={entry.id} data-role={entry.role}>
      <video ref={videoRef} class="gr-video" data-guest={entry.id} autoplay playsinline muted />
      <div class="gr-meta">
        <span class="gr-name">{entry.name || entry.id}</span>
        <span class="gr-pill" data-onair={entry.onAir || "status-unavailable"}>
          {onAirLabel(entry.onAir)}
        </span>
        <span class="gr-signal" data-signal={entry.signal || 0} title="Connection health" />
        <button type="button" class="gr-reconnect" onClick={onReconnect}>
          Reconnect
        </button>
      </div>
      {notices.length > 0 ? (
        <p class="gr-lock" data-locked="1">
          {notices.join(" · ")}
        </p>
      ) : null}
      {entry.handRaised ? (
        <span class="gr-hand" data-hand="1">
          ✋ Hand raised
        </span>
      ) : null}
    </div>
  );
}

/**
 * Greenroom is the grid island.
 * @returns {import("preact").VNode}
 */
function Greenroom() {
  /** @type {[Array<{id:string, entry:any, stream:MediaStream|null}>, Function]} */
  const [tiles, setTiles] = useState([]);
  const [state, setState] = useState("connecting"); // connecting | live | error
  /** @type {{current: import("../rtc/room.js").Room|null}} */
  const roomRef = useRef(null);
  /** @type {{current: Map<string, import("../rtc/peerlink.js").PeerLink>}} */
  const linksRef = useRef(new Map());
  /** @type {{current: Map<string, MediaStream>}} */
  const streamsRef = useRef(new Map());
  /** @type {{current: Map<string, any>}} */
  const entriesRef = useRef(new Map());

  useEffect(() => {
    const room = new Room(""); // host: the session cookie authenticates the WS
    roomRef.current = room;

    // syncTiles rebuilds the rendered tiles from the current entries + streams, in a stable id
    // order so the grid doesn't reshuffle on every roster update.
    function syncTiles() {
      const ids = [...entriesRef.current.keys()].sort();
      setTiles(
        ids.map((id) => ({
          id,
          entry: entriesRef.current.get(id),
          stream: streamsRef.current.get(id) || null,
        })),
      );
    }

    function ensureLink(id) {
      if (linksRef.current.has(id)) return; // keep the live link across roster updates
      const link = new PeerLink(room, id, room.iceServers);
      linksRef.current.set(id, link);
      link.pc.ontrack = (e) => {
        streamsRef.current.set(id, e.streams[0]);
        syncTiles();
      };
      link.pc.oniceconnectionstatechange = () => {
        if (link.pc.iceConnectionState === "failed") link.restartIce();
      };
      link.offer();
    }

    function dropPeer(id) {
      const link = linksRef.current.get(id);
      if (link) link.close();
      linksRef.current.delete(id);
      streamsRef.current.delete(id);
      entriesRef.current.delete(id);
    }

    function upsert(entry) {
      if (!entry || !isGuestRole(entry.role)) return; // grid renders guests/co-hosts only
      entriesRef.current.set(entry.id, entry);
      ensureLink(entry.id);
    }

    room.on("roster", (f) => {
      // The roster is authoritative: add/update guest entries, and drop any peer no longer in it.
      const present = new Set();
      for (const p of f.peers || []) {
        if (isGuestRole(p.role)) {
          present.add(p.id);
          upsert(p);
        }
      }
      for (const id of [...entriesRef.current.keys()]) {
        if (!present.has(id)) dropPeer(id);
      }
      syncTiles();
      setState((s) => (s === "connecting" ? "live" : s));
    });
    room.on("peer-joined", (f) => {
      upsert(f.peer);
      syncTiles();
    });
    room.on("peer-left", (f) => {
      dropPeer(f.peerId);
      syncTiles();
    });
    room.on("signal", (f) => {
      const link = linksRef.current.get(f.from);
      if (link) link.onSignal(f);
    });
    room.onIce((servers) => {
      for (const link of linksRef.current.values()) {
        try {
          link.pc.setConfiguration({ iceServers: servers });
        } catch (_) {
          /* ignore */
        }
      }
    });
    room.ready.then(() => setState((s) => (s === "connecting" ? "live" : s))).catch(() => setState("error"));
    room.onClose(() => {
      for (const link of linksRef.current.values()) link.close();
      linksRef.current.clear();
      streamsRef.current.clear();
      entriesRef.current.clear();
      setTiles([]);
      setState("error");
    });

    return () => {
      for (const link of linksRef.current.values()) link.close();
      room.close();
    };
  }, []);

  return (
    <div class="greenroom-grid" data-state={state} data-count={tiles.length}>
      {tiles.length === 0 ? (
        <p class="gr-empty" data-state={state}>
          Waiting for guests to join…
        </p>
      ) : (
        tiles.map((t) => (
          <Tile
            key={t.id}
            entry={t.entry}
            stream={t.stream}
            onReconnect={() => {
              const link = linksRef.current.get(t.id);
              if (link) link.restartIce();
            }}
          />
        ))
      )}
    </div>
  );
}

/**
 * mountGreenroom renders the greenroom grid into root.
 * @param {HTMLElement} root
 */
export function mountGreenroom(root) {
  render(<Greenroom />, root);
}
