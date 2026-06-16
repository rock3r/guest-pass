import "./guest-session.css";
import { useState, useRef, useEffect } from "preact/hooks";
import { Tile } from "./grid-tile.js";

/**
 * GuestSession is the guest's in-session surface (AC-12), rendered by the device-check island in
 * its "entered" phase. It deliberately shares the device-check's SINGLE pass-token signaling Room
 * (one connection per identity, EN-16) rather than being a second WS-owning island: device-check
 * owns the connection + the camera and folds the live reflections (on-air, streaming, locks), the
 * chat/roster, and the backstage thumbnails down as props, while this component owns the in-session
 * presentation + the chat draft and the raise-hand / send actions.
 *
 * It shows the guest's self-view, the three-state on-air SELF pill (D-24), the backstage chat
 * (relayed-but-never-recorded, EN-20 — see the microcopy), a raise-hand control whose state is the
 * server's roster value (not optimistic), a separate "muted/hidden by host" force-lock notice
 * (D-13), and the everyone-backstage thumbnails of the other participants over the P2P mesh (D-10).
 * The thumbnails reuse the shared greenroom tile, so a co-host sees the moderation controls it may
 * use within rank (AC-11) — a host-only /greenroom isn't reachable with a pass; a guest's are
 * view-only.
 *
 * The wrapper preserves the device-check entered-view contract (`data-entered` / `data-pub` /
 * `data-locked`, `.dc-onair`, `.dc-live`) that the on-air and moderation browser tests assert.
 */

// Per-modality force-lock notice copy (matches the host greenroom, M3 plan default), shown as the
// guest's separate "muted/hidden by host" affordance whenever a suppression lock is active (D-13).
const LOCK_COPY = {
  mic: "Muted by host",
  cam: "Camera turned off by host",
  share: "Screen share stopped by host",
};

/**
 * onAirLabel maps the three-state on-air to its self-pill copy (D-24). status-unavailable means no
 * live OBS signal — never asserted as on/off.
 * @param {string} onAir
 */
function onAirLabel(onAir) {
  if (onAir === "on-air") return "On air";
  if (onAir === "not-on-air") return "Not on air";
  return "On-air status unavailable";
}

/**
 * GuestSession renders the guest's in-session view.
 * @param {{
 *   pubState: string, onAir: string, streaming: boolean, lockedMods: string[],
 *   selfStream: MediaStream|null, peers: any[], selfId: string,
 *   messages: Array<{from:string, text:string}>, handRaised: boolean,
 *   onSendChat: (text:string)=>void, onToggleHand: ()=>void,
 *   screenShare: string, onToggleScreen: ()=>void,
 *   selfDegraded: {dir:string,reason:string}|null,
 *   thumbnails: Array<{id:string, entry:any, stream:MediaStream|null}>, viewerRole: string,
 *   onThumbForce: (id:string, m:string)=>void, onThumbRelease: (id:string, m:string)=>void,
 *   onThumbRole: (id:string, role:string)=>void, onThumbDismissHand: (id:string)=>void,
 *   onThumbReconnect: (id:string)=>void,
 * }} props
 * @returns {import("preact").VNode}
 */
export function GuestSession({
  pubState,
  onAir,
  streaming,
  lockedMods,
  selfStream,
  peers,
  selfId,
  messages,
  handRaised,
  onSendChat,
  onToggleHand,
  screenShare,
  onToggleScreen,
  selfDegraded,
  thumbnails,
  viewerRole,
  onThumbForce,
  onThumbRelease,
  onThumbRole,
  onThumbDismissHand,
  onThumbReconnect,
}) {
  const [draft, setDraft] = useState("");
  /** @type {{current: HTMLVideoElement|null}} */
  const selfRef = useRef(null);
  // Attach the guest's own camera once the <video> is mounted; an effect (not a render-time set)
  // so a re-render (an on-air change, a new chat line) never reloads the self-view.
  useEffect(() => {
    if (selfRef.current) selfRef.current.srcObject = selfStream || null;
  }, [selfStream]);

  // nameFor resolves a chat sender's peer id to a display name from the roster; the guest's own
  // messages (relayed back by the server) read as "You".
  const nameFor = (from) => {
    if (from && from === selfId) return "You";
    const p = (peers || []).find((x) => x.id === from);
    return (p && p.name) || from;
  };

  const notices = (lockedMods || []).map((m) => LOCK_COPY[m]).filter(Boolean);

  // Screenshare eligibility (EN-23/AC-9): the host grants/revokes can_screen live; the guest sees it
  // on its OWN roster entry, gating the share affordance.
  const self = (peers || []).find((p) => p.id === selfId);
  const canShareScreen = !!(self && self.canScreen);
  // Screenshare self-state (AC-13): "" idle, "backstage" capturing but not selected for the live
  // slot, "live" the host promoted this sharer to /s/screen. Derived solely from the server-folded
  // self pointer — the sharer never asserts "live" optimistically (the host alone selects live).
  const sharing = screenShare === "backstage" || screenShare === "live";

  // The chat and raise-hand actions send over the signaling socket, which throws while it is still
  // CONNECTING (and is dead once disconnected) — so they are disabled until the room is live.
  const live = pubState === "live";

  const submit = (e) => {
    e.preventDefault();
    if (!live) return;
    const text = draft.trim();
    if (!text) return;
    onSendChat(text); // render happens when the server relays it back (proves the round-trip; EN-20)
    setDraft("");
  };

  return (
    <div
      class="dc-entered gs-root"
      data-entered="1"
      data-pub={pubState}
      data-locked={(lockedMods || []).join(",")}
      data-degraded={selfDegraded ? `${selfDegraded.dir}:${selfDegraded.reason}` : ""}
    >
      <div class="gs-stage">
        <video ref={selfRef} class="gs-selfview" autoplay playsinline muted />
        <div class="gs-status">
          {pubState === "live" ? (
            <p>You're in — your camera is live in the greenroom.</p>
          ) : pubState === "reconnecting" ? (
            <p class="gs-reconnecting">Connection dropped — reconnecting to the greenroom…</p>
          ) : (
            <p>You're in — connecting your camera to the greenroom…</p>
          )}
          {selfDegraded ? (
            <p class="gs-degraded" data-degraded={`${selfDegraded.dir}:${selfDegraded.reason}`}>
              {selfDegraded.dir === "recovering"
                ? "Recovering your video quality…"
                : "Trimming your video to protect your stream."}
            </p>
          ) : null}
          <p class="dc-onair" data-onair={onAir}>
            {onAirLabel(onAir)}
            {onAir === "status-unavailable" ? (
              <span class="dc-onair-hint"> — check the actual stream to confirm.</span>
            ) : null}
          </p>
          {streaming ? (
            <p class="dc-live" data-live="1">
              The broadcast is live.
            </p>
          ) : null}
          {notices.length > 0 ? (
            <p class="gs-lock" data-locked="1">
              {notices.join(" · ")}
            </p>
          ) : null}
          {canShareScreen ? (
            <div class="gs-screen" data-eligible="1" data-screen-state={screenShare || "idle"}>
              <button
                type="button"
                class="gs-screen-toggle"
                data-sharing={sharing ? "1" : "0"}
                /* STARTING needs a live socket (the announce); STOPPING must stay enabled even while
                   reconnecting — the capture is kept alive for recovery, and the sharer must always be
                   able to release it (the stop is best-effort over the socket + an unconditional local
                   teardown), so a reconnecting sharer is never stuck unable to stop. */
                disabled={!sharing && !live}
                onClick={onToggleScreen}
              >
                {sharing ? "Stop sharing" : "Share screen"}
              </button>
              {screenShare === "live" ? (
                <p class="gs-screen-status" data-screen-status="live">
                  Your screen is on the live output.
                </p>
              ) : screenShare === "backstage" ? (
                <p class="gs-screen-status" data-screen-status="backstage">
                  Screen ready — the host can put it live.
                </p>
              ) : (
                <p class="gs-screen-status" data-screen-status="idle">
                  Screen sharing enabled by the host.
                </p>
              )}
            </div>
          ) : null}
          <button
            type="button"
            class="gs-hand"
            data-raised={handRaised ? "1" : "0"}
            disabled={!live}
            onClick={onToggleHand}
          >
            {handRaised ? "Lower hand" : "Raise hand"}
          </button>
        </div>
      </div>

      <section class="gs-chat" aria-label="Backstage chat">
        <p class="gs-chat-note">Backstage chat is not recorded — it's off the record.</p>
        <ul class="gs-chat-log">
          {(messages || []).map((m, i) => (
            <li key={i} class="gs-chat-msg">
              <span class="gs-chat-from">{nameFor(m.from)}:</span> {m.text}
            </li>
          ))}
        </ul>
        <form class="gs-chat-form" onSubmit={submit}>
          <input
            class="gs-chat-input"
            type="text"
            value={draft}
            disabled={!live}
            placeholder="Message the backstage…"
            onInput={(e) => setDraft(/** @type {HTMLInputElement} */ (e.target).value)}
          />
          <button class="gs-chat-send" type="submit" disabled={!live}>
            Send
          </button>
        </form>
      </section>

      {(thumbnails || []).length > 0 ? (
        <section class="gs-thumbs" aria-label="Backstage participants">
          <p class="gs-thumbs-label">Everyone backstage</p>
          <div class="greenroom-grid" data-count={thumbnails.length}>
            {thumbnails.map((t) => (
              <Tile
                key={t.id}
                entry={t.entry}
                stream={t.stream}
                viewerRole={viewerRole}
                live={live}
                onReconnect={() => onThumbReconnect(t.id)}
                onForce={(m) => onThumbForce(t.id, m)}
                onRelease={(m) => onThumbRelease(t.id, m)}
                onRole={(role) => onThumbRole(t.id, role)}
                onDismissHand={() => onThumbDismissHand(t.id)}
              />
            ))}
          </div>
        </section>
      ) : null}
    </div>
  );
}
