import { Room } from "./room.js";

/**
 * Reconnect backoff bounds: a dropped signaling socket retries fast at first, then backs off to a
 * ceiling so a server restart or a long outage doesn't hammer the server (matches the OBS source
 * page's policy). The first retry waits RECONNECT_MIN_MS, so the reconnecting state is always
 * observable for at least that long before recovery.
 */
const RECONNECT_MIN_MS = 500;
const RECONNECT_MAX_MS = 10_000;

// After this many CONSECUTIVE reconnect attempts that never reach a live socket, give up and route
// to the terminal "unreachable" screen (RF-22). A revoked/expired/rotated pass is rejected at the
// /ws upgrade with a plain HTTP 403 — the browser exposes that only as a close with NO terminate
// frame — so without a cap the guest would sit on the reconnecting overlay forever. The cap (with
// backoff) still tolerates a server restart of up to ~25s before giving up; a successful open resets.
const MAX_FAILED_OPENS = 6;

/**
 * TERMINAL_REASONS maps each TERMINAL terminate reason to its guest-facing error-screen copy. The
 * five EN-9 reasons arrive as a {t:terminate,reason} frame; "unreachable" is the client-derived
 * terminal when reconnection is exhausted (RF-22 — e.g. a pass revoked while the socket was down,
 * which fails the upgrade with no frame). A terminal reason routes to the matching screen and STOPS;
 * any other close (a bare drop, or the TRANSIENT reason "reconnect") is recoverable and retries.
 * @type {Record<string,{title:string, body:string}>}
 */
export const TERMINAL_REASONS = {
  kicked: { title: "You were removed", body: "The host removed you from the greenroom." },
  expired: { title: "Your pass has expired", body: "This guest pass is past its window. Ask the host for a new link." },
  revoked: { title: "Your pass was revoked", body: "This guest pass is no longer valid. Ask the host for a new link." },
  "session-ended": { title: "The stream has ended", body: "The host ended this session." },
  "token-rotated": { title: "This link was replaced", body: "Ask the host for a fresh link." },
  unreachable: { title: "Couldn't reconnect", body: "We couldn't reconnect you to the greenroom. The session may have ended, or your pass is no longer valid — ask the host for a new link." },
};

/**
 * isTerminal reports whether a {t:terminate} reason is terminal (route to an error screen) rather
 * than transient (reconnect). An unknown/absent reason is treated as transient (RF-22: default to
 * recoverable unless the server explicitly says otherwise).
 * @param {string} [reason]
 * @returns {boolean}
 */
export function isTerminal(reason) {
  return !!reason && Object.prototype.hasOwnProperty.call(TERMINAL_REASONS, reason);
}

/**
 * ReconnectingSession owns a self-healing signaling connection (AC-13). It (re)builds a Room, runs
 * setup(room) on each connection so the caller can wire its handlers + media, reports connection
 * state via onState ("live" once the socket is up, "reconnecting" while down), and routes a
 * TERMINAL {t:terminate,reason} to onTerminal(reason) — stopping retries. A transient drop runs
 * teardown() (so dead peer connections + stale reflections are dropped) and reconnects with capped
 * exponential backoff. The caller owns whatever media it builds in setup(); teardown() runs before
 * every reconnect and on close().
 */
export class ReconnectingSession {
  /**
   * @param {{
   *   query: string,
   *   setup: (room: Room) => void,
   *   teardown?: () => void,
   *   onState?: (state: "reconnecting"|"live") => void,
   *   onTerminal?: (reason: string) => void,
   * }} opts
   */
  constructor({ query, setup, teardown, onState, onTerminal }) {
    this._query = query;
    this._setup = setup;
    this._teardown = teardown || (() => {});
    this._onState = onState || (() => {});
    this._onTerminal = onTerminal || (() => {});
    this._backoff = RECONNECT_MIN_MS;
    /** @type {ReturnType<typeof setTimeout>|undefined} */
    this._timer = undefined;
    this._stopped = false;
    /** @type {Room|null} */
    this._room = null;
    /** @type {string|null} the last {t:terminate} reason seen before a close */
    this._reason = null;
    // Consecutive reconnect attempts that never reached a live socket; reset on a clean open. Caps
    // retries so a permanently-rejected pass (HTTP 403 at upgrade, no frame) routes terminal (RF-22).
    this._failedOpens = 0;
    this._connect();
  }

  /** The current live Room (or the most recent one); null before the first connect. */
  get room() {
    return this._room;
  }

  /** send a frame over the current connection (a no-op if the socket isn't up). */
  send(frame) {
    if (this._room) this._room.send(frame);
  }

  _connect() {
    if (this._stopped) return;
    this._reason = null;
    let opened = false; // did THIS attempt reach a live socket? (drives the failed-open cap)
    const room = new Room(this._query);
    this._room = room;
    // The server sends {t:terminate,reason} BEFORE closing, so the handler captures the reason
    // for the onClose router below (a frame is dispatched before ws.onclose fires).
    room.on("terminate", (f) => {
      this._reason = f.reason;
    });
    this._setup(room);
    room.ready
      .then(() => {
        opened = true;
        this._backoff = RECONNECT_MIN_MS; // a clean connection resets the backoff
        this._failedOpens = 0; // …and the failed-open cap
        this._onState("live");
      })
      .catch(() => {
        /* onClose drives the retry; nothing to do on a failed open */
      });
    room.onClose(() => {
      if (this._stopped) return;
      this._teardown();
      if (isTerminal(this._reason)) {
        this._onTerminal(/** @type {string} */ (this._reason)); // terminal frame — no reconnect
        return;
      }
      // A socket that never opened is a failed handshake (e.g. a 403 for a pass revoked/expired while
      // down — no terminate frame). Cap consecutive failures so the guest doesn't retry forever (RF-22).
      if (!opened && ++this._failedOpens >= MAX_FAILED_OPENS) {
        this._onTerminal("unreachable");
        return;
      }
      this._onState("reconnecting");
      clearTimeout(this._timer);
      this._timer = setTimeout(() => this._connect(), this._backoff);
      this._backoff = Math.min(this._backoff * 2, RECONNECT_MAX_MS);
    });
  }

  /** Stop retrying and tear down the live connection + media. */
  close() {
    this._stopped = true;
    clearTimeout(this._timer);
    this._teardown();
    if (this._room) this._room.close();
  }
}
