/**
 * PeerLink is the CONSUMING side of one P2P connection: a recvonly RTCPeerConnection to a
 * single remote publisher (a guest), with trickle ICE relayed over the Room's signaling
 * channel. It is used by the greenroom grid tiles and the OBS source page. The ICE config
 * (STUN, and a TURN entry with an ephemeral credential when configured) comes from the
 * Room's join-ack (AD-14/EN-4); the publishing side is in publisher.js.
 */
export class PeerLink {
  /**
   * @param {import("./room.js").Room} room
   * @param {string} remoteId the peer id to consume from
   * @param {RTCIceServer[]} [iceServers] ICE config from the Room's join-ack
   * @param {string} [channel] the signaling channel (D-21): "screen" consumes the remote's
   *   SECOND publisher (the screenshare track) distinct from the camera (default ""). Every
   *   outbound signal carries this `ch`, and the consumer routes inbound signals to this link
   *   by matching (from, ch), so two links to the same peer (camera + screen) don't cross.
   */
  constructor(room, remoteId, iceServers, channel) {
    this.room = room;
    this.remoteId = remoteId;
    this.channel = channel || "";
    this.closed = false;
    /** @type {RTCIceCandidateInit[]} ICE that arrived before the remote description */
    this.pendingIce = [];
    this.pc = new RTCPeerConnection({ iceServers: iceServers || [] });
    this.pc.onicecandidate = (e) => {
      if (e.candidate && !this.closed) {
        room.send({ t: "signal", to: remoteId, ice: e.candidate.toJSON(), ch: this.channel });
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
   * setRemoteTrackEnabled mutes/unmutes the REMOTE track of a modality on this consuming link, so a
   * suppression lock detaches the locked peer's media from rendering AND OBS output independent of the
   * (possibly modified) target — receiver-side enforcement (RF-8). mic → the audio receiver, cam → the
   * video receiver; share has no separately-consumed track in M3 (screenshare is moderation-only), so
   * it is a no-op, mirroring publisher.setModalityEnabled. It uses receiver.track.enabled, which is
   * REVERSIBLE (a release re-enables it) — never track.stop(). A disabled remote video track renders
   * black, exactly as a voluntary source-side cam-off already does.
   * @param {"mic"|"cam"|"share"} modality
   * @param {boolean} enabled
   */
  setRemoteTrackEnabled(modality, enabled) {
    const kind = modality === "mic" ? "audio" : modality === "cam" ? "video" : null;
    if (!kind) return; // share: no consumed track in M3
    for (const r of this.pc.getReceivers()) {
      if (r.track && r.track.kind === kind) r.track.enabled = enabled;
    }
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
    this.room.send({ t: "signal", to: this.remoteId, sdp: this.pc.localDescription, ch: this.channel });
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
