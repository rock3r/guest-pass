/**
 * PeerLink is one RTCPeerConnection to a single remote peer, with trickle ICE
 * relayed over the Room's signaling channel. This SPIKE-2 variant is the consuming
 * (recvonly) side used by an OBS source page; the publishing side and the per-link
 * bitrate caps / degradation (AD-21) land in M2/M3.
 */
export class PeerLink {
  /** @param {import("./room.js").Room} room @param {string} remoteId */
  constructor(room, remoteId) {
    this.room = room;
    this.remoteId = remoteId;
    this.closed = false;
    /** @type {RTCIceCandidateInit[]} ICE that arrived before the remote description */
    this.pendingIce = [];
    this.pc = new RTCPeerConnection();
    this.pc.onicecandidate = (e) => {
      if (e.candidate && !this.closed) {
        room.send({ t: "signal", to: remoteId, ice: e.candidate.toJSON() });
      }
    };
  }

  /** Create and send a recvonly offer to the remote peer. Guarded so a link closed
   *  mid-negotiation (a rapid rebind) never sends a stale offer to a prior occupant. */
  async offer() {
    this.pc.addTransceiver("video", { direction: "recvonly" });
    const o = await this.pc.createOffer();
    if (this.closed) return;
    await this.pc.setLocalDescription(o);
    if (this.closed) return;
    this.room.send({ t: "signal", to: this.remoteId, sdp: this.pc.localDescription });
  }

  /** Handle a relayed signal frame from the remote peer. */
  async onSignal(f) {
    if (this.closed) return;
    if (f.sdp) {
      await this.pc.setRemoteDescription(f.sdp);
      // Replay any ICE that arrived before the remote description was set.
      for (const cand of this.pendingIce) {
        try {
          await this.pc.addIceCandidate(cand);
        } catch (_) {
          /* ignore */
        }
      }
      this.pendingIce = [];
    } else if (f.ice) {
      if (this.pc.remoteDescription) {
        try {
          await this.pc.addIceCandidate(f.ice);
        } catch (_) {
          /* ignore */
        }
      } else {
        this.pendingIce.push(f.ice); // buffer until the offer/answer is in place
      }
    }
  }

  close() {
    this.closed = true;
    this.pc.close();
  }
}
