// SPIKE-2 host control harness: issues slot-rebind / unbind for cam-1 and pokes the
// OBS source-active reflection. Stands in for the greenroom host People tab (M3).
import { Room } from "../rtc/room.js";

const session = new URLSearchParams(location.search).get("session") || "s1";
const room = new Room({ session, peer: "host", role: "host" });

/** @param {string} occ */
window.rebind = (occ) => room.send({ t: "rebind", slot: "cam-1", occupantPeerId: occ });
window.unbind = () => room.send({ t: "unbind", slot: "cam-1" });

const el = document.getElementById("status");
if (el) el.textContent = "host control ready";
