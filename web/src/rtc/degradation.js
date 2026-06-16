/**
 * Per-publisher-local degradation (AD-21): each browser caps its OWN senders to relieve encoder
 * (cpu) or uplink (bandwidth) pressure, on a host-set priority order — the only mesh-coherent model
 * (no peer controls another's encoders). It samples getStats(), derives a coarse health signal,
 * detects the limitation, sheds on a priority ladder with degrade-fast / recover-slow hysteresis,
 * and self-reports {t:stats,signal,rttMs,degraded} so the host sees per-tile health (the server
 * never sees media — these are the publisher's own numbers, D-23/EN-11).
 *
 * The decision core (signalFromStats / limitationFromStats / planLadder) is pure and unit-tested;
 * DegradationController wires it to real getStats + setParameters + a report callback.
 */

// Hysteresis: degrade after the FIRST limited sample (fast), recover only after this many
// consecutive UNLIMITED samples (slow) — "degrade fast, recover slow", no flapping (D-34).
export const RECOVER_STREAK = 3;

// Bandwidth degrades the constrained link in steps: lower resolution, then frame rate, then bitrate
// (AD-21). Each step is applied to that sender's encoding params; step 0 = full quality.
export const BANDWIDTH_STEPS = [
  { scaleResolutionDownBy: 2 }, // res first
  { scaleResolutionDownBy: 2, maxFramerate: 15 }, // then fps
  { scaleResolutionDownBy: 4, maxFramerate: 15, maxBitrate: 150_000 }, // then bitrate
];

/**
 * signalFromStats maps a round-trip estimate + packet-loss fraction to a coarse 1..5 health bar
 * (5 = great, 1 = bad). Pure. Returns 0 when there is no data yet (unknown).
 * @param {number} rttMs
 * @param {number} lossFrac 0..1
 * @returns {number}
 */
export function signalFromStats(rttMs, lossFrac) {
  if (!rttMs && !lossFrac) return 0; // no sample yet
  if (lossFrac >= 0.1 || rttMs >= 600) return 1;
  if (lossFrac >= 0.05 || rttMs >= 350) return 2;
  if (lossFrac >= 0.02 || rttMs >= 200) return 3;
  if (lossFrac >= 0.01 || rttMs >= 100) return 4;
  return 5;
}

/**
 * limitationFromStats reduces per-sender qualityLimitationReason readings to one room-level
 * limitation, preferring cpu (a global encoder limit sheds whole encoders) over bandwidth (a
 * per-link limit lowers that link's quality). Returns "cpu" | "bandwidth" | null. Pure.
 * @param {Array<{reason:string}>} senderReadings each sender's qualityLimitationReason
 * @returns {"cpu"|"bandwidth"|null}
 */
export function limitationFromStats(senderReadings) {
  let bandwidth = false;
  for (const r of senderReadings) {
    if (r.reason === "cpu") return "cpu";
    if (r.reason === "bandwidth") bandwidth = true;
  }
  return bandwidth ? "bandwidth" : null;
}

/**
 * planLadder is the pure shedding/recovery decision for one sample. Given the current limitation,
 * the publisher's senders (each with a `key`, a `priority` where LOWER sheds first, and the id of
 * the bandwidth-constrained one), and the prior ladder state, it returns the new state, the
 * setParameters actions to apply, and the degraded view to report.
 *
 * - cpu → shed the lowest-priority senders first (disable whole encoders), one more each limited
 *   sample, up the ladder.
 * - bandwidth → step the CONSTRAINED sender down res→fps→bitrate.
 * - no limit → after RECOVER_STREAK clean samples, recover ONE step (un-shed the highest-priority
 *   shed sender, or step a link back up), reporting "recovering" until fully restored.
 *
 * @param {{
 *   reason: "cpu"|"bandwidth"|null,
 *   senders: Array<{key:string, priority:number, constrained?:boolean}>,
 *   state: {cpuLevel:number, bw:Record<string,number>, recoverStreak:number, lastReason:string|null},
 * }} input
 * @returns {{state:any, actions:Array<{key:string, active?:boolean, params?:object}>, degraded:{dir:string,reason:string}|null}}
 */
export function planLadder({ reason, senders, state }) {
  const bw = { ...(state.bw || {}) };
  let recoverStreak = state.recoverStreak || 0;
  let lastReason = state.lastReason || null;
  const actions = [];
  // Senders sorted so index 0 is the FIRST to shed (lowest priority); highest priority shed last.
  const byShedOrder = [...senders].sort((a, b) => a.priority - b.priority);
  // cpu shedding hard-disables encoders, but NEVER a `protected` sender (the program/monitor publish
  // path): disabling the program would kill the broadcast. The program is "degraded only as a last
  // resort" (DESIGN ladder) — for M3 the host warning is the degraded badge; we shed thumbnails and,
  // once they're exhausted, report the pressure without cutting the program.
  const sheddable = byShedOrder.filter((s) => !s.protected);
  // Clamp the carried shed level to the senders that still exist: a shed thumbnail peer can leave
  // before recovery, shrinking `sheddable`, and an unclamped index would read undefined and throw
  // (breaking the sampler). Clamping treats the departed sender as already recovered.
  const cpuLevel = Math.min(state.cpuLevel || 0, sheddable.length);

  let newCpuLevel = cpuLevel;
  let dir = null;

  if (reason === "cpu") {
    recoverStreak = 0;
    lastReason = "cpu";
    if (cpuLevel < sheddable.length) {
      newCpuLevel = cpuLevel + 1;
      actions.push({ key: sheddable[cpuLevel].key, active: false }); // shed the next-lowest thumbnail
      dir = "lowering";
    } else {
      dir = "lowering"; // thumbnails exhausted, program protected — report the pressure, don't cut it
    }
  } else if (reason === "bandwidth") {
    recoverStreak = 0;
    lastReason = "bandwidth";
    const target = senders.find((s) => s.constrained) || byShedOrder[0];
    if (target) {
      const step = bw[target.key] || 0;
      if (step < BANDWIDTH_STEPS.length) {
        bw[target.key] = step + 1;
        actions.push({ key: target.key, params: BANDWIDTH_STEPS[step] });
        dir = "lowering";
      } else {
        dir = "lowering";
      }
    }
  } else {
    // No limitation this sample. Recover slowly: only after a clean streak, and one step at a time.
    const shedding = cpuLevel > 0 || Object.values(bw).some((v) => v > 0);
    if (shedding) {
      recoverStreak += 1;
      if (recoverStreak >= RECOVER_STREAK) {
        recoverStreak = 0;
        dir = "recovering";
        // Prefer restoring a bandwidth step (cheaper) before re-enabling a shed encoder.
        const bwKey = Object.keys(bw).find((k) => bw[k] > 0);
        if (bwKey) {
          bw[bwKey] -= 1;
          const step = bw[bwKey];
          actions.push({ key: bwKey, params: step > 0 ? BANDWIDTH_STEPS[step - 1] : { scaleResolutionDownBy: 1 } });
        } else if (cpuLevel > 0) {
          newCpuLevel = cpuLevel - 1;
          actions.push({ key: sheddable[newCpuLevel].key, active: true }); // re-enable highest-priority shed
        }
      }
    }
  }

  const stillShedding = newCpuLevel > 0 || Object.values(bw).some((v) => v > 0);
  const degraded = dir ? { dir, reason: lastReason || reason || "cpu" } : stillShedding ? { dir: "lowering", reason: lastReason || "cpu" } : null;
  if (!stillShedding) lastReason = null;
  // disabled = the keys currently hard-disabled by cpu shedding (the lowest-priority thumbnails) —
  // never a protected program/monitor sender. Exposed for observability/tests.
  const disabled = sheddable.slice(0, newCpuLevel).map((s) => s.key);
  return {
    state: { cpuLevel: newCpuLevel, bw, recoverStreak, lastReason },
    actions,
    degraded,
    disabled,
  };
}

/**
 * DegradationController samples a publisher's live senders on an interval, applies the ladder, and
 * reports stats. `getTargets()` returns the current senders to manage as
 * [{key, priority, sender}] (lower priority = shed first); `report(stats)` ships {t:stats}.
 */
export class DegradationController {
  /**
   * @param {{getTargets: () => Array<{key:string, priority:number, sender:RTCRtpSender}>, report: (s:{signal:number, rttMs:number, degraded:object|null}) => void, intervalMs?: number}} opts
   */
  constructor({ getTargets, report, intervalMs = 2000 }) {
    this.getTargets = getTargets;
    this.report = report;
    this.intervalMs = intervalMs;
    this.state = { cpuLevel: 0, bw: {}, recoverStreak: 0, lastReason: null };
    /** @type {ReturnType<typeof setInterval>|undefined} */
    this._timer = undefined;
    // Program quality ceiling (D-19/AC-8): the MAX encoding the program/monitor (protected) senders
    // run at, so the program encoder is actually capped and degradation recovery never exceeds it.
    // null = no ceiling yet (browser default). Per-source overrides cap one specific source's sender
    // tighter (its ?res). _capApplied memoizes the last applied ceiling signature keyed by the SENDER
    // OBJECT (a reconnect makes a NEW sender for the same pub:<id> key — it must re-cap), recorded
    // only AFTER setParameters resolves, so the ceiling isn't re-pushed every sample yet a recreated
    // or rejected sender is re-capped. A WeakMap so dropped senders don't leak.
    /** @type {{maxRes:number, maxFps:number, maxBitrateKbps:number}|null} */
    this._ceiling = null;
    /** @type {Record<string, number>} sender key ("pub:<id>") → per-source max height override */
    this._overrides = {};
    /** @type {WeakMap<RTCRtpSender, string>} sender → last successfully applied ceiling signature */
    this._capApplied = new WeakMap();
  }

  /**
   * setCeiling sets the program quality ceiling (D-19/AC-8) and immediately caps every protected
   * (program/monitor) sender at it. A falsy/zero ceiling clears it (back to browser default).
   * @param {{maxRes:number, maxFps:number, maxBitrateKbps:number}|null} c
   */
  setCeiling(c) {
    this._ceiling = c && c.maxRes ? { maxRes: c.maxRes, maxFps: c.maxFps, maxBitrateKbps: c.maxBitrateKbps } : null;
    this._capApplied = new WeakMap(); // a new ceiling must re-push to every sender
    this._enforceCeiling();
  }

  /**
   * setSourceOverride caps ONE source's program sender (keyed "pub:<sourceId>") at `res` px — the
   * source's ?res URL param (D-19/AC-8), layered under the stream ceiling. res<=0 clears it.
   * @param {string} key sender key "pub:<sourceId>"
   * @param {number} res max height in px (0 = clear)
   */
  setSourceOverride(key, res) {
    if (res > 0) this._overrides[key] = res;
    else delete this._overrides[key];
    // No memo clear needed: the override changes the computed ceiling signature for that sender, so
    // _enforceCeiling re-applies it (a stale memo only matches an identical signature).
    this._enforceCeiling();
  }

  /**
   * _ceilingParamsFor computes the ceiling encoding for a protected sender from its captured height,
   * the stream ceiling, and any per-source override (the tighter of the two resolutions). Returns
   * null when no ceiling is set.
   * @param {{key:string, sender:RTCRtpSender}} target
   */
  _ceilingParamsFor(target) {
    const c = this._ceiling;
    if (!c) return null;
    const settings = target.sender.track && target.sender.track.getSettings ? target.sender.track.getSettings() : {};
    const h = settings.height || 0;
    const override = this._overrides[target.key];
    const maxRes = override ? Math.min(c.maxRes, override) : c.maxRes; // per-source override is res-only
    const scaleResolutionDownBy = h && maxRes ? Math.max(1, h / maxRes) : 1;
    return { scaleResolutionDownBy, maxFramerate: c.maxFps, maxBitrate: c.maxBitrateKbps * 1000 };
  }

  /**
   * _enforceCeiling caps each PROTECTED (program/monitor) sender at the ceiling baseline, UNLESS the
   * bandwidth ladder has it shed BELOW the ceiling already (don't raise a shed sender). Idempotent
   * via _capApplied so identical params aren't re-pushed every sample. The mesh thumbnails are NOT
   * capped here — they ride their own low-res shedding, not the program ceiling.
   */
  _enforceCeiling() {
    if (!this._ceiling) return;
    for (const t of this.getTargets()) {
      if (!t.protected) continue;
      if ((this.state.bw[t.key] || 0) > 0) continue; // shed below the ceiling — leave it lower
      const params = this._ceilingParamsFor(t);
      const sig = `${params.scaleResolutionDownBy}|${params.maxFramerate}|${params.maxBitrate}`;
      const sender = t.sender;
      if (this._capApplied.get(sender) === sig) continue; // already capped (this exact sender object)
      // Record the memo ONLY after setParameters resolves: a recreated sender (reconnect) is a fresh
      // object so it misses the memo and re-caps, and a transient rejection (renegotiation) leaves no
      // memo so the next sample retries — the recreated/uncapped program sender can't outrun the cap.
      sender
        .setParameters(encodingParams(sender, { params }))
        .then(() => this._capApplied.set(sender, sig))
        .catch(() => {
          /* renegotiating / closed — leave it unmemoized so the next sample retries */
        });
    }
  }

  start() {
    if (this._timer) return;
    this._timer = setInterval(() => this.sample(), this.intervalMs);
  }

  stop() {
    clearInterval(this._timer);
    this._timer = undefined;
  }

  /**
   * recoverNow restores every shed sender immediately and resets the ladder — the host "bump
   * quality now" override (D-34), bypassing the slow recover hysteresis. If the pressure persists,
   * the next sample simply re-degrades; the immediate sample() re-reports current health.
   */
  recoverNow() {
    // Invalidate any sample() currently suspended mid-pass (it captured the prior epoch at its start):
    // when it resumes after `await getStats()` it must NOT re-apply its now-stale shedding plan — built
    // from pre-recovery stats against the ladder this override is about to reset — nor emit a stale
    // {t:stats} that would undo this recovery (the degrade-fast path racing the host's "bump quality
    // now"). The resuming pass sees the bumped epoch and abandons itself.
    this._epoch = (this._epoch || 0) + 1;
    for (const t of this.getTargets()) {
      applyAction(t.sender, { active: true, params: { scaleResolutionDownBy: 1 } });
    }
    this.state = { cpuLevel: 0, bw: {}, recoverStreak: 0, lastReason: null };
    // "Bump quality now" restores to the program CEILING, never above it (D-19/AC-8): re-cap the
    // protected senders we just reset to scaleResolutionDownBy:1 so the override can't exceed the
    // host's ceiling. _capApplied is cleared so the re-cap actually pushes.
    this._capApplied = new WeakMap();
    this._enforceCeiling();
    // Report recovered IMMEDIATELY (don't wait for the next ~2s sample) so the host's badge and the
    // guest's own degradation clear right away — that's the point of the override. If the pressure
    // persists, the next sample re-degrades.
    this.report({ signal: this._lastSignal || 0, rttMs: this._lastRttMs || 0, degraded: null });
    // Debug/test observability: count "bump quality now" executions (deterministic — natural
    // recovery never calls this), so a test can prove the host→broadcast→recoverNow wiring fired.
    if (typeof window !== "undefined") window.__gpRecoverNowCount = (window.__gpRecoverNowCount || 0) + 1;
  }

  /** sample reads getStats across the live senders, plans the ladder, applies it, and reports. */
  async sample() {
    // Re-entrancy guard: getStats() is async, so a slow round could overlap the next interval tick
    // and two passes would race this.state (conflicting actions / lost recovery streak). Skip a tick
    // if the prior sample is still in flight.
    if (this._sampling) return;
    this._sampling = true;
    try {
      await this._sampleOnce();
    } finally {
      this._sampling = false;
    }
  }

  async _sampleOnce() {
    const targets = this.getTargets();
    if (!targets.length) return;
    const epoch = this._epoch || 0; // captured before the awaits; a recoverNow() mid-pass bumps it
    let rttMs = 0;
    let lossFrac = 0;
    const readings = [];
    const byKey = {};
    for (const t of targets) {
      byKey[t.key] = t;
      try {
        const stats = await t.sender.getStats();
        const r = readSenderStats(stats);
        readings.push({ reason: r.reason });
        if (r.rttMs > rttMs) rttMs = r.rttMs; // worst link drives the bar
        if (r.lossFrac > lossFrac) lossFrac = r.lossFrac;
        if (r.reason === "bandwidth") t.constrained = true;
      } catch (_) {
        /* a closed/negotiating sender has no stats — skip it this sample */
      }
    }
    // A host recover-now landed while we were awaiting getStats: this pass's plan + report are stale
    // (pre-recovery stats, since-reset ladder). Abandon it so we don't re-shed or report over the
    // override — the next interval tick samples fresh.
    if ((this._epoch || 0) !== epoch) return;
    const reason = limitationFromStats(readings);
    const plan = planLadder({ reason, senders: targets, state: this.state });
    this.state = plan.state;
    for (const a of plan.actions) {
      const target = byKey[a.key];
      if (!target) continue;
      applyAction(target.sender, a);
      // A shed/recover action touched a protected sender → its applied params no longer match the
      // ceiling memo, so clear it; _enforceCeiling below re-caps it to the ceiling if it recovered
      // to baseline, or leaves it shed below the ceiling.
      if (target.protected) this._capApplied.delete(target.sender);
    }
    // Cap any protected sender at the program ceiling (D-19/AC-8): catches a newly-connected program
    // consumer and a sender that just recovered to baseline, so the program encoder never runs above
    // the ceiling and recovery clamps to it (shed-below senders are left untouched).
    this._enforceCeiling();
    // Debug/test observability: expose this sample's reason + decision (matches the wsRecorder seam
    // used by the browser tests). No behavior, no secrets — just this publisher's own numbers.
    if (typeof window !== "undefined") {
      window.__gpDegradation = { reason, degraded: plan.degraded, actions: plan.actions, disabled: plan.disabled };
    }
    this._lastSignal = signalFromStats(rttMs, lossFrac); // remembered so recoverNow can report at once
    this._lastRttMs = rttMs;
    this.report({ signal: this._lastSignal, rttMs, degraded: plan.degraded });
  }
}

/**
 * readSenderStats pulls the limitation reason, RTT, and loss fraction from one sender's stats.
 * @param {RTCStatsReport} stats
 * @returns {{reason:string, rttMs:number, lossFrac:number}}
 */
function readSenderStats(stats) {
  let reason = "none";
  let rttMs = 0;
  let lossFrac = 0;
  stats.forEach((s) => {
    if (s.type === "outbound-rtp" && s.qualityLimitationReason && s.qualityLimitationReason !== "none") {
      reason = s.qualityLimitationReason;
    }
    if (s.type === "remote-inbound-rtp") {
      if (typeof s.roundTripTime === "number") rttMs = Math.round(s.roundTripTime * 1000);
      if (typeof s.fractionLost === "number") lossFrac = s.fractionLost;
    }
  });
  return { reason, rttMs, lossFrac };
}

/**
 * encodingParams folds one action into a sender's current RTCRtpSendParameters (encodings[0]),
 * returning the object to hand to setParameters. Split out so _enforceCeiling can apply the same
 * encoding shape but observe setParameters success/failure itself (success-gated ceiling memo).
 * @param {RTCRtpSender} sender
 * @param {{active?:boolean, params?:object}} action
 */
function encodingParams(sender, action) {
  const p = sender.getParameters();
  if (!p.encodings || !p.encodings.length) p.encodings = [{}];
  const enc = p.encodings[0];
  if (action.active !== undefined) enc.active = action.active;
  if (action.params) {
    enc.scaleResolutionDownBy = action.params.scaleResolutionDownBy ?? 1;
    if (action.params.maxFramerate !== undefined) enc.maxFramerate = action.params.maxFramerate;
    else delete enc.maxFramerate;
    if (action.params.maxBitrate !== undefined) enc.maxBitrate = action.params.maxBitrate;
    else delete enc.maxBitrate;
  }
  return p;
}

/**
 * applyAction applies one ladder action to a sender via setParameters (the proven AD-21 mechanism).
 * @param {RTCRtpSender} sender
 * @param {{active?:boolean, params?:object}} action
 */
function applyAction(sender, action) {
  return sender.setParameters(encodingParams(sender, action)).catch(() => {
    /* setParameters can reject on a renegotiating sender; the next sample retries */
  });
}

// Debug/test seam: expose the PURE ladder helpers (and the controller class, for the recover-now /
// in-flight-sample race test) so a browser test can drive the decision logic directly (cpu/bandwidth
// shedding order, program protection, hysteresis, peer-leave clamp, recover-now cancellation) without
// real media. Pure functions + the constructor; no secrets, no behavior change.
if (typeof window !== "undefined") {
  window.__gpDeg = { planLadder, signalFromStats, limitationFromStats, DegradationController };
}
