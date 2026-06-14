import "./greenroom.css";
import { useRef, useEffect } from "preact/hooks";

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
 * Tile renders one participant's P2P video plus its roster-driven status chrome and moderation
 * controls. The stream attaches via an effect so a re-render (e.g. an on-air change) never reloads
 * the <video>. The Reconnect control forces an ICE restart for a stuck tile.
 * @param {{entry:any, stream:MediaStream|null, viewerRole:string, live?:boolean, onReconnect:()=>void, onForce:(m:string)=>void, onRelease:(m:string)=>void, onRole:(role:string)=>void, onDismissHand:()=>void}} props
 * @returns {import("preact").VNode}
 */
export function Tile({ entry, stream, viewerRole, live = true, onReconnect, onForce, onRelease, onRole, onDismissHand }) {
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
      <Controls
        entry={entry}
        viewerRole={viewerRole}
        live={live}
        onForce={onForce}
        onRelease={onRelease}
        onRole={onRole}
        onDismissHand={onDismissHand}
      />
    </div>
  );
}
