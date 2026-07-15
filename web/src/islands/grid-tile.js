import "./greenroom.css";
import { useRef, useEffect, useState } from "preact/hooks";

/**
 * Shared greenroom tile + moderation controls, rendered by BOTH the host greenroom grid (PR-9/10)
 * and the guest-session's backstage thumbnails (PR-11b). A tile shows one participant's P2P video
 * plus roster-driven status chrome (name, three-state on-air pill D-24, force-lock notice D-13,
 * signal, raised hand) and, for a viewer with rank, the moderation controls. Authority is the
 * server reducer (EN-7); the gates here are convenience shown by the viewer's own rank.
 */

export const RANK = { host: 2, cohost: 1, guest: 0 };
export const rankOf = (role) => RANK[role] ?? -1;

// The moderatable modalities and their control copy / inbound force-frame type (D-13).
export const MODS = ["mic", "cam", "share"];
const FORCE_LABEL = { mic: "Mute", cam: "Turn off camera", share: "Stop screen share" };
export const FORCE_FRAME = { mic: "force-mute", cam: "force-no-cam", share: "force-no-share" };

// Per-modality force-lock notice copy (M3 plan default), shown when a lock is active.
const LOCK_COPY = {
  mic: "Muted by host",
  cam: "Camera turned off by host",
  share: "Screen share stopped by host",
};

// The addressable cam slots a guest can be bound to (D-20/D-33): the host wires OBS once per
// slot, then assigns/reassigns occupants live with no OBS edit (EN-3).
const CAM_SLOTS = ["cam-1", "cam-2", "cam-3", "cam-4", "cam-5", "cam-6", "cam-7", "cam-8"];

/**
 * SlotPicker is the host-only People control that (re)binds a guest to a cam slot (D-20/AC-6).
 * Selecting a slot PUTs /api/passes/{id}/slot (via onBindSlot); the server persists the binding
 * and live-re-routes /s/{slot}, then re-broadcasts the roster so entry.boundSlot — and this
 * select — reflect the new assignment. Host-only: boundSlot is stripped from non-host rosters.
 * @param {{entry:any, live:boolean, onBindSlot:(slot:string)=>void}} props
 * @returns {import("preact").VNode}
 */
function SlotPicker({ entry, live, onBindSlot }) {
  return (
    <label class="gr-slotbind">
      <span class="gr-slotbind-label">OBS slot</span>
      <select
        class="gr-slot"
        data-guest={entry.id}
        disabled={!live}
        value={entry.boundSlot || ""}
        onChange={(e) => onBindSlot(/** @type {HTMLSelectElement} */ (e.currentTarget).value)}
      >
        <option value="">Unassigned</option>
        {CAM_SLOTS.map((s) => (
          <option key={s} value={s}>
            {s.replace("cam-", "Cam ")}
          </option>
        ))}
      </select>
    </label>
  );
}

/**
 * NameOverride is the host-only People control that sets a guest's sticky nameplate name
 * (D-16/AC-7). Submitting PUTs /api/passes/{id}/name (via onSetName); the server caps the name
 * server-side (EN-15 charset/length), persists passes.name, and — if the stream is live —
 * refreshes the OBS nameplate at the SAME occupant + epoch (no media re-link). The input is
 * UNCONTROLLED (defaultValue, keyed by the authoritative name) so a roster re-render never clobbers
 * what the host is typing; the authoritative name still shows in the gr-name pill above. maxLength
 * mirrors the server cap as a courtesy — the server is the authority.
 * @param {{entry:any, onSetName:(name:string)=>void}} props
 * @returns {import("preact").VNode}
 */
function NameOverride({ entry, onSetName }) {
  return (
    <form
      class="gr-nameedit"
      data-guest={entry.id}
      onSubmit={(e) => {
        e.preventDefault();
        const input = /** @type {HTMLInputElement} */ (
          /** @type {HTMLFormElement} */ (e.currentTarget).elements.namedItem("name")
        );
        onSetName(input.value);
      }}
    >
      <label class="gr-nameedit-label">
        <span class="gr-nameedit-text">Nameplate</span>
        <input
          key={entry.name || ""}
          class="gr-nameedit-input"
          name="name"
          type="text"
          maxLength={60}
          defaultValue={entry.name || ""}
          placeholder="Display name"
        />
      </label>
      <button type="submit" class="gr-nameedit-set">
        Set
      </button>
    </form>
  );
}

/**
 * EligibilityToggle is the host-only People control that grants/revokes a guest's screenshare
 * eligibility (can_screen, EN-23/AC-9). Toggling PATCHes /api/passes/{id} (via onSetCanScreen); the
 * server persists + re-projects the room (a revoke runs force-no-share). CONTROLLED by
 * entry.canScreen (roster-driven) — a connected guest always re-projects, so the box reflects the
 * authoritative value with no snap-back.
 * @param {{entry:any, onSetCanScreen:(can:boolean)=>void}} props
 * @returns {import("preact").VNode}
 */
function EligibilityToggle({ entry, onSetCanScreen }) {
  return (
    <label class="gr-screenelig">
      <input
        type="checkbox"
        class="gr-screenelig-input"
        data-guest={entry.id}
        checked={!!entry.canScreen}
        onChange={(e) => onSetCanScreen(/** @type {HTMLInputElement} */ (e.currentTarget).checked)}
      />
      <span>Can share screen</span>
    </label>
  );
}

/**
 * onAirLabel maps the three-state on-air to its pill copy (D-24). status-unavailable means no live
 * OBS signal — never asserted as on/off.
 * @param {string} onAir
 */
function onAirLabel(onAir) {
  if (onAir === "on-air") return "On air";
  if (onAir === "not-on-air") return "Not on air";
  return "On-air status unavailable";
}

/**
 * lockNotices renders the distinct force-lock notices for a roster entry's locks.
 * @param {Array<{kind:string}>} [locks]
 * @returns {string[]}
 */
function lockNotices(locks) {
  return (locks || []).map((l) => LOCK_COPY[l.kind]).filter(Boolean);
}

/** @param {number} signal */
function signalLabel(signal) {
  if (signal >= 4) return "Connection strong";
  if (signal >= 2) return "Connection fair";
  if (signal >= 1) return "Connection weak";
  return "Connection status unavailable";
}

/**
 * Controls renders the per-tile moderation actions a viewer of viewerRole may take on a target
 * entry (D-13/D-15). FORCE shows on an unlocked modality only to a moderator strictly ABOVE the
 * target. RELEASE shows on a locked modality whenever the viewer's rank is at or above the LOCK
 * FLOOR — independent of the force gate, so a co-host can release a lock on a guest later promoted
 * to co-host — but never on the viewer's OWN tile (a target can't self-release, D-13). Promote/
 * demote and hand-dismiss are host-only. Every action sends over the signaling socket, so the
 * buttons are disabled until the connection is `live` (matching the chat/raise-hand controls — a
 * send on a non-open socket throws). The reducer is the authority — these gates are convenience.
 * @param {{entry:any, viewerRole:string, live:boolean, onForce:(m:string)=>void, onRelease:(m:string)=>void, onRole:(role:string)=>void, onDismissHand:()=>void}} props
 * @returns {import("preact").VNode}
 */
function Controls({ entry, viewerRole, live, onForce, onRelease, onRole, onDismissHand }) {
  const vr = rankOf(viewerRole);
  const canModerate = vr > rankOf(entry.role); // strictly above the target → may force
  const locks = {};
  for (const l of entry.locks || []) locks[l.kind] = l;
  return (
    <div class="gr-controls">
      {MODS.map((m) => {
        const lock = locks[m];
        if (lock) {
          // Release is floor-gated and self-forbidden, NOT canModerate-gated (D-13).
          const canRelease = !entry.self && vr >= rankOf(lock.applierRank);
          return canRelease ? (
            <button type="button" class="gr-release" data-kind={m} disabled={!live} onClick={() => onRelease(m)}>
              Release {m}
            </button>
          ) : null;
        }
        return canModerate ? (
          <button type="button" class="gr-force" data-kind={m} disabled={!live} onClick={() => onForce(m)}>
            {FORCE_LABEL[m]}
          </button>
        ) : null;
      })}
      {viewerRole === "host" && entry.handRaised ? (
        <button type="button" class="gr-dismiss-hand" disabled={!live} onClick={onDismissHand}>
          Dismiss hand
        </button>
      ) : null}
      {viewerRole === "host" ? (
        <button
          type="button"
          class="gr-role"
          data-to={entry.role === "guest" ? "cohost" : "guest"}
          disabled={!live}
          onClick={() => onRole(entry.role === "guest" ? "cohost" : "guest")}
        >
          {entry.role === "guest" ? "Promote to co-host" : "Demote to guest"}
        </button>
      ) : null}
    </div>
  );
}

/**
 * PersonControls renders the live controls for one selected participant. The host greenroom uses it
 * in the persistent People rail; the reused guest-session tiles retain their in-tile controls. This
 * keeps the video grid observational while preserving the established server-authorized operations.
 * @param {{entry:any, viewerRole:string, live:boolean, onForce:(m:string)=>void, onRelease:(m:string)=>void, onRole:(role:string)=>void, onDismissHand:()=>void, onBindSlot?:(slot:string)=>void, onSetName?:(name:string)=>void, onSetCanScreen?:(can:boolean)=>void}} props
 * @returns {import("preact").VNode}
 */
export function PersonControls({ entry, viewerRole, live, onForce, onRelease, onRole, onDismissHand, onBindSlot, onSetName, onSetCanScreen }) {
  const locks = lockNotices(entry.locks);
  return (
    <section class="gr-person-detail" data-guest={entry.id} aria-label={`Controls for ${entry.name || entry.id}`}>
      <div class="gr-person-detail-head">
        <div>
          <span class="gr-person-detail-name">{entry.name || entry.id}</span>
          <span class="gr-person-detail-meta">{entry.role} · {onAirLabel(entry.onAir)}</span>
        </div>
        {entry.handRaised ? <span class="gr-hand" data-hand="1">Hand raised</span> : null}
      </div>
      <p class="gr-person-health" data-signal={entry.signal || 0}>
        {signalLabel(entry.signal || 0)}{entry.rttMs ? ` · ${entry.rttMs} ms` : ""}
      </p>
      {locks.length ? <p class="gr-person-locks">Host controls active: {locks.join(" · ")}</p> : null}
      {viewerRole === "host" && onBindSlot ? <SlotPicker entry={entry} live={live} onBindSlot={onBindSlot} /> : null}
      {viewerRole === "host" && onSetName ? <NameOverride entry={entry} onSetName={onSetName} /> : null}
      {viewerRole === "host" && onSetCanScreen && entry.role === "guest" ? (
        <EligibilityToggle entry={entry} onSetCanScreen={onSetCanScreen} />
      ) : null}
      <Controls
        entry={entry}
        viewerRole={viewerRole}
        live={live}
        onForce={onForce}
        onRelease={onRelease}
        onRole={onRole}
        onDismissHand={onDismissHand}
      />
    </section>
  );
}

/**
 * Tile renders one participant's P2P video plus its roster-driven status chrome and, outside the
 * host control room, optional moderation controls. The stream attaches via an effect so a re-render
 * (e.g. an on-air change) never reloads the <video>. The Reconnect control forces an ICE restart
 * for a stuck tile.
 * @param {{entry:any, stream:MediaStream|null, viewerRole:string, live?:boolean, showControls?:boolean, onReconnect:()=>void, onForce:(m:string)=>void, onRelease:(m:string)=>void, onRole:(role:string)=>void, onDismissHand:()=>void, onBindSlot?:(slot:string)=>void, onSetName?:(name:string)=>void, onSetCanScreen?:(can:boolean)=>void}} props
 * @returns {import("preact").VNode}
 */
export function Tile({ entry, stream, viewerRole, live = true, showControls = true, onReconnect, onForce, onRelease, onRole, onDismissHand, onBindSlot, onSetName, onSetCanScreen }) {
  /** @type {{current: HTMLVideoElement|null}} */
  const videoRef = useRef(null);
  // Whether the consumed stream carries video. An audio-only guest (PD-12 mic-only join) connects
  // with a mic track but no camera track, so the tile shows a connected-with-audio placeholder
  // instead of a broken black box. Tracked as state (not a bare render-time read) so a track added or
  // removed on the same stream object updates the tile. A force-no-cam lock KEEPS the (disabled) video
  // track, so it stays "has video" here and is communicated by the lock notice — not this placeholder.
  const [hasVideo, setHasVideo] = useState(false);
  const [hasAudio, setHasAudio] = useState(false);
  useEffect(() => {
    if (videoRef.current) videoRef.current.srcObject = stream || null;
    const recompute = () => {
      setHasVideo(!!stream && stream.getVideoTracks().length > 0);
      setHasAudio(!!stream && stream.getAudioTracks().length > 0);
    };
    recompute();
    if (!stream || !stream.addEventListener) return undefined;
    stream.addEventListener("addtrack", recompute);
    stream.addEventListener("removetrack", recompute);
    return () => {
      stream.removeEventListener("addtrack", recompute);
      stream.removeEventListener("removetrack", recompute);
    };
  }, [stream]);
  const audioOnly = hasAudio && !hasVideo;
  const notices = lockNotices(entry.locks);
  return (
    <div class="gr-tile" data-guest={entry.id} data-role={entry.role} data-novideo={audioOnly ? "1" : undefined}>
      <div class="gr-video-wrap">
        <video ref={videoRef} class="gr-video" data-guest={entry.id} autoplay playsinline muted />
        {/* Audio-only guest (PD-12): a connected-with-audio placeholder over the black video, so a
            mic-only guest reads as connected-with-audio rather than a broken tile. */}
        {audioOnly ? (
          <div class="gr-novideo" data-novideo="1">
            <span class="gr-novideo-icon" aria-hidden="true">🎙️</span>
            <span class="gr-novideo-text">Camera off · audio only</span>
          </div>
        ) : null}
      </div>
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
      {entry.degraded ? (
        <p class="gr-degraded" data-degraded={`${entry.degraded.dir}:${entry.degraded.reason}`}>
          {entry.degraded.dir === "recovering" ? "Recovering" : "Degrading"} ({entry.degraded.reason})
        </p>
      ) : null}
      {entry.handRaised ? (
        <span class="gr-hand" data-hand="1">
          ✋ Hand raised
        </span>
      ) : null}
      {showControls ? (
        <PersonControls
          entry={entry}
          viewerRole={viewerRole}
          live={live}
          onForce={onForce}
          onRelease={onRelease}
          onRole={onRole}
          onDismissHand={onDismissHand}
          onBindSlot={onBindSlot}
          onSetName={onSetName}
          onSetCanScreen={onSetCanScreen}
        />
      ) : null}
    </div>
  );
}
