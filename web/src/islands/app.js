import "../styles/tokens.css";
import { mountDeviceCheck } from "./devicecheck.js";
import { mountHostMonitor } from "./hostmonitor.js";

// The app bundle is a small dispatcher: it mounts whichever island the server-rendered page
// asked for by its root element. M2 ships the guest device-check/publish island and the thin
// host-monitor tile; the fuller greenroom islands join them in M3.
const deviceCheck = document.getElementById("device-check");
if (deviceCheck) mountDeviceCheck(deviceCheck);

const hostMonitor = document.getElementById("host-monitor");
if (hostMonitor) mountHostMonitor(hostMonitor);
