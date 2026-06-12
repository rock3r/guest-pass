// OBS source-page entry (EN-13): a DELIBERATELY separate, minimal bundle — no
// @font-face, no Preact, minimal JS — so the ~412 KB font payload and the full app
// stay out of up to 9 browser sources per show. The real PeerLink + window.obsstudio
// relay + reconnect land in M2; this SPIKE-1 placeholder only proves the second,
// font-free entry builds independently of the app bundle.
const el = document.getElementById("obs");
if (el) el.textContent = "obs source ready";
