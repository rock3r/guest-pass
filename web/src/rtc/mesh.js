/**
 * Backstage thumbnail mesh (D-10 everyone-sees-everyone). Each backstage guest/co-host both
 * publishes its camera to and consumes every other backstage guest/co-host's camera over a single
 * bidirectional P2P connection per pair, so each guest renders the others as live thumbnails. The
 * host (no camera in M3) and OBS sources are NOT part of this mesh — they consume the guest over the
 * one-way Publisher path instead. The server only relays the opaque SDP/ICE (D-23).
 */

/**
 * isMeshRole reports whether a roster peer is a backstage mesh participant (a guest or co-host).
 * The host and OBS-source virtual peers are not — they consume via the Publisher.
 * @param {string} role
 * @returns {boolean}
 */
export function isMeshRole(role) {
  return role === "guest" || role === "cohost";
}

/**
 * MeshPeer is ONE bidirectional connection between this guest and one other backstage participant.
 * Exactly one side offers — the peer with the lexicographically smaller id — so there is no glare
 * and a single RTCPeerConnection per pair (ICE is unambiguous). Both sides add their camera, so the
 * connection carries both directions; ontrack delivers the remote camera for the thumbnail.
 */
export class MeshPeer {
  /**
   * @param {import("./room.js").Room} room
   * @param {string} selfId this guest's own peer id
   * @param {string} remoteId the remote participant's peer id
   * @param {MediaStream} localStream this guest's camera/mic to send
   * @param {RTCIceServer[]} [iceServers]
   */
  constructor(room, selfId, remoteId, localStream, iceServers) {
    this.room = room;
    this.remoteId = remoteId;
    this.localStream = localStream;
    this.closed = false;
    // Deterministic role: the LOWER id offers, the higher id answers — one offer per pair, no glare.
    this.isOfferer = selfId < remoteId;
    /** @type {RTCIceCandidateInit[]} ICE that arrived before the remote description */
    this.pendingIce = [];
    /** @type {((stream: MediaStream) => void)|null} */
    this.ontrack = null;
    this.pc = new RTCPeerConnection({ iceServers: iceServers || [] });
    this.pc.onicecandidate = (e) => {
      if (e.candidate && !this.closed) {
        this.room.send({ t: "signal", to: this.remoteId, ice: e.candidate.toJSON() });
      }
    };
    this.pc.ontrack = (e) => {
      if (this.ontrack) this.ontrack(e.streams[0]);
    };
    this.pc.oniceconnectionstatechange = () => {
      if (this.pc.iceConnectionState === "failed") this.restartIce();
    };
  }

  /** start negotiation if this peer is the offerer; the answerer negotiates on the incoming offer. */
  async start() {
    if (!this.isOfferer) return;
    this._addLocalTracks();
    this._ensureRecvTransceivers();
    return this._negotiate();
  }

  // _ensureRecvTransceivers guarantees the OFFER carries both an audio and a video m-line, even when
  // this guest publishes only one of them — the audio-only fallback (PD-12) sends no camera, so its
  // offer would otherwise omit the video m-line and the answering peer would have nothing to send its
  // camera back on, leaving this guest's thumbnail of that peer blank. Offerer-only: an answer can't
  // introduce an m-line the offer lacks, so the missing receive direction must be opened here. A kind
  // already covered by a local sender (the common both-tracks case) is left untouched — no extra
  // transceiver, so a normal guest's negotiation is unchanged.
  _ensureRecvTransceivers() {
    const sent = new Set();
    for (const s of this.pc.getSenders()) {
      if (s.track) sent.add(s.track.kind);
    }
    for (const kind of ["audio", "video"]) {
      if (!sent.has(kind)) this.pc.addTransceiver(kind, { direction: "recvonly" });
    }
  }

  _addLocalTracks() {
    // addTrack reuses a same-kind transceiver created from the remote offer (answerer side) or
    // creates sendrecv transceivers (offerer side); guarded so a re-offer doesn't double-add.
    for (const track of this.localStream.getTracks()) {
      if (!this.pc.getSenders().some((s) => s.track === track)) {
        this.pc.addTrack(track, this.localStream);
      }
    }
  }

  /** @param {RTCOfferOptions} [opts] */
  async _negotiate(opts) {
    const offer = await this.pc.createOffer(opts);
    if (this.closed) return;
    await this.pc.setLocalDescription(offer);
    if (this.closed) return;
    this.room.send({ t: "signal", to: this.remoteId, sdp: this.pc.localDescription });
  }

  /** restartIce re-offers with an ICE restart (offerer only) so a dropped path recovers. */
  async restartIce() {
    if (!this.isOfferer || this.closed) return;
    return this._negotiate({ iceRestart: true });
  }

  /** Handle a relayed signal frame from the remote peer. */
  async onSignal(f) {
    if (this.closed) return;
    if (f.sdp) {
      await this.pc.setRemoteDescription(f.sdp);
      if (f.sdp.type === "offer") {
        // Answerer: add our camera to the offered transceivers, then answer.
        this._addLocalTracks();
        const answer = await this.pc.createAnswer();
        if (this.closed) return;
        await this.pc.setLocalDescription(answer);
        this.room.send({ t: "signal", to: this.remoteId, sdp: this.pc.localDescription });
      }
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

  /** applyIceServers updates the live connection with a refreshed ICE config (EN-4). */
  applyIceServers(servers) {
    try {
      this.pc.setConfiguration({ iceServers: servers });
    } catch (_) {
      /* setConfiguration unsupported / pc closed — ignore */
    }
  }

  /**
   * setRemoteTrackEnabled mutes/unmutes the REMOTE track of a modality on this mesh link, so a
   * suppression lock detaches the locked peer's thumbnail media independent of the (possibly modified)
   * target — receiver-side enforcement (RF-8). It operates on getReceivers() (the INBOUND track only —
   * our own outbound sender is untouched): mic → audio, cam → video; share has no consumed track in M3.
   * Uses track.enabled (reversible — a release re-enables it), never track.stop().
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

  close() {
    this.closed = true;
    this.pc.close();
  }
}

/**
 * MeshManager owns this guest's set of MeshPeers — one per other backstage participant — and the
 * received thumbnail streams. The owner drives it from the roster (sync) and routes peer signals to
 * it (handleSignal), and renders from streams(); onUpdate fires whenever the rendered set changes
 * (a thumbnail arrives, a peer joins/leaves).
 */
export class MeshManager {
  /**
   * @param {import("./room.js").Room} room
   * @param {() => MediaStream|null} getLocalStream the guest's camera/mic (read lazily so a late
   *   capture is still picked up)
   * @param {() => void} onUpdate called when the rendered thumbnail set changes
   * @param {(pc: RTCPeerConnection, id: string) => void} [onPc] notified when a mesh peer connection
   *   is created, so the guest can watch it for connectivity (D-38 network-blocked detection).
   * @param {(id: string) => void} [onUntrack] notified when a mesh peer connection is dropped.
   */
  constructor(room, getLocalStream, onUpdate, onPc, onUntrack) {
    this.room = room;
    this.getLocalStream = getLocalStream;
    this.onUpdate = onUpdate || (() => {});
    this.onPc = onPc || (() => {});
    this.onUntrack = onUntrack || (() => {});
    /** @type {Map<string, MeshPeer>} */
    this.peers = new Map();
    /** @type {Map<string, MediaStream>} */
    this._streams = new Map();
    this.selfId = "";
    /** @type {RTCIceServer[]} */
    this.iceServers = [];
    // Per-peer suppression-locked modalities (RF-8 receiver-side), set from the roster by the owner.
    // Held here (not just applied once) so a freshly-arrived thumbnail track re-asserts the lock.
    /** @type {Map<string, string[]>} */
    this._locked = new Map();
  }

  /** streams returns the received remote thumbnail streams, keyed by remote peer id. */
  streams() {
    return this._streams;
  }

  /**
   * sync reconciles the mesh against the current roster: open a MeshPeer for every backstage
   * guest/co-host that isn't us and isn't already connected, and drop any peer no longer present.
   * @param {string} selfId
   * @param {Array<{id:string, role:string}>} roster
   */
  sync(selfId, roster) {
    this.selfId = selfId;
    const localStream = this.getLocalStream();
    if (!selfId || !localStream) return; // can't mesh until we know our id and have a camera
    const want = new Set();
    for (const p of roster) {
      if (p.id === selfId || !isMeshRole(p.role)) continue;
      want.add(p.id);
      this._ensure(p.id, localStream);
    }
    for (const id of [...this.peers.keys()]) {
      if (!want.has(id)) this._drop(id);
    }
  }

  _ensure(remoteId, localStream) {
    if (this.peers.has(remoteId)) return; // keep the live connection across roster updates
    const mp = new MeshPeer(this.room, this.selfId, remoteId, localStream, this.iceServers);
    mp.ontrack = (stream) => {
      this._streams.set(remoteId, stream);
      this._applyLocks(remoteId); // re-assert any suppression lock on the freshly-arrived track (RF-8)
      this.onUpdate();
    };
    this.peers.set(remoteId, mp);
    this.onPc(mp.pc, remoteId); // watch this mesh connection for D-38 network-blocked detection
    mp.start(); // offerer offers immediately; answerer waits for the offer
  }

  _drop(id) {
    const mp = this.peers.get(id);
    if (mp) mp.close();
    this.peers.delete(id);
    this._streams.delete(id);
    this._locked.delete(id);
    this.onUntrack(id); // this peer's connection no longer counts toward connectivity (D-38)
    this.onUpdate();
  }

  /**
   * setLocks records a peer's suppression-locked modalities (from the roster's entry.locks) and
   * detaches/re-attaches its remote thumbnail tracks accordingly (RF-8 receiver-side) — independent
   * of whether that peer cooperates. A no-op for a peer with no live mesh link yet; the lock re-asserts
   * when its track arrives (see _ensure).
   * @param {string} id
   * @param {string[]} lockedKinds  modalities currently locked for the peer (mic|cam|share)
   */
  setLocks(id, lockedKinds) {
    this._locked.set(id, lockedKinds || []);
    this._applyLocks(id);
  }

  /** _applyLocks enforces the stored lock set on one peer's live mesh link. */
  _applyLocks(id) {
    const mp = this.peers.get(id);
    if (!mp) return;
    const locked = this._locked.get(id) || [];
    for (const m of ["mic", "cam", "share"]) mp.setRemoteTrackEnabled(m, !locked.includes(m));
  }

  /**
   * handleSignal routes a relayed signal from a backstage peer to its MeshPeer. Returns true if it
   * handled the frame (so the caller doesn't also feed it to the Publisher). A signal for a peer not
   * yet synced is created lazily (the answerer side of a just-joined pair).
   * @param {{from:string}} f
   * @returns {boolean}
   */
  handleSignal(f) {
    let mp = this.peers.get(f.from);
    if (!mp) {
      const localStream = this.getLocalStream();
      if (!this.selfId || !localStream) return false; // not ready — let the Publisher try
      this._ensure(f.from, localStream);
      mp = this.peers.get(f.from);
    }
    if (mp) {
      mp.onSignal(f);
      return true;
    }
    return false;
  }

  /** isMeshPeer reports whether a peer id currently has a mesh connection (for signal routing). */
  isMeshPeer(id) {
    return this.peers.has(id);
  }

  /**
   * reconnect forces an ICE restart on a stuck mesh link (manual recovery for a tile). It does NOT
   * drop+recreate: only the offerer side can re-offer, so dropping from the answerer side would tear
   * down a working connection that the remote offerer would never re-establish. restartIce is a
   * no-op on the answerer side — the offerer drives recovery (its own iceconnectionstate "failed"
   * triggers an automatic restart) — so this is safe from either tile.
   */
  reconnect(id) {
    const mp = this.peers.get(id);
    if (mp) mp.restartIce();
  }

  /** applyIceServers updates every live mesh connection with a refreshed ICE config (EN-4). */
  applyIceServers(servers) {
    this.iceServers = servers;
    for (const mp of this.peers.values()) mp.applyIceServers(servers);
  }

  /** close tears down every mesh connection. */
  close() {
    for (const [id, mp] of this.peers) {
      mp.close();
      this.onUntrack(id); // each connection stops counting toward connectivity (D-38), order-independent
    }
    this.peers.clear();
    this._streams.clear();
  }
}
