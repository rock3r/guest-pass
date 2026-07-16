/**
 * Report one completed WebRTC media link's transport class without retaining any peer detail.
 * Either endpoint may observe the link, so the server deduplicates the peer pair and channel for
 * the room lifetime. A report is sent only after a selected candidate pair is available.
 */
export function trackRelayUsage(room, remoteId, channel, pc) {
  let stopped = false;
  let sent = false;
  const report = async () => {
    if (stopped || sent || pc.connectionState !== "connected") return;
    const relay = await selectedPairUsesRelay(pc);
    if (stopped || sent || relay === null) return;
    sent = true;
    room.send({ t: "connection-stats", peerId: remoteId, ch: channel || "", relay });
  };
  pc.addEventListener("connectionstatechange", report);
  pc.addEventListener("iceconnectionstatechange", report);
  report();
  return () => {
    stopped = true;
    pc.removeEventListener("connectionstatechange", report);
    pc.removeEventListener("iceconnectionstatechange", report);
  };
}

async function selectedPairUsesRelay(pc) {
  try {
    const stats = await pc.getStats();
    for (const pair of stats.values()) {
      if (pair.type !== "candidate-pair" || pair.state !== "succeeded") continue;
      // `selected` is Chrome's explicit signal; `nominated` covers browsers that omit it.
      if (pair.selected === false || pair.nominated === false) continue;
      const local = stats.get(pair.localCandidateId);
      const remote = stats.get(pair.remoteCandidateId);
      // Wait for both candidates: an incomplete report could call a link direct while the
      // not-yet-materialized remote side is a relay candidate.
      if (!local || !remote) continue;
      return local?.candidateType === "relay" || remote?.candidateType === "relay";
    }
  } catch (_) {
    // Stats are advisory telemetry. Retry on the next connection-state transition.
  }
  return null;
}
