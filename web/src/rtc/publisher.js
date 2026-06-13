/**
 * Publisher is the PUBLISHING side: it holds the guest's local camera stream and answers
 * each consumer's offer by adding its tracks, so the host-monitor tile and OBS source pages
 * can render the guest over P2P. One RTCPeerConnection per consumer (keyed by the
 * consumer's peer id). It answers ICE-restart re-offers transparently — addTrack is
 * idempotent — so a recovered consumer keeps receiving the same camera.
 *
 * The server only relays the opaque SDP/ICE (D-23); no media touches it.
 */
export class Publisher {
  /**
   * @param {import("./room.js").Room} room
   * @param {MediaStream} stream the local camera/mic stream to publish
   */
  constructor(room, stream) {
    this.room = room;
    this.stream = stream;
    this.closed = false;
    /** @type {Record<string, RTCPeerConnection>} */
    this.pcs = {};
  }

  /**
   * applyIceServers updates every live consumer connection with a refreshed ICE config
   * (e.g. a rotated TURN credential, EN-4), so existing connections keep relay access.
   * @param {RTCIceServer[]} servers
   */
  applyIceServers(servers) {
    for (const id of Object.keys(this.pcs)) {
      try {
        this.pcs[id].setConfiguration({ iceServers: servers });
      } catch (_) {
        /* setConfiguration unsupported / pc closed — ignore */
      }
    }
  }

  /** Handle a relayed signal frame from a consumer. */
  async onSignal(f) {
    if (this.closed) return; // a late frame after teardown must not spawn a new connection
    let pc = this.pcs[f.from];
    if (!pc) {
      pc = new RTCPeerConnection({ iceServers: this.room.iceServers });
      /** @type {RTCIceCandidateInit[]} */
      pc._pendingIce = [];
      this.pcs[f.from] = pc;
      pc.onicecandidate = (e) => {
        if (e.candidate) this.room.send({ t: "signal", to: f.from, ice: e.candidate.toJSON() });
      };
    }
    if (f.sdp) {
      await pc.setRemoteDescription(f.sdp);
      if (f.sdp.type === "offer") {
        for (const track of this.stream.getTracks()) {
          if (!pc.getSenders().some((s) => s.track === track)) pc.addTrack(track, this.stream);
        }
        const answer = await pc.createAnswer();
        await pc.setLocalDescription(answer);
        this.room.send({ t: "signal", to: f.from, sdp: pc.localDescription });
      }
      for (const cand of pc._pendingIce) {
        try {
          await pc.addIceCandidate(cand);
        } catch (_) {
          /* ignore */
        }
      }
      pc._pendingIce = [];
    } else if (f.ice) {
      if (pc.remoteDescription) {
        try {
          await pc.addIceCandidate(f.ice);
        } catch (_) {
          /* ignore */
        }
      } else {
        pc._pendingIce.push(f.ice); // buffer until the offer is applied
      }
    }
  }

  /** Close all consumer connections (the local stream is owned by the caller). */
  close() {
    this.closed = true;
    for (const id of Object.keys(this.pcs)) this.pcs[id].close();
    this.pcs = {};
  }
}
