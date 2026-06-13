import "../styles/tokens.css";
import { mountDeviceCheck } from "./devicecheck.js";

// The app bundle is a small dispatcher: it mounts whichever island the server-rendered
// page asked for by its root element. The device-check island lands here in M2; the
// guest-session / greenroom islands join it in M3.
const deviceCheck = document.getElementById("device-check");
if (deviceCheck) mountDeviceCheck(deviceCheck);
