/**
 * Room is a minimal signaling-WS orchestrator: it opens the /ws connection and
 * dispatches inbound frames to per-type handlers. The full orchestrator (roster,
 * locks, reconnect/backoff) is M3; this is the SPIKE-2 slice.
 */
export class Room {
  /** @param {{session:string, peer:string, role:string, slot?:string}} opts */
  constructor(opts) {
    this.peer = opts.peer;
    /** @type {Record<string, (f:any)=>void>} */
    this.handlers = {};
    const q = new URLSearchParams({
      session: opts.session,
      peer: opts.peer,
      role: opts.role,
      slot: opts.slot || "",
    });
    this.ws = new WebSocket(`ws://${location.host}/ws?${q}`);
    this.ws.onmessage = (e) => {
      const f = JSON.parse(e.data);
      const h = this.handlers[f.t];
      if (h) h(f);
    };
    this.ready = new Promise((res) => {
      this.ws.onopen = () => res(undefined);
    });
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
}
