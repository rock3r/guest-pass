import "./greenroom.css";
import { render } from "preact";
import { useState, useRef, useEffect } from "preact/hooks";
import { Room } from "../rtc/room.js";
import { PeerLink } from "../rtc/peerlink.js";
import { Tile, FORCE_FRAME } from "./grid-tile.js";

/**
 * Greenroom is the host's live multi-guest monitoring grid (D-10/AC-10): it connects to the
 * signaling WS as the host (session cookie), and for every guest/co-host in the role-filtered
 * roster it consumes that peer's camera over P2P and renders a tile (the shared grid-tile, also
 * used by the guest-session backstage thumbnails). Each tile shows the name, the three-state
 * on-air pill (D-24), a force-lock notice (D-13) and signal bars — all read from the roster entry,
 * so a roster re-broadcast updates a tile WITHOUT churning its live P2P link. From each tile a
 * moderator acts within rank (D-13/D-15): force/release a modality, promote/demote (host-only), and
 * dismiss a raised hand. Authority is enforced server-side (EN-7); the controls are shown by the
 * viewer's own rank only as a convenience. Functional-first styling (D-B).
 */

const isGuestRole = (role) => role === "guest" || role === "cohost";

/**
 * Greenroom is the grid island.
 * @returns {import("preact").VNode}
 */
function Greenroom() {
  /** @type {[Array<{id:string, entry:any, stream:MediaStream|null}>, Function]} */
  const [tiles, setTiles] = useState([]);
  const [state, setState] = useState("connecting"); // connecting | live | error
  // viewerRole is this client's own rank (from its self roster entry), so the grid shows only the
  // moderation controls the viewer may use. The /greenroom host is "host"; the grid is reused for
  // a co-host in the guest-session (PR-11).
  const [viewerRole, setViewerRole] = useState("host");
  // bindError surfaces a failed slot (re)bind to the host: the picker is controlled by
  // entry.boundSlot (roster-driven), so a rejected PUT snaps the picker back AND shows why
  // (e.g. 404 when slots aren't provisioned yet — the host must open the Sources tab first).
  const [bindError, setBindError] = useState("");
  // boundOverrides keeps a picker selection visible when the bind persisted but produced NO roster
  // frame — i.e. a DB-only (pre-live) bind: boundSlot is derived from LIVE occupancy, so before the
  // host goes live nothing updates entry.boundSlot and the controlled select would snap back. Keyed
  // by pass id → the slot the host last successfully set (""=unassigned). The roster clears an entry
  // once the authoritative live value catches up (e.g. on Go-live replay).
  const [boundOverrides, setBoundOverrides] = useState({});
  /** @type {{current: import("../rtc/room.js").Room|null}} */
  const roomRef = useRef(null);
  /** @type {{current: Map<string, import("../rtc/peerlink.js").PeerLink>}} */
  const linksRef = useRef(new Map());
  /** @type {{current: Map<string, MediaStream>}} */
  const streamsRef = useRef(new Map());
  /** @type {{current: Map<string, any>}} */
  const entriesRef = useRef(new Map());
  // Per-pass bind sequence: the host can change a picker twice quickly, so multiple PUTs are in
  // flight. Pre-live there's no roster to reconcile, so an OLDER response landing last would
  // overwrite the newer selection — track the latest request per pass and ignore stale responses.
  /** @type {{current: Object<string, number>}} */
  const bindSeqRef = useRef({});

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
        applyLocks(id); // re-assert any active suppression lock on the freshly-arrived track (RF-8)
        syncTiles();
      };
      link.pc.oniceconnectionstatechange = () => {
        if (link.pc.iceConnectionState === "failed") link.restartIce();
      };
      link.offer();
    }

    // applyLocks enforces a peer's suppression locks on its live monitor link (RF-8 receiver-side):
    // detach each locked modality's REMOTE track from this tile (and re-attach a released one),
    // independent of whether the locked peer cooperates at source. Driven by the roster's entry.locks.
    function applyLocks(id) {
      const link = linksRef.current.get(id);
      const entry = entriesRef.current.get(id);
      if (!link || !entry) return;
      const locked = new Set((entry.locks || []).map((l) => l.kind));
      for (const m of ["mic", "cam", "share"]) link.setRemoteTrackEnabled(m, !locked.has(m));
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
      applyLocks(entry.id); // a roster / peer-joined update may have changed locks → enforce (RF-8)
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
      // This client's own rank drives which controls show (it can change live via demotion).
      const me = (f.peers || []).find((p) => p.self || p.id === f.self);
      if (me) setViewerRole(me.role);
      // Release a local (pre-live DB-only) override once the authoritative roster makes it stale:
      //  - the pass left the room, or
      //  - the pass is now itself live-bound (its own boundSlot is set — e.g. Go-live replay), or
      //  - a DIFFERENT peer now holds the override's slot (a displacement — another tab/the API
      //    rebound that slot to someone else; codex).
      // A still-free slot with the pass not yet live-bound is left as a pending pre-live selection.
      setBoundOverrides((prev) => {
        if (!Object.keys(prev).length) return prev;
        const ownBound = {}; // peer id -> its live boundSlot ("" if none); presence = in roster
        const holder = {}; // slot -> the peer id live-bound to it
        for (const p of f.peers || []) {
          ownBound[p.id] = p.boundSlot || "";
          if (p.boundSlot) holder[p.boundSlot] = p.id;
        }
        let changed = false;
        const next = { ...prev };
        for (const pid of Object.keys(next)) {
          const slot = next[pid];
          const stale = !(pid in ownBound) || ownBound[pid] || (slot && holder[slot] && holder[slot] !== pid);
          if (stale) {
            delete next[pid];
            changed = true;
          }
        }
        return changed ? next : prev;
      });
      syncTiles();
      setState((s) => (s === "connecting" ? "live" : s));
    });
    room.on("session-live", () => {
      // The host's session just went live: the roster now carries the authoritative live bindings,
      // so drop ALL optimistic pre-live overrides. A pass unassigned/displaced from another client
      // before Go live would otherwise keep showing its stale slot while OBS shows the placeholder
      // (codex). Sent after the replay, so entry.boundSlot is already authoritative.
      setBoundOverrides((prev) => (Object.keys(prev).length ? {} : prev));
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

  // bindSlot is the host-only People control (AC-6/D-20): (re)assign a guest to a cam slot (or
  // "" to unassign) over the REST endpoint. The server persists passes.slot_id AND live-re-routes
  // /s/{slot} with no OBS edit, then re-broadcasts the roster — so entry.boundSlot (and the
  // picker) reflect the new assignment. Same-origin fetch carries the host cookie; CSRF is the
  // SameSite=Lax cookie (a cross-site request can't send it) + connect-src 'self'.
  function bindSlot(passId, slot) {
    const seq = (bindSeqRef.current[passId] || 0) + 1;
    bindSeqRef.current[passId] = seq;
    const isStale = () => bindSeqRef.current[passId] !== seq; // a newer bind for this pass superseded us
    fetch(`/api/passes/${encodeURIComponent(passId)}/slot`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ slot }),
    })
      .then(async (r) => {
        if (r.ok) {
          const body = await r.json().catch(() => ({}));
          if (isStale()) return; // ignore an out-of-order response (codex)
          setBindError("");
          const newSlot = (body && body.boundSlot) || "";
          if (body && body.live) {
            // A LIVE bind: the authoritative roster already carries the new boundSlot, so keep NO
            // override — holding one would mask a later unassign/displace whose roster reports "".
            // Drop any stale override for this pass (e.g. one left from an earlier DB-only bind).
            setBoundOverrides((prev) => {
              if (!(passId in prev)) return prev;
              const next = { ...prev };
              delete next[passId];
              return next;
            });
          } else {
            // A DB-only (pre-live) bind produces no roster frame, so reflect the persisted selection
            // locally (""=unassigned). Drop any other pass's override on the same slot (displacement).
            setBoundOverrides((prev) => {
              const next = { ...prev };
              if (newSlot) for (const k of Object.keys(next)) if (next[k] === newSlot) delete next[k];
              next[passId] = newSlot;
              return next;
            });
          }
          return;
        }
        // A server-side rejection (404 unprovisioned, 400 bad slot, 5xx) returns no roster
        // update, so the controlled picker stays put; tell the host why it didn't move.
        let msg = "Couldn't update the slot.";
        try {
          const body = await r.json();
          if (body && body.error) msg = body.error;
        } catch (_) {
          /* non-JSON body: keep the generic message */
        }
        if (isStale()) return; // a newer bind superseded this rejected one
        setBindError(msg);
        rollbackPicker();
      })
      .catch(() => {
        // fetch only rejects on a network/transport failure; the binding is unchanged.
        if (isStale()) return;
        setBindError("Couldn't reach the server to update the slot.");
        rollbackPicker();
      });
  }

  // rollbackPicker forces a grid re-render so a rejected pick reverts to the authoritative
  // entry.boundSlot. setBindError alone is a no-op when the SAME message is already shown (e.g.
  // every unprovisioned slot returns the same 404), and then Preact wouldn't reconcile the
  // controlled <select> back — leaving the invalid choice on screen (codex). A fresh tiles array
  // ref guarantees the render.
  function rollbackPicker() {
    setTiles((ts) => [...ts]);
  }

  return (
    <div class="greenroom" data-state={state}>
      <div class="gr-toolbar">
        {/* Host-only "bump quality now" (AD-21/D-34): broadcasts {t:recover-quality} so every
            publisher recovers immediately, overriding the slow recover hysteresis. */}
        <button
          type="button"
          class="gr-recover"
          disabled={state !== "live"}
          onClick={() => roomRef.current?.send({ t: "recover-quality" })}
        >
          Bump quality now
        </button>
        {bindError ? (
          <p class="gr-binderr" role="alert">
            {bindError}
          </p>
        ) : null}
      </div>
      <div class="greenroom-grid" data-state={state} data-count={tiles.length}>
        {tiles.length === 0 ? (
          <p class="gr-empty" data-state={state}>
            Waiting for guests to join…
          </p>
        ) : (
          tiles.map((t) => (
          <Tile
            key={t.id}
            entry={t.id in boundOverrides ? { ...t.entry, boundSlot: boundOverrides[t.id] } : t.entry}
            stream={t.stream}
            viewerRole={viewerRole}
            live={state === "live"}
            onReconnect={() => {
              const link = linksRef.current.get(t.id);
              if (link) link.restartIce();
            }}
            onForce={(m) => roomRef.current?.send({ t: FORCE_FRAME[m], peerId: t.id })}
            onRelease={(m) => roomRef.current?.send({ t: "release", peerId: t.id, kind: m })}
            onRole={(role) => roomRef.current?.send({ t: "role", peerId: t.id, role })}
            onDismissHand={() => roomRef.current?.send({ t: "hand", peerId: t.id, raised: false })}
            onBindSlot={(slot) => bindSlot(t.id, slot)}
          />
          ))
        )}
      </div>
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
