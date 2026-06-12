// OBS cam source page (EN-13): a deliberately minimal, font-free bundle. It auths
// with a slot token alone (query param in the spike; real ?src token in M2), and the
// server resolves slot -> current occupant at signal time (EN-1). On {t:slot-rebind}
// it tears down the old PeerLink and renegotiates to the new occupant, resetting
// on-air to status-unavailable until a fresh obsSourceActiveChanged arrives (EN-3).
import { Room } from "../rtc/room.js";
import { PeerLink } from "../rtc/peerlink.js";

const q = new URLSearchParams(location.search);
const session = q.get("session") || "s1";
const peer = q.get("peer") || "src";
const slot = q.get("slot") || "cam-1";

const video = /** @type {HTMLVideoElement} */ (document.getElementById("video"));
const status = document.getElementById("status");

/** @type {PeerLink|null} */ let link = null;
let occupant = null;
let epoch = 0;
let onAir = "status-unavailable";

function render() {
  const pc = link ? link.pc.connectionState : "none";
  status.textContent =
    `occupant: ${occupant ?? "—"} · epoch: ${epoch} · on-air: ${onAir} ` +
    `· pc: ${pc} · video: ${video.videoWidth || 0}x${video.videoHeight || 0}`;
}

const room = new Room({ session, peer, role: "obs", slot });

function bind(id, ep) {
  if (link) {
    link.close();
    link = null;
  }
  occupant = id;
  epoch = ep;
  onAir = "status-unavailable"; // EN-3: reset until a fresh sourceActive at this epoch
  video.srcObject = null;
  render();
  if (!id) return;

  link = new PeerLink(room, id);
  link.pc.ontrack = (e) => {
    video.srcObject = e.streams[0];
    render();
  };
  link.pc.onconnectionstatechange = render;
  link.offer();
}

room.on("slot-rebind", (f) => bind(f.occupantPeerId, f.epoch));
room.on("slot-unbound", (f) => bind(null, f.epoch));
room.on("signal", (f) => {
  if (link && f.from === occupant) link.onSignal(f);
});
room.on("onair", (f) => {
  onAir = f.onAir;
  render();
});

// Stand-in for the window.obsstudio obsSourceActiveChanged relay (real wiring in M2).
// Echoes the CURRENT epoch so the server can reject stale events (EN-3).
window.reportSourceActive = (active) =>
  room.send({ t: "obs", event: "sourceActive", active, epoch });

video.onloadedmetadata = render;
setInterval(render, 400);
render();
