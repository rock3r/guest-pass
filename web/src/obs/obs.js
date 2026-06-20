import "./obs.css";
import { Room } from "../rtc/room.js";
import { PeerLink } from "../rtc/peerlink.js";
import { isTerminal, TERMINAL_REASONS } from "../rtc/terminate.js";

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
  // The signaling channel for this source (D-21): the screenshare slot ("screen") consumes the
  // occupant's SCREEN publisher (ch="screen"), every cam/host slot its camera (ch=""). It is bound to
  // the AUTHENTICATED slot the server reports on the {t:slot-rebind} frame (resolved from the source
  // TOKEN), NOT the cosmetic /s/{slot} URL label — so a mismatched path can't pick the wrong channel.
  let channel = "";
  // EN-15: the token authenticates the WS the page opens, it is not page state — read it
  // from the URL and keep it out of the DOM entirely.
  const params = new URLSearchParams(location.search);
  const token = params.get("token") || "";
  // The nameplate (D-16) is a per-source show/hide URL param (no DB column), HIDDEN by default:
  // the name renders only when the source URL carries ?name (any value but 0/false). The display
  // name is NOT a secret (unlike the token) — it is the guest's chosen name, meant to be shown —
  // so it renders, but as ESCAPED textContent only (never innerHTML), the EN-15 injection guard.
  const nameVal = params.get("name");
  const showName = nameVal !== null && nameVal !== "0" && nameVal !== "false";
  /** @type {HTMLElement|null} the nameplate overlay; styled by the host via OBS Custom CSS */
  const nameplate = document.getElementById("obs-nameplate");
  // Per-source program-resolution override (D-19/AC-8): an optional ?res URL param caps the bound
  // occupant's program encoder for THIS source tighter than the stream ceiling. We don't re-encode
  // (a source only consumes) — we tell the server, which relays it to the occupant so it caps the
  // sender feeding us. 0/invalid = no override.
  const resOverride = Math.max(0, parseInt(params.get("res") || "", 10) || 0);

  // State that must survive a reconnect (a fresh Room is built on each retry).
  /** @type {PeerLink|null} */
  let link = null;
  /** @type {string|null} the peer id currently bound to this slot */
  let occupant = null;
  // The bound occupant's force-suppressed modalities (mic|cam|share), from the server's
  // {t:occupant-locks} projection (RF-8 receiver-side). An OBS source gets no roster, so this is how
  // it learns a lock and detaches the matching REMOTE track from the program output — a force really
  // suppresses the occupant on air, independent of whether the occupant cooperates. Reset on rebind.
  /** @type {string[]} */
  let lockedMods = [];
  // The slot epoch we last acted on. A frame from an older epoch is a stale straggler and is
  // ignored (EN-3); the server's epoch is monotonic per slot, so a fresh connection's binding
  // frame is always >= this. Reset on disconnect so the reconnect's binding is always taken.
  let epoch = -1;
  let backoff = RECONNECT_MIN_MS;
  /** @type {ReturnType<typeof setTimeout>|undefined} */
  let reconnectTimer;
  /** @type {import("../rtc/room.js").Room|null} the live signaling room (rebuilt each reconnect) */
  let room = null;
  // Set once a TERMINAL {t:terminate} (e.g. token-rotated, D-22) ends this source for good, so
  // the reconnect loop stops — the host must re-paste the fresh slot URL into OBS. Reflected on
  // the document for the host (a visible note in OBS) and the browser test.
  let terminated = false;

  // showTerminal stops the source and surfaces a terminal reason in OBS (the page is normally
  // chromeless; a token-rotated/kicked source is a real dead-end the host should see).
  function showTerminal(reason) {
    terminated = true;
    clearLink();
    document.documentElement.dataset.obsConnected = "";
    document.documentElement.dataset.obsTerminated = reason;
    const copy = TERMINAL_REASONS[reason];
    let note = document.getElementById("obs-terminal");
    if (!note) {
      note = document.createElement("div");
      note.id = "obs-terminal";
      document.body.appendChild(note);
    }
    note.textContent = copy ? copy.title + " — " + copy.body : "This source ended.";
  }

  function clearLink() {
    if (link) link.close();
    link = null;
    occupant = null;
    lockedMods = []; // a (re)bind re-projects from the server; never carry a prior occupant's locks
    if (video) video.srcObject = null;
    renderNameplate(""); // an unbound/cleared slot shows no nameplate
  }

  // renderNameplate writes the bound occupant's display name into the nameplate as ESCAPED
  // textContent (EN-15 — NEVER innerHTML, so a hostile name can't inject markup) and only when the
  // per-source ?name show/hide gate is on (hidden by default). The server already caps charset +
  // length (EN-15); this is the injection-safe render half. An absent name or a hidden gate leaves
  // the nameplate empty and hidden so it can't composite an empty box into OBS.
  function renderNameplate(name) {
    if (!nameplate) return;
    const text = showName && typeof name === "string" ? name : "";
    nameplate.textContent = text; // escaped textContent ONLY — never innerHTML (EN-15)
    nameplate.hidden = text === "";
  }

  // applyOccupantLocks detaches/re-attaches the bound occupant's REMOTE tracks per the current lock
  // set (RF-8 receiver-side). Re-asserted both when the lock set changes and when a fresh track
  // arrives (ontrack), so a lock that landed BEFORE media — a pre-existing/seeded lock, or one
  // re-projected on reconnect — still takes effect on the program output.
  function applyOccupantLocks() {
    // Test/host seam (no secret — lock KINDS only, EN-13): expose the applied lock set so a browser
    // test can assert which modalities are suppressed on this source.
    document.documentElement.dataset.obsLocks = lockedMods.join(",");
    if (!link) return;
    for (const m of ["mic", "cam", "share"]) link.setRemoteTrackEnabled(m, !lockedMods.includes(m));
  }

  function connect() {
    if (terminated) return; // a terminal terminate (e.g. token-rotated) stops the loop for good
    room = new Room("src=" + encodeURIComponent(token));
    // Capture a {t:terminate} reason BEFORE the socket closes, so onClose can tell a terminal
    // end (stop) from a transient drop (reconnect). Per-connection: a fresh Room each retry.
    let lastReason = null;
    room.on("terminate", (f) => {
      lastReason = f && f.reason;
    });

    // bind the slot to occupantPeerId by opening a recvonly link and rendering its track. slotLabel is
    // the server-authenticated slot from the rebind frame (D-21): it sets the consume channel.
    function bind(occupantPeerId, ep, name, slotLabel) {
      channel = slotLabel === "screen" ? "screen" : ""; // bind the channel to the authenticated slot
      if (typeof ep === "number" && ep < epoch) return; // stale epoch (EN-3)
      // A same-occupant, same-epoch slot-rebind is a NAME-ONLY refresh (the nameplate override,
      // D-16): the server re-sends slot-rebind after a host name change with the SAME occupant +
      // epoch, so the overlay updates WITHOUT re-linking media — refresh the nameplate and leave the
      // live video link untouched (no flicker on the program output).
      if (link && occupantPeerId === occupant && (typeof ep !== "number" || ep === epoch)) {
        renderNameplate(name);
        return;
      }
      // A re-link to the SAME occupant at a NEW epoch is a grace reconnect (D-40): the guest dropped
      // and rejoined, so the server re-sends slot-rebind to renegotiate. Keep the last frame painted
      // (do NOT null srcObject) until the new track arrives, so the program output never blanks — no
      // placeholder AND no flash (AC-3). A switch to a DIFFERENT occupant clears the old frame as
      // before (clearLink nulls srcObject) so a stale occupant never lingers on a real reassignment.
      const resuming = !!link && occupantPeerId === occupant;
      if (typeof ep === "number") epoch = ep;
      if (resuming) {
        link.close();
        link = null;
        lockedMods = []; // re-projected from the server on (re)bind; never carry the prior lock view
        // intentionally keep video.srcObject (the frozen last frame) + the nameplate until ontrack swaps
      } else {
        clearLink();
      }
      occupant = occupantPeerId;
      renderNameplate(name);
      const l = new PeerLink(room, occupantPeerId, room.iceServers, channel);
      link = l;
      l.pc.ontrack = (e) => {
        if (video) video.srcObject = e.streams[0];
        applyOccupantLocks(); // re-assert any active suppression lock on the freshly-arrived track (RF-8)
      };
      l.pc.oniceconnectionstatechange = () => {
        // A dropped path (NAT rebind, network change) self-heals with an ICE restart rather
        // than tearing down the link — OBS keeps the last frame until the path recovers.
        if (l.pc.iceConnectionState === "failed") l.restartIce();
      };
      l.offer();
      // Per-source resolution override (?res, D-19/AC-8): on every real (re)bind tell the server our
      // override so it relays the cap to the (new) occupant, which caps the sender feeding us. ALWAYS
      // sent — res 0 when ?res is absent — so a source whose URL DROPS ?res (host reload) clears any
      // override the prior occupant/sender still held, instead of leaving a stale tighter cap. The
      // server resolves us→occupant via the slot (EN-1); we never address the occupant directly.
      relay({ t: "source-quality", res: resOverride });
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

    room.on("slot-rebind", (f) => bind(f.occupantPeerId, f.epoch, f.name, f.slot));
    room.on("slot-unbound", (f) => unbind(f.epoch));
    // RF-8 (receiver-side): the server projects the bound occupant's force-locked modality KINDS here
    // (an OBS source gets no roster). Detach the locked REMOTE track from the program output. Gated by
    // occupant + epoch — the same straggler defence as the slot frames — so a late frame for a prior
    // occupant/epoch can't mislight the air-critical surface (EN-3).
    room.on("occupant-locks", (f) => {
      if (f.occupantPeerId !== occupant) return;
      if (typeof f.epoch === "number" && f.epoch < epoch) return;
      lockedMods = f.lockKinds || [];
      applyOccupantLocks();
    });
    room.on("signal", (f) => {
      // Match this source's channel (D-21): a /s/screen source ignores camera-channel signals and
      // vice versa, so the occupant's camera and screen publishers never cross into the wrong source.
      if (link && f.from === occupant && (f.ch || "") === channel) link.onSignal(f);
    });

    // A clean connection resets the backoff so the NEXT drop retries fast again, and
    // re-asserts the last known OBS streaming state: streaming is global and the server does
    // NOT clear it on a source drop, so a streamingStarted/Stopped transition that fired while
    // this socket was reconnecting (room.send throws on a CONNECTING/CLOSED socket) would
    // otherwise leave a stale "live" banner until OBS next toggles (D-24).
    room.ready.then(() => {
      backoff = RECONNECT_MIN_MS;
      document.documentElement.dataset.obsConnected = "1"; // test/host seam: source is live
      reassertStreaming();
    }).catch(() => {
      /* the onclose handler drives the reconnect; nothing to do here */
    });

    room.onClose(() => {
      clearLink();
      document.documentElement.dataset.obsConnected = "";
      epoch = -1; // accept whatever the reconnect's binding frame reports
      // A TERMINAL terminate (token-rotated, kicked, …) ends the source — stop retrying; the
      // old slot token is dead, so reconnecting would just 401 forever (D-22 / EN-9). The host
      // re-pastes the fresh URL from the Sources tab. A transient drop reconnects as before.
      if (isTerminal(lastReason)) {
        showTerminal(lastReason);
        return;
      }
      clearTimeout(reconnectTimer);
      reconnectTimer = setTimeout(connect, backoff);
      backoff = Math.min(backoff * 2, RECONNECT_MAX_MS);
    });
  }

  // Relay OBS's on-air/broadcast reflection (D-24) over the signaling room. These
  // window.obsstudio events fire ONLY inside OBS, at the default permission level (no setup
  // needed); in a normal browser they never fire, so the occupant's pill stays
  // status-unavailable. Registered ONCE (not per reconnect) and they send over whatever room
  // is currently live. We use `active` (obsSourceActiveChanged) and NEVER `visible`
  // (obsSourceVisibleChanged also fires in OBS *preview* and would false-positive).
  const relay = (frame) => {
    if (!room) return;
    try {
      room.send(frame);
    } catch (_) {
      /* socket not open yet / already closing — a missed streaming transition is re-asserted on
         reconnect via reassertStreaming; a per-slot sourceActive degrades to unknown safely */
    }
  };
  // lastStreaming is the last OBS "we're live" state this page witnessed (null = none yet), so a
  // reconnect can re-assert it if the transition was dropped mid-reconnect.
  let lastStreaming = null;
  const reassertStreaming = () => {
    if (lastStreaming !== null) {
      relay({ t: "obs", event: lastStreaming ? "streamingStarted" : "streamingStopped" });
    }
  };
  addEventListener("obsSourceActiveChanged", (e) => {
    // Echo the current slot epoch so the server resolves slot→occupant at signal time and
    // ignores stale reports (EN-1/EN-3). Skip until a binding has set the epoch.
    if (occupant && epoch >= 0) {
      relay({ t: "obs", event: "sourceActive", active: !!(e && e.detail && e.detail.active), epoch });
    }
  });
  addEventListener("obsStreamingStarted", () => {
    lastStreaming = true;
    relay({ t: "obs", event: "streamingStarted" });
  });
  addEventListener("obsStreamingStopped", () => {
    lastStreaming = false;
    relay({ t: "obs", event: "streamingStopped" });
  });

  connect();
}

start();
