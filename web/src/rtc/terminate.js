/**
 * Terminal {t:terminate} reasons (EN-9), shared by the guest session (ReconnectingSession)
 * and the OBS source page. Kept in its own tiny module so the OBS entry can import just this
 * — never the app-only ReconnectingSession — keeping the OBS bundle minimal (EN-13).
 */

/**
 * TERMINAL_REASONS maps each TERMINAL terminate reason to its user-facing error-screen copy.
 * The five EN-9 reasons arrive as a {t:terminate,reason} frame; "unreachable" is the
 * client-derived terminal when reconnection is exhausted (RF-22 — e.g. a pass revoked while
 * the socket was down, which fails the upgrade with no frame). A terminal reason routes to
 * the matching screen and STOPS; any other close (a bare drop, or the TRANSIENT reason
 * "reconnect") is recoverable and retries.
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
 * isTerminal reports whether a {t:terminate} reason is terminal (route to an error screen)
 * rather than transient (reconnect). An unknown/absent reason is treated as transient
 * (RF-22: default to recoverable unless the server explicitly says otherwise).
 * @param {string} [reason]
 * @returns {boolean}
 */
export function isTerminal(reason) {
  return !!reason && Object.prototype.hasOwnProperty.call(TERMINAL_REASONS, reason);
}
