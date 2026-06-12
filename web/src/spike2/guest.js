// SPIKE-2 guest harness: publishes a CANVAS stream (no camera needed) so the
// slot-rebind renegotiation can be exercised deterministically. It answers a
// recvonly offer from whichever OBS source page the server connects it to, adding
// its canvas track. The real guest device-check + getUserMedia island is M2.
import { Room } from "../rtc/room.js";

const q = new URLSearchParams(location.search);
const session = q.get("session") || "s1";
const peer = q.get("peer") || "g1";

const canvas = /** @type {HTMLCanvasElement} */ (document.getElementById("canvas"));
const ctx = canvas.getContext("2d");
const colors = { g1: "#b8e03a", g2: "#e8402e" };
let t = 0;
function draw() {
  t++;
  ctx.fillStyle = colors[peer] || "#888";
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  ctx.fillStyle = "#1d2218";
  ctx.font = "64px sans-serif";
  ctx.fillText(peer, 24, 100);
  ctx.fillRect((t * 3) % canvas.width, 130, 24, 24); // motion so frames keep flowing
  requestAnimationFrame(draw);
}
requestAnimationFrame(draw);
const stream = canvas.captureStream(30);

const room = new Room({ session, peer, role: "guest" });
/** @type {Record<string, RTCPeerConnection>} */
const pcs = {};

room.on("signal", async (f) => {
  let pc = pcs[f.from];
  if (!pc) {
    pc = new RTCPeerConnection();
    pc._pendingIce = []; // ICE that arrived before the remote description
    pcs[f.from] = pc;
    pc.onicecandidate = (e) => {
      if (e.candidate) room.send({ t: "signal", to: f.from, ice: e.candidate.toJSON() });
    };
  }
  if (f.sdp) {
    await pc.setRemoteDescription(f.sdp);
    if (f.sdp.type === "offer") {
      for (const tr of stream.getTracks()) {
        if (!pc.getSenders().some((s) => s.track === tr)) pc.addTrack(tr, stream);
      }
      const ans = await pc.createAnswer();
      await pc.setLocalDescription(ans);
      room.send({ t: "signal", to: f.from, sdp: pc.localDescription });
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
});

// The occupant is the canonical recipient of its own on-air reflection (D-24).
const el = document.getElementById("status");
let onair = "—";
function showStatus() {
  if (el) el.textContent = `guest ${peer} publishing · on-air: ${onair}`;
}
room.on("onair", (f) => {
  onair = f.onAir;
  showStatus();
});
showStatus();
