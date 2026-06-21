import "./greenroom.css";
import { render } from "preact";
import { useState, useRef, useEffect } from "preact/hooks";
import { ReconnectingSession } from "../rtc/session.js";
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
 * CeilingControl is the host-only stream-wide program quality ceiling control (D-19/AC-8): max
 * resolution / fps / bitrate inputs + Apply. Submitting PUTs the ceiling; the server clamps,
 * persists, and re-broadcasts {t:ceiling} to live publishers so each re-caps its program encoder.
 * Inputs are UNCONTROLLED, keyed by the current values so a server clamp or external update re-seeds
 * them. Shown only while a session is live (the parent renders it only when `ceiling` is set).
 * @param {{ceiling:{streamId:string,maxRes:number,maxFps:number,maxBitrateKbps:number}, onApply:(streamId:string,maxRes:number,maxFps:number,maxBitrateKbps:number)=>void}} props
 * @returns {import("preact").VNode}
 */
function CeilingControl({ ceiling, onApply }) {
  return (
    <form
      class="gr-ceiling"
      onSubmit={(e) => {
        e.preventDefault();
        const el = /** @type {HTMLFormElement} */ (e.currentTarget).elements;
        const num = (name) => parseInt(/** @type {HTMLInputElement} */ (el.namedItem(name)).value, 10) || 0;
        onApply(ceiling.streamId, num("maxRes"), num("maxFps"), num("maxBitrateKbps"));
      }}
    >
      <span class="gr-ceiling-label">Quality ceiling</span>
      <label class="gr-ceiling-field">
        <span>Res</span>
        <input key={"r" + ceiling.maxRes} class="gr-ceiling-res" name="maxRes" type="number" min="144" max="2160" defaultValue={ceiling.maxRes} />
        <span class="gr-ceiling-unit">p</span>
      </label>
      <label class="gr-ceiling-field">
        <span>FPS</span>
        <input key={"f" + ceiling.maxFps} class="gr-ceiling-fps" name="maxFps" type="number" min="1" max="60" defaultValue={ceiling.maxFps} />
      </label>
      <label class="gr-ceiling-field">
        <span>Kbps</span>
        <input
          key={"b" + ceiling.maxBitrateKbps}
          class="gr-ceiling-bitrate"
          name="maxBitrateKbps"
          type="number"
          min="100"
          max="20000"
          defaultValue={ceiling.maxBitrateKbps}
        />
      </label>
      <button type="submit" class="gr-ceiling-apply">
        Apply
      </button>
    </form>
  );
}

/**
 * ScreenTile renders one sharer in the host's screenshare rail (D-21/AC-11): the consumed screen
 * video (attached via an effect so a re-render doesn't reload it), the sharer's name, and either a
 * "Put live" select control or the live badge. Select-live is host-only (D-11 exception); the server
 * enforces authority (EN-7). The screen capture is video-only (D-41), so the video is muted.
 * @param {{tile:{id:string,name:string,stream:MediaStream|null,live:boolean}, live:boolean, onSelect:()=>void}} props
 * @returns {import("preact").VNode}
 */
function ScreenTile({ tile, live, onSelect }) {
  /** @type {{current: HTMLVideoElement|null}} */
  const ref = useRef(null);
  useEffect(() => {
    if (ref.current) ref.current.srcObject = tile.stream || null;
  }, [tile.stream]);
  return (
    <figure class="gr-screen-tile" data-sharer={tile.id} data-live={tile.live ? "1" : "0"}>
      <video ref={ref} class="gr-screen-video" autoplay playsinline muted />
      <figcaption class="gr-screen-cap">
        <span class="gr-screen-name">{tile.name || "Guest"}</span>
        {tile.live ? (
          <span class="gr-screen-livebadge" data-live="1">
            ● Live
          </span>
        ) : (
          <button type="button" class="gr-screen-select" disabled={!live} onClick={onSelect}>
            Put live
          </button>
        )}
      </figcaption>
    </figure>
  );
}

/**
 * Greenroom is the grid island.
 * @returns {import("preact").VNode}
 */
function Greenroom() {
  /** @type {[Array<{id:string, entry:any, stream:MediaStream|null}>, Function]} */
  const [tiles, setTiles] = useState([]);
  // connecting | live | error (a recoverable drop) | ended (a TERMINAL session-ended teardown —
  // the host ended the session, or an admin force-ended it via the D-27 cascade, EN-9).
  const [state, setState] = useState("connecting");
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
  // ceiling is the active session's program quality ceiling (D-19/AC-8): {streamId, maxRes, maxFps,
  // maxBitrateKbps} when a session is live, else null (the control is hidden until Go live). Fetched
  // from GET /api/session/ceiling on mount + on session-live, and updated from each adjust's response.
  const [ceiling, setCeiling] = useState(null);
  // Screenshare preview-switcher (D-21/AC-11): the host-only rail of every active sharer + the live
  // selection. screenTiles is the rendered list [{id,name,stream,live}] (sharers ∪ the live one), and
  // screenLive marks which is on-air. Driven by the host-only {t:screen-roster} broadcast.
  /** @type {[Array<{id:string,name:string,stream:MediaStream|null,live:boolean}>, Function]} */
  const [screenTiles, setScreenTiles] = useState([]);
  const [screenLive, setScreenLive] = useState("");
  // verifiedLive folds the D-29 live-verify result into the D-24 broadcast layer (AC-8): {status,
  // platform, watchURL} for the linked channel, polled from /api/streams/{id}/livecheck while a
  // session is live, or null when no channel is linked. Lets the host confirm "live (verified on
  // Twitch)" even when OBS gives status-unavailable.
  const [verifiedLive, setVerifiedLive] = useState(null);
  /** @type {{current: import("../rtc/room.js").Room|null}} */
  const roomRef = useRef(null);
  /** @type {{current: Map<string, import("../rtc/peerlink.js").PeerLink>}} */
  const linksRef = useRef(new Map());
  /** @type {{current: Map<string, MediaStream>}} */
  const streamsRef = useRef(new Map());
  // Screen-channel consumer links + their tracks, keyed by sharer id (separate from the camera maps:
  // a sharer has BOTH a camera tile link and a screen rail link, distinguished by the ch field).
  /** @type {{current: Map<string, import("../rtc/peerlink.js").PeerLink>}} */
  const screenLinksRef = useRef(new Map());
  /** @type {{current: Map<string, MediaStream>}} */
  const screenStreamsRef = useRef(new Map());
  // Current preview pool + live selection (refs so the async ontrack + the roster handler agree).
  /** @type {{current: string[]}} */
  const screenPoolRef = useRef([]);
  const screenLiveRef = useRef("");
  /** @type {{current: Map<string, any>}} */
  const entriesRef = useRef(new Map());
  // Bind ordering: the host can change pickers quickly, so multiple PUTs are in flight and (pre-live,
  // with no roster to reconcile) an OLDER response landing last would overwrite a newer selection. A
  // global monotonic counter stamps each bind; we track the latest stamp per PASS and per SLOT, and
  // ignore a response that a newer bind superseded — for the same pass OR for the same slot (a
  // cross-pass displacement, codex).
  /** @type {{current: {g:number, pass:Object<string,number>, slot:Object<string,number>}}} */
  const bindSeqRef = useRef({ g: 0, pass: {}, slot: {} });

  useEffect(() => {
    fetchCeiling(); // populate the quality-ceiling control if a session is already live

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
      const room = roomRef.current; // the CURRENT room (re-pointed on every reconnect, D-40)
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
      // A departing guest also drops out of the screenshare rail; the authoritative screen-roster
      // re-broadcast clears the pool too, but tear the link down here so a dead pc isn't left behind.
      dropScreenLink(id);
    }

    function upsert(entry) {
      if (!entry || !isGuestRole(entry.role)) return; // grid renders guests/co-hosts only
      entriesRef.current.set(entry.id, entry);
      ensureLink(entry.id);
      applyLocks(entry.id); // a roster / peer-joined update may have changed locks → enforce (RF-8)
    }

    // syncScreenTiles rebuilds the rendered screenshare rail (D-21/AC-11) from the current pool + the
    // live selection + the consumed streams, in a stable id order so the rail doesn't reshuffle. The
    // sharer's display name comes from its camera roster entry (a sharer is always a roster guest).
    function syncScreenTiles() {
      const live = screenLiveRef.current;
      const ids = [...new Set([...screenPoolRef.current, ...(live ? [live] : [])])].sort();
      setScreenTiles(
        ids.map((id) => ({
          id,
          name: (entriesRef.current.get(id) || {}).name || "",
          stream: screenStreamsRef.current.get(id) || null,
          live: id === live,
        })),
      );
      setScreenLive(live);
    }

    // ensureScreenLink opens a screen-channel consumer PeerLink to a sharer (the rail thumbnail + the
    // live render both read its track). Kept across roster updates so a re-select doesn't churn links.
    function ensureScreenLink(id) {
      if (screenLinksRef.current.has(id)) return;
      const room = roomRef.current; // the CURRENT room (re-pointed on every reconnect, D-40)
      const link = new PeerLink(room, id, room.iceServers, "screen");
      screenLinksRef.current.set(id, link);
      link.pc.ontrack = (e) => {
        screenStreamsRef.current.set(id, e.streams[0]);
        syncScreenTiles();
      };
      link.pc.oniceconnectionstatechange = () => {
        if (link.pc.iceConnectionState === "failed") link.restartIce();
      };
      link.offer();
    }

    function dropScreenLink(id) {
      const link = screenLinksRef.current.get(id);
      if (link) link.close();
      screenLinksRef.current.delete(id);
      screenStreamsRef.current.delete(id);
    }

    // setup wires this room's signaling handlers onto a (re)connected socket. ReconnectingSession
    // calls it on every (re)connect with a fresh Room (D-40), so roomRef + the PeerLinks always
    // speak to the CURRENT socket. The host's live room persists server-side across the drop, so a
    // reconnect resumes the same session and the roster rebuilds the grid. Co-hosts have their OWN
    // sockets — they keep moderating in the gap — and can never assume host: the room is keyed to
    // the host id server-side, so there is no host-handoff path.
    function setup(room) {
      roomRef.current = room;

      // The host-only screenshare roster (D-21): a FULL snapshot of the preview pool + the live sharer.
      // Open a screen link to every sharer (pool ∪ live), drop links for sharers that left, and rebuild
      // the rail. An omitted previews/live means "empty"/"none" (full-state snapshot, not a delta).
      room.on("screen-roster", (f) => {
      const pool = f.previews || [];
      const live = f.live || "";
      screenPoolRef.current = pool;
      screenLiveRef.current = live;
      const want = new Set([...pool, ...(live ? [live] : [])]);
      for (const id of want) ensureScreenLink(id);
      for (const id of [...screenLinksRef.current.keys()]) {
        if (!want.has(id)) dropScreenLink(id);
      }
      syncScreenTiles();
    });

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
      fetchCeiling(); // the session just went live — its ceiling control is now available
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
      // Route by channel (D-21): a screen-channel signal goes to the sharer's screen rail link, a
      // camera signal to its grid-tile link — the two links to the same sharer never cross.
      if (f.ch === "screen") {
        const sl = screenLinksRef.current.get(f.from);
        if (sl) sl.onSignal(f);
        return;
      }
      const link = linksRef.current.get(f.from);
      if (link) link.onSignal(f);
    });
    room.onIce((servers) => {
      for (const link of [...linksRef.current.values(), ...screenLinksRef.current.values()]) {
        try {
          link.pc.setConfiguration({ iceServers: servers });
        } catch (_) {
          /* ignore */
        }
      }
    });
    } // end setup(room)

    // teardown closes the per-room PeerLinks and clears the rendered grid so the next (re)connect
    // rebuilds it from the fresh roster. ReconnectingSession runs it before every reconnect and on
    // close/unmount, so no dead pc or stale tile survives a drop.
    function teardown() {
      for (const link of linksRef.current.values()) link.close();
      for (const link of screenLinksRef.current.values()) link.close();
      linksRef.current.clear();
      streamsRef.current.clear();
      entriesRef.current.clear();
      screenLinksRef.current.clear();
      screenStreamsRef.current.clear();
      screenPoolRef.current = [];
      screenLiveRef.current = "";
      setTiles([]);
      setScreenTiles([]);
      setScreenLive("");
    }

    // A transient drop AUTO-RECONNECTS (D-40/AC-4): show the reconnecting banner, not an error, and
    // resume the same live session on recovery. Only a TERMINAL end stops for good — a session-ended
    // teardown (the host ended it, or an admin force-ended it via D-27) shows the "ended" screen, and
    // exhausted reconnects ("unreachable", the RF-22 cap) show the error screen. The host is never
    // kicked/expired/revoked, so those reasons never reach its own connection.
    const session = new ReconnectingSession({
      query: "", // the host session cookie authenticates the WS
      setup,
      teardown,
      onState: (s) =>
        setState((prev) =>
          prev === "ended" || prev === "error" || prev === "displaced" ? prev : s === "live" ? "live" : "reconnecting",
        ),
      // displaced (a second greenroom tab took over, EN-16) stops THIS tab so the two don't
      // reconnect-war; unreachable (RF-22 cap) shows the error screen; any other terminal end
      // (session-ended) shows the "ended" screen.
      onTerminal: (reason) => setState(reason === "displaced" ? "displaced" : reason === "unreachable" ? "error" : "ended"),
    });
    return () => session.close();
  }, []);

  // Seed picker overrides from the host's PERSISTED bindings on load (codex): a pre-live bind is
  // DB-only and isn't in the live-occupancy roster, so without this it would vanish from the picker
  // on a refresh / new tab. In-session overrides take precedence; once live, the matching roster
  // values (and the session-live signal) reconcile these away.
  useEffect(() => {
    let active = true;
    fetch("/api/passes/slot-bindings")
      .then((r) => (r.ok ? r.json() : {}))
      .then((m) => {
        if (active && m && typeof m === "object") {
          setBoundOverrides((prev) => ({ ...m, ...prev }));
        }
      })
      .catch(() => {
        /* best-effort: the picker still works from live roster updates */
      });
    return () => {
      active = false;
    };
  }, []);

  // bindSlot is the host-only People control (AC-6/D-20): (re)assign a guest to a cam slot (or
  // "" to unassign) over the REST endpoint. The server persists passes.slot_id AND live-re-routes
  // /s/{slot} with no OBS edit, then re-broadcasts the roster — so entry.boundSlot (and the
  // picker) reflect the new assignment. Same-origin fetch carries the host cookie; CSRF is the
  // SameSite=Lax cookie (a cross-site request can't send it) + connect-src 'self'.
  function bindSlot(passId, slot) {
    const seq = ++bindSeqRef.current.g; // global order across all binds
    bindSeqRef.current.pass[passId] = seq;
    if (slot) bindSeqRef.current.slot[slot] = seq;
    // Stale if a newer bind superseded this one — for the SAME pass, or one that CLAIMED the same
    // slot (cross-pass displacement). An unassign (slot:"") only checks the pass.
    const isStale = () =>
      bindSeqRef.current.pass[passId] !== seq || (!!slot && bindSeqRef.current.slot[slot] !== seq);
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

  // setName is the host-only nameplate override (AC-7/D-16): PUT /api/passes/{id}/name with the new
  // sticky display name (or "" to clear). The server caps it server-side (EN-15), persists
  // passes.name, and — if this stream is live — refreshes the OBS nameplate at the same occupant +
  // epoch (no media re-link). The authoritative name rides the re-broadcast roster, so the gr-name
  // pill updates without local override bookkeeping. Same-origin fetch carries the host cookie.
  function setName(passId, name) {
    fetch(`/api/passes/${encodeURIComponent(passId)}/name`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    })
      .then(async (r) => {
        if (r.ok) {
          setBindError("");
          return;
        }
        let msg = "Couldn't update the nameplate.";
        try {
          const body = await r.json();
          if (body && body.error) msg = body.error;
        } catch (_) {
          /* non-JSON body: keep the generic message */
        }
        setBindError(msg);
      })
      .catch(() => setBindError("Couldn't reach the server to update the nameplate."));
  }

  // setCanScreen grants/revokes a guest's screenshare eligibility (EN-23/AC-9): PATCH
  // /api/passes/{id}. The server persists can_screen and re-projects the room (a revoke runs
  // force-no-share), so the authoritative roster updates the controlled checkbox — no local override.
  function setCanScreen(passId, canScreen) {
    fetch(`/api/passes/${encodeURIComponent(passId)}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ canScreen }),
    })
      .then(async (r) => {
        if (r.ok) {
          setBindError("");
          return;
        }
        let msg = "Couldn't update screenshare eligibility.";
        try {
          const body = await r.json();
          if (body && body.error) msg = body.error;
        } catch (_) {
          /* non-JSON body: keep the generic message */
        }
        setBindError(msg);
        rollbackPicker(); // re-render so the controlled checkbox reverts to the authoritative value
      })
      .catch(() => {
        // A network failure leaves the DB unchanged + no roster update, so the controlled checkbox
        // would otherwise stay on the host's click — re-render so it reverts to the authoritative value.
        setBindError("Couldn't reach the server to update screenshare eligibility.");
        rollbackPicker();
      });
  }

  // selectScreen is the host-only select-live control (D-21/D-11 exception): promote a backstage
  // sharer to the live "screen" slot, or "" to take the current share off air (no auto-advance). The
  // server enforces host-only authority + that the target is in the pool (EN-7); a co-host's click
  // would be a server no-op. The authoritative {t:screen-roster} re-broadcast updates the rail.
  function selectScreen(peerId) {
    if (state !== "live") return; // the WS throws on send before it's live / once dropped
    roomRef.current?.send({ t: "screen-select", peerId });
  }

  // rollbackPicker forces a grid re-render so a rejected pick reverts to the authoritative
  // entry.boundSlot. setBindError alone is a no-op when the SAME message is already shown (e.g.
  // every unprovisioned slot returns the same 404), and then Preact wouldn't reconcile the
  // controlled <select> back — leaving the invalid choice on screen (codex). A fresh tiles array
  // ref guarantees the render.
  function rollbackPicker() {
    setTiles((ts) => [...ts]);
  }

  // fetchCeiling loads the active session's stream id + current ceiling (D-19/AC-8) for the control;
  // a 404 (no live session) hides it. Called on mount and on session-live.
  function fetchCeiling() {
    fetch("/api/session/ceiling")
      .then((r) => (r.ok ? r.json() : null))
      .then((c) => setCeiling(c && c.streamId ? c : null))
      .catch(() => {
        /* transient: the control just stays as-is */
      });
  }

  // Poll the verified-live status (D-29/AC-8) while a session is live (the ceiling carries the live
  // stream id). Re-polls every 30s — matching the server-side cache TTL — and clears on
  // unmount/stream change. Best-effort: a failed fetch just leaves the badge as-is.
  useEffect(() => {
    const streamId = ceiling && ceiling.streamId;
    if (!streamId) {
      setVerifiedLive(null);
      return undefined;
    }
    let alive = true;
    const poll = () => {
      fetch(`/api/streams/${encodeURIComponent(streamId)}/livecheck`)
        .then((r) => (r.ok ? r.json() : null))
        .then((v) => {
          if (alive) setVerifiedLive(v && v.linked ? v : null);
        })
        .catch(() => {});
    };
    poll();
    const t = setInterval(poll, 30000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [ceiling && ceiling.streamId]);

  // applyCeiling PUTs a ceiling adjustment; the server clamps + persists + re-broadcasts {t:ceiling}
  // to live publishers, and echoes the clamped values so the control reflects the server's clamp.
  function applyCeiling(streamId, maxRes, maxFps, maxBitrateKbps) {
    fetch(`/api/streams/${encodeURIComponent(streamId)}/ceiling`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ maxRes, maxFps, maxBitrateKbps }),
    })
      .then((r) => (r.ok ? r.json() : null))
      .then((c) => {
        if (c) setCeiling({ streamId, maxRes: c.maxRes, maxFps: c.maxFps, maxBitrateKbps: c.maxBitrateKbps });
      })
      .catch(() => {
        /* transient network failure: the persisted ceiling is unchanged */
      });
  }

  return (
    <div class="greenroom" data-state={state}>
      {/* Connection banners (M5.5/AC-4): a transient drop AUTO-RECONNECTS, showing a recoverable
          "reconnecting" notice while the live session keeps running server-side (D-40); a TERMINAL
          session-ended teardown — the host ended the session, or an admin force-ended it (D-27) —
          shows the "ended" screen; only exhausted reconnects fall to the "error" screen. */}
      {state === "ended" ? (
        <div class="gr-ended" role="alert">
          <h2 class="gr-ended-title">This session has ended</h2>
          <p class="gr-ended-body">
            Your live session was ended and everyone has been disconnected. This happens when you end
            the session yourself, or when an administrator ends it.
          </p>
        </div>
      ) : state === "displaced" ? (
        <div class="gr-error" role="alert">
          <p class="gr-error-body">
            The greenroom is now open in another tab or window. This tab has been disconnected to
            avoid two greenrooms fighting over the connection — close it and use the other one.
          </p>
        </div>
      ) : state === "reconnecting" ? (
        <div class="gr-reconnecting" role="status">
          <p class="gr-reconnecting-body">
            Reconnecting to the greenroom… your live session is still running, so guests stay on the
            air — this will recover on its own.
          </p>
        </div>
      ) : state === "error" ? (
        <div class="gr-error" role="alert">
          <p class="gr-error-body">The greenroom couldn’t reconnect after several tries. Reload the page to try again.</p>
        </div>
      ) : null}
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
        {ceiling ? <CeilingControl ceiling={ceiling} onApply={applyCeiling} /> : null}
        {/* Verified-live signal (D-29/AC-8): folds the linked channel's live status into the D-24
            broadcast surface, alongside the OBS-reflected on-air. Shown only when a channel is linked. */}
        {verifiedLive ? (
          <span
            class="gr-verified"
            data-verified-status={verifiedLive.status}
            data-verified-platform={verifiedLive.platform}
          >
            {verifiedLive.status === "live"
              ? `● Live (verified on ${verifiedLive.platform})`
              : verifiedLive.status === "offline"
                ? `Not live on ${verifiedLive.platform}`
                : `${verifiedLive.platform}: live status unavailable`}
            {verifiedLive.watch_url ? (
              <a class="gr-verified-watch" href={verifiedLive.watch_url} target="_blank" rel="noopener noreferrer">
                {" "}
                watch ↗
              </a>
            ) : null}
          </span>
        ) : null}
        {bindError ? (
          <p class="gr-binderr" role="alert">
            {bindError}
          </p>
        ) : null}
      </div>
      {/* Screenshare preview-switcher rail (D-21/AC-11): every active sharer as a thumbnail; the host
          picks which one is live (the live render is the badged tile, shown to everyone backstage +
          on /s/screen). Host-only — guests never see the rail (the screen-roster is host-only). */}
      {screenTiles.length > 0 ? (
        <section class="gr-screen" data-live={screenLive ? "1" : "0"} data-count={screenTiles.length}>
          <div class="gr-screen-head">
            <span class="gr-screen-label">Screen shares</span>
            {screenLive ? (
              <button type="button" class="gr-screen-off" disabled={state !== "live"} onClick={() => selectScreen("")}>
                Take screen off air
              </button>
            ) : null}
          </div>
          <div class="gr-screen-rail">
            {screenTiles.map((t) => (
              <ScreenTile key={t.id} tile={t} live={state === "live"} onSelect={() => selectScreen(t.id)} />
            ))}
          </div>
        </section>
      ) : null}
      <div class="greenroom-grid" data-state={state} data-count={tiles.length}>
        {tiles.length === 0 ? (
          // Once ended/dropped/reconnecting, the banner above already explains the empty grid — the
          // "waiting for guests" hint would contradict it, so suppress it in those states (the grid
          // rebuilds from the fresh roster once a reconnect recovers).
          state === "ended" || state === "error" || state === "reconnecting" || state === "displaced" ? null : (
            <p class="gr-empty" data-state={state}>
              Waiting for guests to join…
            </p>
          )
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
            onSetName={(name) => setName(t.id, name)}
            onSetCanScreen={(can) => setCanScreen(t.id, can)}
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
