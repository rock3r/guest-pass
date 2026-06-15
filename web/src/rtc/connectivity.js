/**
 * ConnectivityWatch detects the D-38 "your network blocks peer-to-peer" condition: a guest behind
 * symmetric NAT or a UDP-blocking firewall whose P2P media connections never establish. v1 is
 * STUN-only (no TURN relay by default), so such a guest can't reach ANYONE over media even though
 * its signaling WebSocket connects fine — without this it would sit forever on a false "you're live"
 * while no media flows (a silent hang). This watcher arms a watchdog once the guest is publishing and
 * the first consumer/peer pc is created (someone is actually trying); if no tracked pc has EVER
 * reached "connected" within the window, it flags the network blocked.
 *
 * The "ever connected" guard (page-scoped) means a guest that reaches anyone — even once, even
 * slowly — is never told its network is blocked: this covers only the INITIAL can't-connect case. A
 * connect-then-drop mid-session is the existing reconnecting path (untouched, AC-13). The shape
 * mirrors DegradationController (web/src/rtc/degradation.js): an injected-deps RTC helper the guest
 * island owns, with a window.__gp* debug seam for the browser tests.
 */

// Production watchdog window: a healthy STUN/loopback path connects within a few seconds; this much
// time with a peer present and ZERO connections means the network is blocking P2P (D-38).
export const NETBLOCK_TIMEOUT_MS = 20000;

// everConnected is page-scoped (not per-instance): once this guest reaches any peer in this page
// load it never sees the network-blocked screen — a later media drop is the reconnecting path, not
// "your network blocks P2P". It resets naturally per page load (a fresh module instance), and a
// re-publish after a reconnect reuses it, so a guest that already connected once is never blocked.
let everConnected = false;

export class ConnectivityWatch {
  /**
   * @param {{onBlocked: () => void, onRecovered?: () => void, timeoutMs?: number}} opts
   *   onBlocked fires once when the watchdog expires with no connection ever made; onRecovered fires
   *   if a pc connects AFTER we flagged blocked (a slow network that eventually came through).
   */
  constructor({ onBlocked, onRecovered, timeoutMs } = {}) {
    this.onBlocked = onBlocked || (() => {});
    this.onRecovered = onRecovered || (() => {});
    // Test-only override (window.__gpNetBlockMs) so a browser test needn't wait the full production
    // window; production never sets it, so the default stands. Mirrors the window.__gp* test seams in
    // this npm-free harness. Validated to a positive finite number so a stray/garbage global can't
    // break the watchdog; it can only set a (non-security) UX timeout, never weaken the default.
    const raw = typeof window !== "undefined" ? window.__gpNetBlockMs : undefined;
    const override = typeof raw === "number" && isFinite(raw) && raw > 0 ? raw : NETBLOCK_TIMEOUT_MS;
    this.timeoutMs = timeoutMs ?? override;
    /** @type {Map<string, RTCPeerConnection>} */
    this._pcs = new Map();
    /** @type {Map<string, () => void>} per-key listener removers */
    this._listeners = new Map();
    /** @type {ReturnType<typeof setTimeout>|undefined} */
    this._timer = undefined;
    this._blocked = false;
    // _flaggedBlocked latches true the first time the watchdog fires onBlocked and is never reset
    // (even on recovery): observability so a test can prove the screen was NEVER flagged — catching
    // even a transient flash that onRecovered would otherwise clear from the DOM before assertion.
    this._flaggedBlocked = false;
    this._stopped = false;
    this._expose();
  }

  /**
   * track starts watching one pc for connectivity, arming the watchdog on the FIRST tracked pc (a
   * consumer/peer is now actually trying). A pc reaching "connected"/"completed" sets the page-scoped
   * everConnected flag — and recovers if we'd already flagged blocked. Idempotent per key.
   * @param {RTCPeerConnection} pc
   * @param {string} key
   */
  track(pc, key) {
    if (this._stopped || !pc || this._pcs.has(key)) return;
    this._pcs.set(key, pc);
    const onChange = () => {
      const cs = pc.connectionState;
      const ics = pc.iceConnectionState;
      // Either the aggregate state or the ICE state reaching connected/completed proves a live path —
      // watching both catches the connection as early as possible (and across browser quirks).
      if (cs === "connected" || cs === "completed" || ics === "connected" || ics === "completed") {
        this._markConnected();
      }
    };
    pc.addEventListener("connectionstatechange", onChange);
    pc.addEventListener("iceconnectionstatechange", onChange);
    this._listeners.set(key, () => {
      pc.removeEventListener("connectionstatechange", onChange);
      pc.removeEventListener("iceconnectionstatechange", onChange);
    });
    onChange(); // a pc that is already connected when we attach (rare) counts immediately
    this._arm();
    this._expose();
  }

  /** untrack drops a closed/departed pc so it no longer counts as "a peer is trying". */
  untrack(key) {
    const remove = this._listeners.get(key);
    if (remove) remove();
    this._listeners.delete(key);
    this._pcs.delete(key);
    this._expose();
  }

  /** stop clears the watchdog + all listeners (island teardown / unmount). */
  stop() {
    this._stopped = true;
    clearTimeout(this._timer);
    this._timer = undefined;
    for (const remove of this._listeners.values()) remove();
    this._listeners.clear();
    this._pcs.clear();
    this._expose();
  }

  _arm() {
    // Arm ONCE, on the first tracked pc, only while a connection is still possible-but-unproven. The
    // window is session-level ("time since the guest started trying to reach anyone"), NOT per-pc:
    // the blocker is the guest's OWN NAT/firewall, uniform across every peer, so a later pc replacing
    // an earlier one inherits the same deadline rather than restarting the clock. Re-arming per pc
    // would let a guest churning peers dodge the watchdog forever (a false negative). The
    // "ever connected" guard below means a guest that reached anyone is never warned.
    if (this._timer || this._stopped || everConnected) return;
    this._timer = setTimeout(() => {
      this._timer = undefined;
      if (this._stopped || everConnected || this._blocked) return;
      if (this._pcs.size === 0) return; // nothing is being attempted — never a false positive
      // Known limitation: a Publisher consumer (host monitor / OBS source) that connects-then-departs
      // before ICE is not individually untracked — those consumers aren't in the guest's roster, so
      // there is no client-side "consumer gone" signal (only mesh peers get peer-left/_drop). A
      // departed never-connected consumer can keep _pcs non-empty and, if it was the only one, trip a
      // rare/narrow false positive that Retry clears. A precise fix needs a signaling-level departure
      // notice (a server change, out of scope here); a pc-state heuristic is rejected because it
      // can't tell "network blocked" from "consumer left" without risking the worse failure — NOT
      // warning a genuinely blocked guest. The common case (persistent host/OBS consumers) is correct.
      this._blocked = true;
      this._flaggedBlocked = true;
      this._expose();
      this.onBlocked();
    }, this.timeoutMs);
  }

  _markConnected() {
    everConnected = true;
    // A live connection makes the watchdog moot; cancel it so a later teardown race can't fire it.
    clearTimeout(this._timer);
    this._timer = undefined;
    const wasBlocked = this._blocked;
    this._blocked = false;
    this._expose();
    if (wasBlocked) this.onRecovered();
  }

  // Debug/test seam (mirrors window.__gpDegradation): expose the watch's coarse state so a browser
  // test can observe "ever connected" without real timing. No behavior, no secrets.
  _expose() {
    if (typeof window !== "undefined") {
      window.__gpNetwatch = {
        everConnected,
        blocked: this._blocked,
        flaggedBlocked: this._flaggedBlocked,
        tracked: this._pcs.size,
        timeoutMs: this.timeoutMs,
      };
    }
  }
}
