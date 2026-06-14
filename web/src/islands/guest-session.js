import "./guest-session.css";
import { useState, useRef, useEffect } from "preact/hooks";

/**
 * GuestSession is the guest's in-session surface (AC-12), rendered by the device-check island in
 * its "entered" phase. It deliberately shares the device-check's SINGLE pass-token signaling Room
 * (one connection per identity, EN-16) rather than being a second WS-owning island: device-check
 * owns the connection + the camera and folds the live reflections (on-air, streaming, locks) and
 * the chat/roster down as props, while this component owns the in-session presentation + the chat
 * draft and the raise-hand / send actions.
 *
 * It shows the guest's self-view, the three-state on-air SELF pill (D-24), the backstage chat
 * (relayed-but-never-recorded, EN-20 — see the microcopy), a raise-hand control whose state is the
 * server's roster value (not optimistic), and a separate "muted/hidden by host" force-lock notice
 * (D-13). Everyone-backstage thumbnails (the guest↔guest mesh, D-10) are a later PR.
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
    <div class="dc-entered gs-root" data-entered="1" data-pub={pubState} data-locked={(lockedMods || []).join(",")}>
      <div class="gs-stage">
        <video ref={selfRef} class="gs-selfview" autoplay playsinline muted />
        <div class="gs-status">
          {pubState === "live" ? (
            <p>You're in — your camera is live in the greenroom.</p>
          ) : pubState === "disconnected" ? (
            <p>You're in, but the greenroom connection dropped. Refresh the page to rejoin.</p>
          ) : (
            <p>You're in — connecting your camera to the greenroom…</p>
          )}
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
    </div>
  );
}
