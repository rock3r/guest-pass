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
    this.pc = new RTCPeerConnection();
    this.pc.onicecandidate = (e) => {
      if (e.candidate) {
        room.send({ t: "signal", to: remoteId, ice: e.candidate.toJSON() });
      }
    };
  }

  /** Create and send a recvonly offer to the remote peer. */
  async offer() {
    this.pc.addTransceiver("video", { direction: "recvonly" });
    const o = await this.pc.createOffer();
    await this.pc.setLocalDescription(o);
    this.room.send({ t: "signal", to: this.remoteId, sdp: this.pc.localDescription });
  }

  /** Handle a relayed signal frame from the remote peer. */
  async onSignal(f) {
    if (f.sdp) {
      await this.pc.setRemoteDescription(f.sdp);
    } else if (f.ice) {
      try {
        await this.pc.addIceCandidate(f.ice);
      } catch (_) {
        // candidate may arrive before remote description; browsers buffer or we drop
      }
    }
  }

  close() {
    this.pc.close();
  }
}
