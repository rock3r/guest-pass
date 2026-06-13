/**
 * PeerLink is the CONSUMING side of one P2P connection: a recvonly RTCPeerConnection to a
 * single remote publisher (a guest), with trickle ICE relayed over the Room's signaling
 * channel. It is used by the host-monitor tile and the OBS source page. The ICE config
 * (STUN, and a TURN entry with an ephemeral credential when configured) comes from the
 * Room's join-ack (AD-14/EN-4); the publishing side is in publisher.js.
 */
export class PeerLink {
  /**
   * @param {import("./room.js").Room} room
   * @param {string} remoteId the peer id to consume from
   * @param {RTCIceServer[]} [iceServers] ICE config from the Room's join-ack
   */
  constructor(room, remoteId, iceServers) {
    this.room = room;
    this.remoteId = remoteId;
    this.closed = false;
    /** @type {RTCIceCandidateInit[]} ICE that arrived before the remote description */
    this.pendingIce = [];
    this.pc = new RTCPeerConnection({ iceServers: iceServers || [] });
    this.pc.onicecandidate = (e) => {
      if (e.candidate && !this.closed) {
        room.send({ t: "signal", to: remoteId, ice: e.candidate.toJSON() });
      }
    };
  }

  /**
   * offer creates and sends a recvonly offer to the remote publisher. Guarded so a link
   * closed mid-negotiation (a rapid rebind) never sends a stale offer to a prior occupant.
   * @param {RTCOfferOptions} [opts]
   */
  async offer(opts) {
    // recvonly video + audio so a publisher's getUserMedia({video,audio}) stream negotiates
    // cleanly (matching m-lines); the tile renders video and (muted) audio rides along.
    this.pc.addTransceiver("video", { direction: "recvonly" });
    this.pc.addTransceiver("audio", { direction: "recvonly" });
    return this._negotiate(opts);
  }

  /**
   * restartIce re-offers with ICE restart so a path that dropped (e.g. a NAT rebinding or a
   * network change) re-gathers candidates and recovers without tearing down the link.
   */
  async restartIce() {
    return this._negotiate({ iceRestart: true });
  }

  /** @param {RTCOfferOptions} [opts] */
  async _negotiate(opts) {
    const o = await this.pc.createOffer(opts);
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
