/**
 * Room is the signaling-WS orchestrator: it opens /ws with the page's credential, applies
 * the ICE config from the join-ack, dispatches inbound frames to per-type handlers, and
 * refreshes the TURN credential before it expires. Authentication is by credential, never
 * a query-param identity (the server derives role/peer/session, EN-7): a guest passes
 * `pass=<token>`, an OBS source `src=<token>`, and a host nothing (the session cookie rides
 * automatically).
 */
export class Room {
  /** @param {string} query the /ws query string, e.g. "pass=<token>" or "" for the host cookie */
  constructor(query) {
    /** @type {Record<string, (f:any)=>void>} */
    this.handlers = {};
    /** @type {RTCIceServer[]} ICE config from the {t:"ice"} join-ack (AD-14); empty until it arrives */
    this.iceServers = [];
    /** @type {((servers:RTCIceServer[])=>void)|null} */
    this._onIce = null;
    /** @type {(()=>void)|null} */
    this._onClose = null;
    this._closed = false;
    /** @type {ReturnType<typeof setTimeout>|undefined} */
    this._refreshTimer = undefined;

    // wss when the page is served over HTTPS, else ws (browsers block ws:// from an
    // https:// page as mixed active content).
    const scheme = location.protocol === "https:" ? "wss:" : "ws:";
    const qs = query ? `?${query}` : "";
    this.ws = new WebSocket(`${scheme}//${location.host}/ws${qs}`);
    this.ws.onmessage = (e) => {
      const f = JSON.parse(e.data);
      if (f.t === "ice") {
        this._applyIce(f);
        return;
      }
      const h = this.handlers[f.t];
      if (h) h(f);
    };
    this.ws.onclose = () => {
      // The signaling socket is gone: stop the refresh timer (no more sends on a dead
      // socket) and tell the owner the connection dropped (it tears down media / shows a
      // reconnect affordance). Fires once.
      if (this._closed) return;
      this._closed = true;
      clearTimeout(this._refreshTimer);
      if (this._onClose) this._onClose();
    };
    this.ready = new Promise((resolve, reject) => {
      this.ws.onopen = () => resolve(undefined);
      this.ws.onerror = (e) => reject(e);
    });
  }

  /**
   * onClose registers a callback fired once when the signaling socket closes (abruptly or
   * after a {t:terminate}), so the owner can stop media and surface a reconnect state.
   * @param {()=>void} fn
   */
  onClose(fn) {
    this._onClose = fn;
    return this;
  }

  /**
   * _applyIce stores the ICE config the server minted for this connection and schedules a
   * refresh before the TURN credential's ttl expires (EN-4), so a long-lived connection
   * never loses relay access mid-stream.
   * @param {{iceServers?:RTCIceServer[], ttlSec?:number}} f
   */
  _applyIce(f) {
    this.iceServers = f.iceServers || [];
    if (this._onIce) this._onIce(this.iceServers);
    clearTimeout(this._refreshTimer);
    if (f.ttlSec) {
      // Refresh at ~80% of the ttl so a fresh credential is in hand before expiry.
      this._refreshTimer = setTimeout(
        () => this.send({ t: "ice-refresh" }),
        Math.max(1, f.ttlSec * 0.8) * 1000,
      );
    }
  }

  /**
   * onIce registers a callback for ICE config updates (the join-ack and each refresh), and
   * fires immediately if a config has already arrived.
   * @param {(servers:RTCIceServer[])=>void} fn
   */
  onIce(fn) {
    this._onIce = fn;
    if (this.iceServers.length) fn(this.iceServers);
    return this;
  }

  /** @param {string} t @param {(f:any)=>void} fn */
  on(t, fn) {
    this.handlers[t] = fn;
    return this;
  }

  /** @param {object} frame */
  send(frame) {
    this.ws.send(JSON.stringify(frame));
  }

  close() {
    clearTimeout(this._refreshTimer);
    this.ws.close();
  }
}
