import "../styles/tokens.css";
import { mountDeviceCheck } from "./devicecheck.js";
import { mountGreenroom } from "./greenroom.js";

// The app bundle is a small dispatcher: it mounts whichever island the server-rendered page
// asked for by its root element. The guest device-check/publish island and the host greenroom
// grid each mount on their own root; moderation controls + the guest-session surface layer on
// in later M3 PRs.
const deviceCheck = document.getElementById("device-check");
if (deviceCheck) mountDeviceCheck(deviceCheck);

const greenroom = document.getElementById("greenroom");
if (greenroom) mountGreenroom(greenroom);
