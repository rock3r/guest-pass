import { Room } from "./room.js";

/**
 * Reconnect backoff bounds: a dropped signaling socket retries fast at first, then backs off to a
 * ceiling so a server restart or a long outage doesn't hammer the server (matches the OBS source
 * page's policy). The first retry waits RECONNECT_MIN_MS, so the reconnecting state is always
 * observable for at least that long before recovery.
 */
const RECONNECT_MIN_MS = 500;
const RECONNECT_MAX_MS = 10_000;

/**
 * TERMINAL_REASONS maps each TERMINAL {t:terminate} reason (EN-9) to its guest-facing error-screen
 * copy. A terminal reason routes to the matching screen and STOPS — no reconnect. Any other close
 * (a bare socket drop, or the TRANSIENT reason "reconnect") is recoverable and retries with backoff.
 * @type {Record<string,{title:string, body:string}>}
 */
export const TERMINAL_REASONS = {
  kicked: { title: "You were removed", body: "The host removed you from the greenroom." },
  expired: { title: "Your pass has expired", body: "This guest pass is past its window. Ask the host for a new link." },
  revoked: { title: "Your pass was revoked", body: "This guest pass is no longer valid. Ask the host for a new link." },
  "session-ended": { title: "The stream has ended", body: "The host ended this session." },
  "token-rotated": { title: "This link was replaced", body: "Ask the host for a fresh link." },
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
        this._backoff = RECONNECT_MIN_MS; // a clean connection resets the backoff
        this._onState("live");
      })
      .catch(() => {
        /* onClose drives the retry; nothing to do on a failed open */
      });
    room.onClose(() => {
      if (this._stopped) return;
      this._teardown();
      if (isTerminal(this._reason)) {
        this._onTerminal(/** @type {string} */ (this._reason)); // terminal — no reconnect
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
