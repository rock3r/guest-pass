/**
 * Publisher is the PUBLISHING side: it holds the guest's local camera stream and answers
 * each consumer's offer by adding its tracks, so the greenroom grid tiles and OBS source pages
 * can render the guest over P2P. One RTCPeerConnection per consumer (keyed by the
 * consumer's peer id). It answers ICE-restart re-offers transparently — addTrack is
 * idempotent — so a recovered consumer keeps receiving the same camera.
 *
 * The server only relays the opaque SDP/ICE (D-23); no media touches it.
 */
import { trackRelayUsage } from "./relaystats.js";

export class Publisher {
  /**
   * @param {import("./room.js").Room} room
   * @param {MediaStream} stream the local camera/mic stream to publish
   * @param {(pc: RTCPeerConnection, id: string) => void} [onPc] notified when a consumer pc is
   *   created, so the guest can watch it for connectivity (D-38 network-blocked detection).
   * @param {(id: string) => void} [onUntrack] notified when a consumer pc is torn down.
   * @param {string} [channel] the signaling channel (D-21): a "screen" Publisher answers offers on
   *   the screen channel and stamps every outbound signal with this `ch`, so a sharer can run BOTH a
   *   camera Publisher ("") and a screen Publisher ("screen") to the same consumer without crossing.
   */
  constructor(room, stream, onPc, onUntrack, channel) {
    this.room = room;
    this.stream = stream;
    this.channel = channel || "";
    this.closed = false;
    this.onPc = onPc || (() => {});
    this.onUntrack = onUntrack || (() => {});
    /** @type {Record<string, RTCPeerConnection>} */
    this.pcs = {};
    /** @type {Record<string, () => void>} */
    this._relayTracking = {};
    // ICE seen for an id with no pc yet: either a new consumer's candidate that raced ahead of its
    // offer, or stale trickle from a consumer that just departed. Buffered by sender and replayed when
    // an offer opens the pc; cleared on drop/close. Holding it here (not in a pc) means a departed
    // source that never re-offers can't materialize a connection that would re-arm the D-38 watchdog.
    /** @type {Record<string, RTCIceCandidateInit[]>} */
    this._earlyIce = {};
  }

  /**
   * setModalityEnabled enables/disables the local outbound track for a modality, so a
   * suppression force stops it AT SOURCE (D-13/RF-8): a force-muted/-hidden guest's track stops
   * sending in the mesh and into any OBS source. mic → audio, cam → video; share has no track in
   * the camera stream (M3 screenshare is moderation-only). track.enabled is reversible (a release
   * re-enables it), unlike track.stop().
   * @param {"mic"|"cam"|"share"} modality
   * @param {boolean} enabled
   */
  setModalityEnabled(modality, enabled) {
    const tracks =
      modality === "mic"
        ? this.stream.getAudioTracks()
        : modality === "cam"
          ? this.stream.getVideoTracks()
          : [];
    for (const t of tracks) t.enabled = enabled;
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
      // A connection is opened (and watched) ONLY by a consumer's OFFER. Bare ICE for an unknown id is
      // either a new consumer's candidate that raced ahead of its offer (PeerLink starts ICE gathering
      // at setLocalDescription, before it sends the offer) OR stale trickle from a consumer that just
      // departed (after {t:consumer-left} / mid source-token rotation). BUFFER it by sender without
      // creating or tracking a pc: a genuine offer below replays it, while a departed source that never
      // re-offers can't re-arm the D-38 watchdog (the buffer is dropped on dropConsumer / close). The
      // Publisher only answers, so a non-offer SDP (an answer) for an unknown id is likewise ignored.
      if (f.ice) {
        if (!this._earlyIce[f.from]) this._earlyIce[f.from] = [];
        this._earlyIce[f.from].push(f.ice);
        return;
      }
      if (!f.sdp || f.sdp.type !== "offer") return;
      pc = new RTCPeerConnection({ iceServers: this.room.iceServers });
      /** @type {RTCIceCandidateInit[]} */
      pc._pendingIce = this._earlyIce[f.from] || []; // replay ICE that raced ahead of this offer
      delete this._earlyIce[f.from];
      this.pcs[f.from] = pc;
      this._relayTracking[f.from] = trackRelayUsage(this.room, f.from, this.channel, pc);
      pc.onicecandidate = (e) => {
        if (e.candidate) this.room.send({ t: "signal", to: f.from, ice: e.candidate.toJSON(), ch: this.channel });
      };
      this.onPc(pc, f.from); // watch this consumer connection for D-38 network-blocked detection
    }
    if (f.sdp) {
      await pc.setRemoteDescription(f.sdp);
      if (f.sdp.type === "offer") {
        for (const track of this.stream.getTracks()) {
          if (!pc.getSenders().some((s) => s.track === track)) pc.addTrack(track, this.stream);
        }
        const answer = await pc.createAnswer();
        await pc.setLocalDescription(answer);
        this.room.send({ t: "signal", to: f.from, sdp: pc.localDescription, ch: this.channel });
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

  /** consumerIds returns the peer ids of every live consumer connection (for authorization pruning). */
  consumerIds() {
    return Object.keys(this.pcs);
  }

  /**
   * dropConsumer tears down one consumer's connection when the server says that consumer departed
   * (a host monitor via {t:peer-left}, an OBS source via {t:consumer-left}). It closes + forgets the
   * pc and untracks it from the connectivity watchdog, so a never-connected departed consumer can't
   * keep the D-38 watchdog armed (a false "network blocks P2P"). It also means a later re-offer from
   * the same id builds a FRESH pc (onSignal only creates one when none exists) rather than reusing a
   * dead one. A no-op for an id with no live pc (e.g. a mesh peer, which the Publisher never serves).
   * @param {string} id the departed consumer's peer id
   */
  dropConsumer(id) {
    delete this._earlyIce[id]; // discard any buffered pre-offer ICE for the departed consumer
    const pc = this.pcs[id];
    if (!pc) return;
    this._relayTracking[id]?.();
    delete this._relayTracking[id];
    pc.close();
    delete this.pcs[id];
    this.onUntrack(id);
  }

  /** Close all consumer connections (the local stream is owned by the caller). */
  close() {
    this.closed = true;
    for (const id of Object.keys(this.pcs)) {
      this._relayTracking[id]?.();
      this.pcs[id].close();
      this.onUntrack(id);
    }
    this.pcs = {};
    this._relayTracking = {};
    this._earlyIce = {};
  }
}
