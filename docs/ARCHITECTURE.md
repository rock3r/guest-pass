# GuestPass — Architecture

> Free, open-source guest management for live streams. Guests click a magic link
> and appear in OBS as individual browser sources — no signup, no install. All
> media is end-to-end WebRTC (P2P mesh) the server never sees; a deliberately thin
> Go backend stores only identities, invites, and ephemeral session metadata.

This is the committed, standalone architecture reference for the v1 build. It
distills the approved system design and the implementation-architecture decisions
into one document. Decision IDs are cited inline — `(D-n)` system design,
`(EN-n)` engineering invariant, `(AD-n)` implementation-architecture decision — so
a choice can be traced to its rationale. The companion docs carry operational
detail: `CONVENTIONS.md` (Go/frontend/DB style, file placement), `TESTING.md`
(test layering, TDD, CI gates), `DEPLOYMENT.md` (self-hosting, config, TURN,
migrations).

The backend is **npm/node-free** — no `node`, no `package.json`, no registry
access at build time. The whole project builds with the Go toolchain alone. This
is LOCKED.

---

## 1. Goals & non-goals

### Goals

- **Guests do almost nothing.** Open emailed magic link → allow camera → join the
  greenroom. Zero accounts, zero installs. Their PII is auto-deleted within 24h of
  stream end (D-37).
- **Hosts get real OBS sources, wired once.** A stable browser-source URL per
  **slot** (cam slots 1–8, an optional host slot, one shared screenshare slot).
  The host pastes these into OBS one time and composites there every stream;
  GuestPass never builds a grid (D-20).
- **OBS owns the show.** GuestPass has no staging / "bring on stream" control. The
  host brings a guest on by *showing that guest's source in OBS*; "backstage is
  sacred" is enforced by the host simply not showing the source (D-11). GuestPass
  *reflects* who OBS has on-program and whether the broadcast is live —
  read-only, never authoritative (D-24).
- **Thin backend, media-blind by architecture.** Stores host Google identities,
  streams, passes, and ephemeral session metadata; relays small JSON signaling
  frames. It **never touches media** — no media server, ever (D-23).
- **Runs as a free public instance *and* self-hosts.** The official multi-tenant
  instance lives at **guest-pass.link** (free, no ads); the same binary self-hosts
  via docker-compose (D-35). License is **AGPL-3.0** (D-31).
- **Absolute-minimum data.** Host Google identity + guest name/email for invites +
  transient session metadata. Never media, never chat, no third-party trackers.
  Minimal *data*, not just minimal PII, is the backstop ethos (D-37).

### Non-goals (v1)

- **No GuestPass-side compositing, staging, scenes, or "bring on stream"** — OBS
  does composition (D-11). No grid, no mixing, no RTMP restreaming, no recording,
  no public chat platform.
- **No media server, ever** — no SFU/relay mode, no WHIP/WHEP ingest (both need a
  media server, violating D-23). The browser source *is* the WebRTC P2P path into
  OBS. An SFU could only ever return as an off-by-default self-hoster opt-in, never
  the default or public path.
- **No operator-run TURN by default** — STUN only; TURN is opt-in BYO (D-38). The
  ~8–15% behind symmetric NAT/firewall get a clear "your network blocks
  peer-to-peer" error, never a silent hang.
- **No guest accounts, ever.** No guest self-service data deletion (auto-purge at
  24h makes it moot; guests are told before and after — D-37).
- No obs-websocket bridge (browser-source reflection only; bridge is future work).
- No viewer counts / platform analytics, no public viewer share page, no
  guest-book / cross-stream guest reuse (→ v1.1).
- Mobile is "guest flow works on a phone," not optimized.

---

## 2. System architecture

GuestPass is a **single Go binary** (signaling + SQLite + embedded frontend & OBS
source pages) plus optional STUN/TURN. **All media is P2P WebRTC mesh** between
browsers; the server is never in the media path. STUN helps peers connect directly
(~85–90% of links); TURN, when a host brings one, relays only the blocked minority
(encrypted, never decoded — D-23/D-38).

```
┌──────────────────────────────  Host's machine  ───────────────────────────────┐
│                                                                                │
│  Host browser (greenroom)            OBS  (owns composition — D-11)            │
│  ┌──────────────────────┐            ┌──────────────────────────────────────┐  │
│  │ monitoring UI         │           │ Browser source: /s/cam-1?token=…     │  │
│  │ host cam/mic publish  │           │ Browser source: /s/cam-2?token=…     │  │
│  │ reflects OBS on-air   │           │ Browser source: /s/screen?token=…    │  │
│  │ N peer connections    │           │ (host shows/hides/composites here)   │  │
│  └─────────┬─────────────┘           └─────────────────┬────────────────────┘  │
│            │                                            │                       │
└────────────┼────────────────────────────────────────────┼──────────────────────┘
             │  WS signaling                       ▲       │  WS signaling
             │  + WebRTC media (P2P mesh)          │ P2P   │  + WebRTC media (P2P)
             ▼                                     │media  ▼
   ┌──────────────────────────┐                    │   ┌────────────────┐
   │   GuestPass server (Go)  │                    └───┤ Guest browser   │
   │  - Go-rendered HTML       │◄──────signaling────────┤  cam/mic/screen │
   │    + Preact islands       │                        └────────────────┘
   │  - WS signaling relay      │
   │  - slot source pages       │      ┌──────────┐  STUN: direct-connect helper
   │  - SQLite (no media path)  │◄────►│  STUN    │  (self-hosted, always on)
   └──────────────────────────┘      └──────────┘
                                       ┌──────────┐  TURN: OPTIONAL, BYO only —
                                       │  TURN    │  relays the ~8–15% P2P-blocked
                                       │ (BYO,    │  links; encrypted packets it
                                       │ off by   │  cannot decode. NOT run by the
                                       │ default) │  public instance (D-38).
                                       └──────────┘
```

**Mesh detail.** Every consumer of a guest's media is a *direct* peer connection
from that guest's browser — the host greenroom (monitor), every other backstage
participant (everyone-sees-everyone thumbnails, D-10/D-33), and each OBS source
page (program feed). The server only relays SDP/ICE and room state.

### Components

| Component | Role |
|---|---|
| **GuestPass server** (single Go binary) | Serves Go-rendered HTML + vendored Preact islands (D-32) and the chromeless OBS slot source pages; WebSocket signaling relay; magic-link email; SQLite storage. Resolves **slot → current occupant** at signal time (EN-1). **No media path** (D-23). |
| **STUN** (self-hosted, always on) | NAT-traversal helper only: peers discover their public IP/port and connect **directly**. No media relay; cheap; ships with v1 (D-38). |
| **TURN** (optional, BYO) | Full media **relay** for P2P-blocked links (symmetric NAT / restrictive firewall). **Off by default**; a host/operator brings their own at *their* bandwidth cost, fed to ICE via config. The public instance runs none (D-38). Relays encrypted DTLS-SRTP it cannot decode — the only sanctioned media-touches-infra path (D-23). |
| **Browsers** | All media capture, encoding, rendering. Host greenroom, guest session page, and each OBS slot source page are all WebRTC mesh peers. |

### Deployment shapes (D-35)

| Shape | What it is | Media cost | TURN |
|---|---|---|---|
| **Public instance** — `guest-pass.link` | Official, free, no ads, **multi-tenant**: any Google-verified user signs in and hosts (progressive-trust quotas, D-36). Cost ≈ signaling + SQLite only. | ~zero (P2P mesh; no operator relay) | None by default; a host may BYO (D-38) |
| **Self-hosted** | Same single binary + docker-compose, **AGPL** (D-31). Operator picks `SIGNUP_MODE` (open / approval / allowlist). | ~zero unless the operator opts into TURN | Operator/host BYO (bundled coturn is a config-flip) |

Both shapes are media-blind by construction (D-23) and run the identical signaling
+ storage path; they differ only in tenancy, signup mode, and whether a TURN relay
is configured. There is no fork divergence — the public instance is just GuestPass
with `SIGNUP_MODE=open` and the abuse dials turned up. (See `DEPLOYMENT.md` for
config and topology.)

---

## 3. Media topology — P2P mesh

GuestPass is a **full WebRTC mesh**: every consumer of a guest's media is a direct
`RTCPeerConnection` from that guest's browser. **No media server, ever** (D-23) —
nothing on the server decodes, forwards, transcodes, or composites a stream. The
mesh is the permanent architecture; the ~6-peer cap is its accepted, permanent
price.

For one guest, the consumers (peers) are:

1. The **OBS cam source** (slot) — the program feed (D-20).
2. The **host's greenroom monitor** — the host judges framing/quality.
3. **Every other backstage participant** (host, co-hosts, other guests) —
   everyone-sees-everyone backstage stays the default (D-10).
4. The **shared screenshare slot**, only while this guest is the live sharer (D-21).

### NAT traversal: STUN-only v1, optional BYO-TURN (D-38)

| Path | v1 default | Notes |
|---|---|---|
| Direct P2P | **always** | Self-hosted STUN for public-IP/port discovery; ~85–90% of pairs connect directly. |
| Operator-run TURN relay | **none by default** | The public instance ships no media relay — keeps "cheap" true and the no-operator-relay-metadata posture honest. |
| BYO-TURN | opt-in | GuestPass accepts a TURN config (`TURN_URL` + `TURN_SECRET`) at host and/or operator level and feeds it to ICE. Must be **publicly reachable** (UPnP / port-forward / public IP). |

**Known limitation + required UX:** with no TURN, the ~8–15% behind symmetric NAT /
restrictive firewalls cannot connect. Surface a clear **"your network blocks
peer-to-peer"** guest error (suggest a different network / hotspot) — never a
silent hang. TURN is a NAT-traversal *packet* relay (encrypted DTLS-SRTP forwarded
without inspection), so it is D-23-safe — not "media through the server."

### Codecs (D-39)

| Track | v1 | Fallback / opt-in |
|---|---|---|
| Video | **H.264, HW-accelerated, constrained-baseline preferred** (interop; HW encode offloads the binding CPU constraint; OBS CEF + universal device compat) | **VP8 software** where no H.264 HW encoder exists. |
| Audio | **Opus** | — |
| Higher-efficiency video | — | **Instance-level opt-in via `CODEC_OPTIN`, OFF by default:** VP9 / AV1 / H.265. Negotiated only where both peers support it. Tradeoff: better compression vs higher encode CPU/battery borne by guests' devices, often no HW encode, uneven WebRTC support. |

Per-stream/per-host codec control → v1.1. The degradation ladder below governs
regardless of codec.

### The real cap: encoder count, not bandwidth (D-33)

The honest hard cap is **~6 (4 recommended), explicitly hardware-dependent** —
documented as "up to ~6 on capable machines, fewer on weak ones; depends on the
guest's CPU and encoders," **never a fixed promise**. The binding constraint is
**per-guest live encoder count + host aggregation**, not raw bandwidth.

Per-guest encoder budget (one camera → N distinct encodes):

```
encoders = program (OBS slot)
         + host grid tile
         + (N-1) backstage thumbnails   ← the amplifier (everyone-sees-everyone, D-10)
         [+ screenshare, when live sharer]
```

At 6 participants that is **program + monitor + 5 thumbnails ≈ 7+ live encoders**
for one camera, before screenshare. NVENC caps at ~2–3 concurrent sessions and
software encoders thrash beyond a handful — which is why the cap is reframed around
encoders.

> **Why per-link distinct bitrate tiers DEFEAT encoder sharing:** each
> distinct-bitrate tier is a distinct encode — distinct tiers force the browser to
> run a *separate* encoder per link instead of reusing one. So the mitigation is
> **not** per-link tiers; it is the priority degradation ladder.

### Degradation ladder — per-publisher-local shedding (AD-21)

The degradation model is **per-publisher-local** (AD-21) — the only mesh-coherent
design. **Each browser caps its own outbound senders** via
`RTCRtpSender.setParameters()`, applying a **host-set shared priority order**. The
host configures the priority and sees full transparency, but **no host controls
another guest's encoders directly**; control over outbound is always local to the
publisher.

> **PROVISIONAL pending SPIKE-0 (RF-1 / AD-24).** Browsers expose no direct
> encoder-count control, and lowering a sender's bitrate/scale via `setParameters()`
> may *not* actually free a hardware (NVENC) encoder session. The model is the only
> mesh-coherent one, but the *mechanism* — and therefore the ~6 cap — is unproven
> until the real-hardware encoder probe runs. Treat the numbers as estimates until then.

| Priority | Stream | Treatment |
|---|---|---|
| 1 (protect) | **OBS program feed** (broadcast) | Degraded only as last resort, **fps before res**, with a host warning. |
| 2 | **Host monitor** | |
| 3 | **Co-host thumbnails** | |
| 4 (shed first) | **Other-guest thumbnails** | Degraded / shed first. |

The ranking is a **host-redistributable default**, not fixed. Screenshare default
is `screenshare > guests` (D-34): when a share is active, guests are usually shown
small, so the screenshare program feed defaults *above* guest cam feeds;
host-overridable.

Driven by WebRTC `getStats()` **`qualityLimitationReason`** — the principled signal,
since browsers expose no raw CPU%:

- **`cpu` → shed the lowest-priority *encoders* first** (drop thumbnail encodes
  first, working up the ladder).
- **`bandwidth` → lower quality on the *constrained link only*** —
  **res → fps → bitrate**, lowest-priority link first.
- Plus framerate / bitrate / packet-loss signals.

**Hysteresis:** degrade fast, recover slow, no flapping. **Manual "bump up quality
now"** (D-34): the host broadcasts a "recover now" signal that forces an immediate
recovery attempt across publishers, overriding slow hysteresis. **Full
transparency** (D-34): the host sees every stream's degradation (which stream, how
much, direction lowering vs recovering); each guest sees only their own (e.g.
"trimming background video to protect your stream").

### Host aggregation chokepoint

The host machine is a bottleneck: its **downlink carries N×program + N×monitor**
into local OBS / greenroom, while its **uplink carries broadcast egress** — both on
one (often asymmetric) connection.

**Guard (folds D-18):** the **optional host source(s) must NOT run on a
single-uplink host.** Routing the host's own cam through GuestPass to an OBS
browser source (the Windows single-webcam-exclusivity fix) fans the host cam out to
every guest on the *same* uplink that carries broadcast egress — actively harmful.
Two-uplink / native-OBS hosts only. Webcam exclusivity is driver/device-specific,
not universal (EN-19).

### Connection resilience

- **ICE restart first, then full renegotiation** over the auto-reconnecting
  (exponential-backoff) signaling WS.
- Peers are keyed by **stable `pass_id`**, so an OBS slot source reattaches to the
  same occupant automatically — the host never edits OBS mid-show (EN-3). The room
  persists through host disconnects; the host auto-reconnects and resumes (D-40).
- On slot rebind, on-air state resets to `status-unavailable` until a fresh
  `obsSourceActiveChanged` transition arrives (EN-3).

There is **no pre-flight speed test** (D-12) — all health is live `getStats()`.

---

## 4. OBS integration

OBS owns composition — GuestPass provides a fixed set of always-available browser
sources; the host shows/hides/composites them in OBS (Guest Star model, D-11). Each
browser source page **is itself a WebRTC P2P peer** that pulls a guest's media
directly into OBS's CEF; no media transits the server (D-23). Live state is
**reflected-only, never authoritative** (D-11/D-24). Floor: **OBS ≥ 31** (CEF 127 —
`color-mix()`/`backdrop-filter` work). vMix / hardware / native-WebRTC ingest paths
are not supported (stock OBS has no WHEP source; D-23).

### Slot model (D-20)

OBS URLs are keyed by **slot, not pass**, so the host wires OBS **once** and reuses
the same URLs every stream. The slot→occupant binding is per-pass, assigned at
invite and **reassignable live** in the greenroom.

**Host-global slot pool** (one per host; tokens are permanent, host-only, stored
hashed):

| Slot kind | Count | Source page |
|---|---|---|
| Cam slots | 1–8 (addressable pool; ~6 concurrent cap, D-33) | `GET /s/{slot}?token={slotToken}` |
| Host slot | 0–1 (optional, D-18) | `GET /s/host?token={slotToken}` |
| Screenshare slot | exactly 1 (shared, D-21) | `GET /s/screen?token={slotToken}` |

- **`?token` (page) vs `?src` (WS):** the slot source PAGE is fetched with
  `?token={slotToken}`; that page then opens `/ws?src={slotToken}` for signaling —
  the **same secret over two transports**, never a second token.
- **Slot-id grammar (RF-26):** ids are **`cam-1`…`cam-8` | `host` | `screen`**; the OBS
  URL is `GET /s/{slotId}?token={slotToken}` and the same ids appear in `slot-rebind`
  frames and the `slots` table.
- **Token boundary:** slot source tokens are **distinct from pass tokens and
  host-only** — guests never see slot URLs (EN-8). Source-page DOM/JS carries
  **zero secrets**: the opaque slot id only (EN-15). Panic-rotate all slot tokens
  via the host leak button (D-22). In v1 the slot token authorizes the media leg
  directly (per-session grant → v1.1, AD-23); source pages send `Referrer-Policy:
  no-referrer` and make no third-party requests so the URL token can't leak (RF-24).
- **Constraint:** one live session per host at a time (v1) so slot URLs resolve
  unambiguously (EN-2). Concurrent shows/layouts → v1.1.
- **Sources tab** is a **read-only "wire OBS once" reference** (EN-26): per-slot
  cards = slot + permanent URL + copy + current occupant + on-air, plus copy-all
  and the per-slot/global **regenerate** panic action (D-22). It has **no editable
  controls** — slot→guest **binding**, the per-slot **program-resolution override**
  (D-19) + nameplate, and the stream-wide **quality ceiling** all live in the
  greenroom's **host-only People tab** (EN-23/EN-26). Co-hosts never see slot URLs
  or quality controls (D-15).

### Slot-rebind protocol + slot epoch (EN-3)

The source page authenticates with the **slot token only**; the occupant is
resolved server-side at signal time (EN-1), so live reassignment lights the right
tile. Each slot carries a monotonic **epoch**.

```jsonc
// server → slot source page
{"t":"slot-rebind","slot":"cam-3","occupantPeerId":"<id>","epoch":42}
{"t":"slot-unbound","slot":"cam-3","epoch":43}            // → transparent placeholder
```

- On `slot-rebind`: close the old PeerLink, renegotiate to the new occupant, stop
  the old encoder. **Reset on-air to `status-unavailable`** until a fresh
  `obsSourceActiveChanged` arrives — the event fires only on **transitions**, so a
  stale `active=true` would otherwise mislight the new occupant.
- On kick / force-end (D-25): **atomically clear the binding + bump epoch BEFORE
  the teardown broadcast**, so a reconnecting modified source resolves to
  placeholder, not the kicked occupant. The cooperative source drops the target
  instantly → off-broadcast.
- Cam slot with no occupant or screenshare slot with no live share → transparent
  "waiting" placeholder; the cam source never switches away from camera.

### Screenshare = single shared slot + host preview-switcher (D-21)

One constant screenshare OBS URL (`/s/screen`). `can_screen` gates eligibility;
**video-only in v1** (guest voice stays on the cam source; D-41).

- **Multiple eligible guests may share simultaneously.** Each active share flows to
  a **host-only preview rail** (thumbnails, low-bitrate). The host
  **selects/swaps which active share is *live*** in the slot (preview→program
  switch in the web UI).
- **`screen-select` is host-only** — the one sanctioned exception to "OBS owns
  composition" (D-11), since OBS can't pick a shared source's occupant. Co-hosts
  may **force-no-share** a guest (moderation, D-13) but never select-live.
- The **live** share renders in the greenroom for everyone + on the OBS screenshare
  source; non-selected shares appear only in the host's preview rail. A sharing
  guest sees its own state: **active-backstage (preview)** vs **active-live (in
  slot)**.
- Revoke/force-no-share pulls a guest from the pool (and the slot if live → slot
  falls to placeholder, **no auto-advance**).

### Three-state on-air (D-24)

Two distinct affordances, never conflated. Applies to the guest self-view and
host/co-host per-tile. No page-permission setup is needed — `window.obsstudio`
events fire at the default level.

1. **Force-lock notice** ("muted/hidden by host") — **authoritative**, always shown
   when a suppressive lock is active (D-13). Separate from the pill.
2. **On-air pill** — reflected from OBS, three values:

| Pill state | Driven by | Meaning |
|---|---|---|
| **on-air** | `obsSourceActiveChanged.active === true` | source is in an active scene (program) |
| **not-on-air** | source connected, not in active scene | wired, not live |
| **status-unavailable** | no OBS signal at all | show "on-air status unavailable" + hint to verify on the actual stream |

- Detection signal is **`active`** (`obsSourceActiveChanged`), **NOT `visible`** —
  `obsSourceVisibleChanged` also fires in preview and would false-positive.
- Broadcast-level "we're live" from `obsStreamingStarted` / `obsStreamingStopped`,
  relayed over the source page's signaling connection.
- **Never assert "backstage" as truth when the real state is unknown** — degrade to
  `status-unavailable`.

**Live verification (D-29).** The host optionally links a Twitch/YouTube channel;
GuestPass best-effort scrapes public web endpoints server-side, zero-config, to feed
the broadcast layer with a `live (verified on <platform>)` signal. SSRF-closed
(channel identifier + platform, never a raw URL; fixed template; block
private/loopback/link-local/metadata IPs; off-domain redirects refused). Degrades to
`status-unavailable` on failure. Optional API-key verification → v1.1.

### Nameplate (D-16)

The **guest owns the name string** (default = host's invite name); the host can
override it in the People panel and the override is **sticky**. Whether it renders
in OBS is a *separate, host-only* concern:

- **Visibility:** per-source **show/hide URL param** (no DB column).
- **Styling:** OBS's native **Custom CSS** against documented selectors GuestPass
  exposes (no GuestPass styling control).
- **Injection-safe (EN-15):** the display name renders as **escaped `textContent`
  only**; server-side charset/length cap. No promise that viewers see the name — it
  depends on the host's OBS setup.

---

## 5. Backend code architecture

The server is a single static Go binary. Its real-time job is purely relaying small
JSON signaling frames and projecting room state — it does **no media processing**
(no pion, no codecs, no SFU; D-23).

### Stack

| Concern | Choice | Why / contract |
|---|---|---|
| Language | **Go 1.26+** (AD-15) | Toolchain floor (a minimum, not a pinned version), tracking current deps (raised to 1.25 for `modernc.org/sqlite`, then 1.26 for `chromedp`); single static binary. Module path `github.com/rock3r/guest-pass`. |
| Router | `go-chi/chi` | Idiomatic, tiny, stdlib-shaped middleware. |
| WebSockets | **`coder/websocket`** (AD-16), wrapped behind `internal/signaling/conn` as an in-process **test seam** | Actively maintained, context-first, ergonomic. Its self-serialization is *redundant* with our single-writer `writeLoop` (EN-12), not extra safety (RF-19). |
| DB | **SQLite via `modernc.org/sqlite`** (pure Go, no CGO) | Embeds cleanly. Concurrency contract (EN-11): `journal_mode=WAL`, `busy_timeout>=5000`, `foreign_keys=ON` applied **via a connection hook** (every pooled conn); a **writer pool `SetMaxOpenConns(1)` + a separate reader pool** (WAL concurrent readers) — decided, not a hedge (RF-11). **Never persist per-frame stats** — `peers.used_turn` written once at disconnect. |
| Auth | `golang.org/x/oauth2` (Google) + JWT cookie | See JWT contract below (EN-6). Google-only sign-in. |
| Email | **Resend HTTP API** (D-2) + `MAIL_MODE=log` | One POST, no SMTP; `log` mode prints links to stdout for dev. Consumes Resend delivery webhooks for real mail-health signal (EN-22). |
| NAT traversal | **STUN-only by default** + optional BYO-TURN (D-38) | Operator/host may supply a TURN URL+secret fed to ICE; ephemeral HMAC creds, 60–120s TTL so kicks revoke (EN-4). |

**JWT contract (EN-6).** The JWT carries **`host_id` only** — no roles, no status
baked in. Every protected handler reads **`hosts.status` and `hosts.is_admin` LIVE
from the DB** on each request, so a suspend (D-27), a still-`pending` account
(D-28), or an admin flip takes effect mid-session without re-issuing tokens. WS
host-join/rejoin gates the same way (`status=active`). The signing key uses a
**`kid` header + two-key ring** so `JWT_SECRET` rotation is a key-add, not a global
logout. The binary **fails closed** if `JWT_SECRET`/`TURN_SECRET` is empty or equals
a shipped placeholder (EN-14). An `AUTH_MODE=dev` seam (AD-8) mints a fake host
session without Google for local dev + hermetic tests; it is gated behind a
**`//go:build dev` build tag** so it is **not compiled into release binaries** at all
(see `CONVENTIONS.md` §1.5) and additionally refuses a non-loopback `BASE_URL` (RF-4).

### Package tree (AD-4)

Pragmatic `internal/` packages — the server is thin and infra is hard-locked, so
hexagonal ports would be dead weight.

```
cmd/guestpass/        main: wire config→store→hub→web; serve :443 HTTP+WS
cmd/build/            esbuild Go API → web/dist  (go run ./cmd/build [--watch])
internal/
  config/             env load + fail-closed secrets (EN-14, AUTH_MODE/MAIL_MODE)
  store/              modernc.org/sqlite; go:embed *.sql migrations (AD-6);
                      WAL+busy_timeout+FK conn hook + single-writer pool (EN-11)
  auth/               Google OAuth + JWT (kid two-key ring), live-DB authz mw (EN-6), AUTH_MODE=dev
  mail/               Resend HTTP + MAIL_MODE=log; delivery-webhook intake (EN-22)
  turn/               ICE config assembly; ephemeral HMAC TURN creds 60–120s (EN-4); STUN default (D-38)
  signaling/          THE CORE (AD-2): hub + room + conn + roster/locks/slots/epochs/frames
    hub.go            room registry, conn routing, cross-room queries, one-live-session (AD-2a)
    room.go           per-room goroutine; single cmd-channel select loop; authoritative state (AD-3)
    conn.go           per-conn readLoop(→room cmds) + writeLoop(←sendCh) — one-writer-per-conn (EN-12)
    frames.go roster.go locks.go slots.go   + *_test.go pure-Go room TDD
  web/                http handlers, html/template render, route table, CSP/SRI/cookies, source pages
  assets/             shared esbuild build config (BuildOptions/Build + SRI manifest), used by cmd/build + browsertest
  browsertest/        //go:build browser — chromedp + fake-media harness (AD-9); islands/OBS/tracer tests
  livecheck/          D-29 scraping, SSRF-closed
  jobs/               24h PII purge + idle-session reaper tickers (D-37/D-40)
web/
  src/rtc/            PeerLink (consume), Publisher (publish), Room (signaling WS + ICE
                      config from the join-ack + {t:ice-refresh}), getStats sampling
  src/islands/        device-check(+publish; renders guest-session in-session), greenroom grid,
                      guest-session (in-session surface: self-view, chat, raise-hand, on-air, locks)
  src/obs/            cam + screen source pages (separate minimal entry — no fonts, EN-13)
  src/styles/         tokens verbatim from styles-v2.css (D-9)
  vendor/preact/      vendored MIT (D-32)   ·   fonts/  OFL woff2 ×3 (EN-17)
  dist/               build output — gitignored, go:embed at release (AD-7)
docs/                 ARCHITECTURE/CONVENTIONS/TESTING/DEPLOYMENT
```

### Actor / hub-per-room model (AD-2)

The realtime core is an **actor / hub-per-room** model: one goroutine owns each
room's state; all mutations go through a command channel; there are **no locks on
room state**. This makes *room-state mutations* race-free, makes one-writer-per-conn
(EN-12) structural, and makes the lock/epoch/roster logic linear and unit-testable.
**Boundary seams still need care (RF-27)** — DB admission, hub-map lifecycle, snapshot
publication, reconnect/eviction, token rotation — and get explicit boundary-race tests.

- A **supervisor/hub** above the room actors (AD-2a) owns the room map, spawns/reaps
  room goroutines, routes inbound conns to rooms, and answers cross-room queries
  (admin force-end / stats EN-2) by messaging rooms — never touching their private
  state. The **one-live-session admission check is a serialized hub command** (the hub
  owns the room map single-threadedly), *not* a snapshot read (RF-2); read-only
  snapshots are reserved for staleness-tolerant admin stats. (DB also enforces it via a
  partial unique index — belt and suspenders, RF-2.)
- **Live state is in-memory only** (AD-3); SQLite holds durable entities
  (hosts/streams/passes/slots) + a thin sessions/peers audit at lifecycle edges. A
  restart drops live rooms; peers reconnect via stable `pass_id` and the room
  rebuilds (D-40).

**Goroutine topology** — this makes one-writer-per-conn (EN-12) structural, not a
discipline a reviewer must police:

```
        ┌─────────────────────────────────────────────┐
        │ Hub (1 goroutine)                            │
        │  owns map[sessionID]*roomHandle              │
        │  routes new conns; cross-room ops as commands│
        └───────────────┬─────────────────────────────┘
                        │ spawns 1 per live session
                        ▼
        ┌─────────────────────────────────────────────┐
        │ Room (1 goroutine / session)                 │
        │  single select over cmdCh                    │
        │  owns ALL live state (roster, locks, slots,  │
        │  epochs); emits frames by pushing to each    │
        │  target conn's sendCh — never writes a socket│
        └───────────────┬─────────────────────────────┘
                        │ 2 goroutines per connection
            ┌───────────┴───────────┐
            ▼                       ▼
   ┌──────────────────┐   ┌──────────────────────┐
   │ readLoop         │   │ writeLoop            │
   │ parse frame →    │   │ drain sendCh →       │
   │ typed cmd →      │   │ one WriteMessage     │
   │ room.cmdCh       │   │ (THE single writer)  │
   └──────────────────┘   └──────────────────────┘
```

- **Backpressure = drop slow peers (AD-12).** `sendCh` is buffered (~64); on full
  during a room push, treat the peer as dead, drop it + broadcast `peer-left`; it
  reconnects via `pass_id`. The room never blocks on a socket. **Frames carry a
  delivery class (RF-16):** control/terminal frames (`terminate`, `slot-rebind`,
  `token-rotated`, SDP/ICE) are not silently dropped — on overflow the room makes a
  best-effort final `terminate` write, then closes, and emits `peer-left` only **after**
  teardown is committed (deterministic ordering, no "left before terminate" races).
- **Audio-level coalescing (AD-13).** `{t:state,level}` updates stay in-memory;
  meters ride a separate lightweight batched frame on a ~6–8 Hz room tick. Full
  `roster` / `peer-*` frames go out only on structural change (join/leave/lock/
  role/on-air/slot), avoiding N² roster spam at the cap.
- **ICE config over the WS join-ack (AD-14)**, not REST: STUN always; a TURN entry
  with a freshly-minted ephemeral HMAC cred + `ttlSec` when configured; the client
  sends `{t:ice-refresh}` before expiry. Keeps creds on the revocable WS so a kick
  revokes within TTL (EN-4).

---

## 6. Data model + schema

### Logical model

```
hosts     id, google_sub, email, name, picture, is_admin,
          status(pending|active|suspended),                 -- D-28
          created_at

streams   id, host_id, title, scheduled_at, duration_min,    -- NO repeat_rule (recurring → v1.1)
          status(draft|scheduled|live|ended),
          max_res, max_fps, max_bitrate_kbps,                -- stream-wide quality ceiling (D-19)
          twitch_yt_channel?, twitch_yt_platform?,           -- optional linked-channel live-verify (D-29)
          created_at

passes    id, stream_id, slot_id,                            -- per-pass slot binding → slots(id) (D-20, RF-2)
          name, email,                                       -- guest PII; purged 24h post-stream (D-37)
          role(guest|cohost),
          token_hash,                                        -- magic-link token, HMAC(secret,token) (EN-5)
          can_screen,
          status(created|sent|opened|accepted|expired|revoked), -- NO 'joined'
          sent_at, expires_at, opened_at, accepted_at, revoked_at

slots          id, host_id, kind(cam|host|screenshare),      -- host-global pool, wired into OBS once (D-20)
               idx,                                           -- cam slots 1..8 (~6 live, D-33)
               source_token_hash,                             -- permanent, host-only; HMAC(secret,token) (EN-5)
               epoch                                           -- monotonic; bumped on rebind/kick (EN-3)

host_source_tokens  id, stream_id, role(host|obs|obs_screen), -- D-18 host cam/screen routing, no pass
                    token_hash                                 -- per-stream, hashed (EN-5)

sessions  id, stream_id, host_id, started_at, ended_at, status(active|ended)  -- host_id → one-live-per-host (RF-2)

peers     id, session_id, pass_id?,                          -- stable pass_id keys reconnect (D-40, EN-3)
          role(host|guest|cohost|obs|obs_screen),
          connected_at, disconnected_at,
          used_turn                                           -- NO on_stage (D-11); written once at disconnect

pass_locks pass_id, modality, applier_rank_floor, applier_pass_id?, created_at  -- locks survive restart (AD-22)
```

### Concrete first-migration DDL (AD-17)

Conventions (AD-17): **IDs are UUIDv4 `TEXT`**; **timestamps are `INTEGER` Unix
seconds, UTC** (trivial expiry/purge comparisons, no TZ ambiguity — absolute UTC is
stored, local rendered, EN-25); token columns hold **`HMAC(server_secret, token)`**,
never bare hashes (EN-5). **All tables are declared `STRICT`** (AD-25) so SQLite
enforces the declared column types instead of silently coercing — every column is
`TEXT`/`INTEGER`, which `STRICT` permits; the migration appends `STRICT` to each
`CREATE TABLE` (omitted in the snippet below for brevity). Schema is kept broadly
Postgres-portable for a possible future (the `STRICT` keyword itself is SQLite-only).

```sql
-- migration 0001 — initial schema

CREATE TABLE hosts (
    id          TEXT    PRIMARY KEY,            -- UUIDv4
    google_sub  TEXT    NOT NULL UNIQUE,
    email       TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    picture     TEXT,
    is_admin    INTEGER NOT NULL DEFAULT 0,     -- bool 0/1
    status      TEXT    NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending','active','suspended')),
    created_at  INTEGER NOT NULL                -- Unix seconds UTC
);

CREATE TABLE streams (
    id               TEXT    PRIMARY KEY,
    host_id          TEXT    NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    title            TEXT    NOT NULL,
    scheduled_at     INTEGER,
    duration_min     INTEGER,
    status           TEXT    NOT NULL DEFAULT 'draft'
                     CHECK (status IN ('draft','scheduled','live','ended')),
    max_res          INTEGER,                   -- stream-wide quality ceiling (D-19)
    max_fps          INTEGER,
    max_bitrate_kbps INTEGER,
    twitch_yt_channel  TEXT,                     -- linked-channel live-verify (D-29)
    twitch_yt_platform TEXT
                     CHECK (twitch_yt_platform IN ('twitch','youtube') OR twitch_yt_platform IS NULL),
    created_at       INTEGER NOT NULL
);
CREATE INDEX idx_streams_host ON streams(host_id);

CREATE TABLE slots (                            -- host-global pool, wired into OBS once (D-20)
    id                TEXT    PRIMARY KEY,
    host_id           TEXT    NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    kind              TEXT    NOT NULL CHECK (kind IN ('cam','host','screenshare')),
    idx               INTEGER,                  -- cam slots 1..8; NULL for host/screenshare
    source_token_hash TEXT    NOT NULL,         -- HMAC(secret,token); permanent, host-only (EN-5)
    source_token_last_used_at   INTEGER,        -- leak-detection metadata (EN-5/AD-23)
    source_token_last_source_ip TEXT,           -- leak-detection metadata (EN-5/AD-23)
    epoch             INTEGER NOT NULL DEFAULT 0, -- in-memory authoritative; persisted at lifecycle edges only (RF-6)
    -- Slot shape (D-20): cam slots are addressable 1..8; the host (D-18) and shared
    -- screenshare (D-21) slots carry no idx. `idx IS NOT NULL` is load-bearing (a NULL
    -- cam idx would make the clause evaluate to NULL, which SQLite treats as passing).
    CHECK ((kind = 'cam' AND idx IS NOT NULL AND idx BETWEEN 1 AND 8)
        OR (kind IN ('host','screenshare') AND idx IS NULL))
);
CREATE INDEX idx_slots_host ON slots(host_id);
CREATE UNIQUE INDEX idx_slots_source_token ON slots(source_token_hash);  -- slot WS auth (/ws?src=) lookup
-- Host-global pool uniqueness (D-20): at most one cam slot per (host, idx), and at most
-- one host slot + one screenshare slot per host.
CREATE UNIQUE INDEX idx_slots_cam ON slots(host_id, idx) WHERE kind = 'cam';
CREATE UNIQUE INDEX idx_slots_singleton ON slots(host_id, kind) WHERE kind IN ('host','screenshare');

CREATE TABLE passes (
    id           TEXT    PRIMARY KEY,
    stream_id    TEXT    NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    slot_id      TEXT    REFERENCES slots(id) ON DELETE SET NULL,  -- per-pass slot binding (D-20); same-host validated in app (RF-2)
    name         TEXT,                          -- guest PII, purged 24h post-stream (D-37)
    email        TEXT,                          -- guest PII, purged 24h post-stream (D-37)
    role         TEXT    NOT NULL DEFAULT 'guest' CHECK (role IN ('guest','cohost')),
    token_hash   TEXT    NOT NULL,              -- HMAC(secret,token) (EN-5)
    can_screen   INTEGER NOT NULL DEFAULT 0,
    status       TEXT    NOT NULL DEFAULT 'created'
                 CHECK (status IN ('created','sent','opened','accepted','expired','revoked')),
    sent_at      INTEGER,
    expires_at   INTEGER,
    opened_at    INTEGER,
    accepted_at  INTEGER,
    revoked_at   INTEGER
);
CREATE INDEX idx_passes_stream ON passes(stream_id);
CREATE UNIQUE INDEX idx_passes_token ON passes(token_hash);   -- magic-link auth lookup (GET /p/{token})
-- at most one active occupant per slot per stream (RF-2)
CREATE UNIQUE INDEX idx_passes_active_slot ON passes(stream_id, slot_id)
    WHERE slot_id IS NOT NULL AND status NOT IN ('revoked','expired');

CREATE TABLE host_source_tokens (               -- D-18 host cam/screen routing, no pass
    id         TEXT NOT NULL PRIMARY KEY,
    stream_id  TEXT NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('host','obs','obs_screen')),
    token_hash TEXT NOT NULL,                   -- per-stream, hashed (EN-5)
    token_last_used_at   INTEGER,               -- leak-detection metadata (EN-5/AD-23)
    token_last_source_ip TEXT                   -- leak-detection metadata (EN-5/AD-23)
);
CREATE INDEX idx_host_source_tokens_stream ON host_source_tokens(stream_id);
CREATE UNIQUE INDEX idx_host_source_tokens_token ON host_source_tokens(token_hash);  -- source WS auth lookup
-- one active value per role per stream (EN-5): rotation replaces, never appends
CREATE UNIQUE INDEX idx_host_source_tokens_stream_role ON host_source_tokens(stream_id, role);

CREATE TABLE sessions (
    id         TEXT    PRIMARY KEY,
    stream_id  TEXT    NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    host_id    TEXT    NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,  -- denormalized for the one-live invariant (RF-2)
    started_at INTEGER NOT NULL,
    ended_at   INTEGER,
    status     TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active','ended'))
);
CREATE INDEX idx_sessions_stream ON sessions(stream_id);
-- DB-enforced one-live-session-per-host (EN-2/D-20): at most one active session per host (RF-2).
-- The admission check is ALSO serialized through the hub goroutine (not a snapshot read) — RF-2.
CREATE UNIQUE INDEX idx_sessions_one_live ON sessions(host_id) WHERE status = 'active';

CREATE TABLE peers (
    id              TEXT    PRIMARY KEY,
    session_id      TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    pass_id         TEXT    REFERENCES passes(id) ON DELETE SET NULL,  -- stable key for reconnect (D-40)
    role            TEXT    NOT NULL CHECK (role IN ('host','guest','cohost','obs','obs_screen')),
    connected_at    INTEGER NOT NULL,
    disconnected_at INTEGER,
    used_turn       INTEGER NOT NULL DEFAULT 0  -- NO on_stage (D-11); written once at disconnect (EN-11)
);
CREATE INDEX idx_peers_session ON peers(session_id);

-- Suppression locks persisted so moderation survives a server restart (AD-22/D-13/EN-7).
-- Not per-frame state, so EN-11's no-hot-path-persistence rule still holds. Loaded + re-applied
-- on room (re)spawn; deleted on release/expiry.
CREATE TABLE pass_locks (
    pass_id            TEXT    NOT NULL REFERENCES passes(id) ON DELETE CASCADE,
    modality           TEXT    NOT NULL CHECK (modality IN ('mic','cam','share')),
    applier_rank_floor TEXT    NOT NULL CHECK (applier_rank_floor IN ('host','cohost')),
    applier_pass_id    TEXT    REFERENCES passes(id) ON DELETE SET NULL,  -- NULL = applied by host (no pass)
    created_at         INTEGER NOT NULL,
    PRIMARY KEY (pass_id, modality)
);
```

The migration runner is **hand-rolled and embedded** (AD-6): `go:embed` numbered
`*.sql`, version tracked in a `schema_version` table **with a per-file checksum**;
each file applied in **its own all-or-nothing transaction**, forward-only. The runner
**refuses to start** on checksum drift of an already-applied file, a dirty/partial
state, or a binary older than the DB (RF-12). (See `CONVENTIONS.md` for the full
runner contract and `DEPLOYMENT.md` for backup ops + the backup-before-migrating-deploy
rule.)

### Invariants

- **No `peers.on_stage`, no staging state (D-11).** OBS owns composition; "on air"
  is a reflected-only signal derived from `obsSourceActiveChanged.active` at signal
  time, never authoritative server state.
- **`slot` supersedes per-pass source URLs (D-20).** OBS subscribes to a slot URL;
  the slot→occupant binding is per-pass, reassignable live. One live session per
  host at a time so slot URLs resolve unambiguously (EN-2).
- **Tokens are the crown jewels (EN-5).** Magic-link, slot-source, and host-source
  tokens are 128-bit, stored as `HMAC(server_secret, token)` (not bare SHA-256),
  constant-time compared. Source-page DOM/JS carries an opaque slot id only, zero
  secrets (EN-15).
- **Backstage chat is never persisted** — relayed through the WS only, no DB/file
  writer in the chat path; `chat.text` is never logged (D-26, EN-20).
- **Guest PII purged 24h post-stream (D-37).** `passes.name` / `passes.email` are
  deleted within 24h of stream end by the periodic purge job; no guest self-service
  deletion (auto-purge makes it moot). `sessions`/`peers` rows are transient, then
  purged or reduced to anonymous aggregates.
- **Suppression locks persist (AD-22).** `pass_locks` rows survive a server restart so
  a force-muted/hidden guest stays suppressed across a deploy/crash; they are loaded +
  re-applied on room (re)spawn and deleted on release/expiry. Not per-frame, so EN-11 holds.
- **Slot tokens authorize the media leg directly in v1 (AD-23).** The short-lived
  per-session media grant is deferred to v1.1; v1 mitigates the permanent token with
  rotation (D-22) + `last_used_at`/`last_source_ip` leak detection + `Referrer-Policy: no-referrer`.

### Pass lifecycle

```
created ──email──▶ sent ──link opened──▶ opened ──device-check / explicit accept──▶ accepted
   │                │                      │                                          │
   └────────────────┴──────────┬───────────┴──────────────────────────────────────────┘
                               ▼
              revoked   (host action: revoke / regenerate / kick)
              expired   (clock: expires_at passes)
```

- **No `joined` state.** The terminal active state is **`accepted`** (UI label
  "Accepted").
- **Expiry: 30 min after stream end (D-5).** Baseline `scheduled_at + duration_min
  + 30min`; if the session runs long, expiry extends to `session.ended_at + 30min`.
- **`GET /p/{token}` is side-effect-free (EN-10).** The transition to `opened`
  happens only on an **explicit client action** — a pass-authenticated
  `POST /p/{token}/enter` from the device-check island — so mail scanners and
  unfurlers can't false-positive. Prefetch-safe by construction. The transition
  fires only from a pre-opened state (`created`/`sent`), so a repeat entry is
  idempotent and never regresses an already-`accepted` pass.
- **Regenerate / revoke → "link turned off" screen.** Regenerate rotates the
  magic-link token, sets status back to `sent`, re-emails; the old link shows "link
  turned off". Expiry alone shows "pass expired".
- **Kick (greenroom)** = cooperative teardown + reconnect-blocked (D-25): atomically
  clear the slot binding + bump `slots.epoch` before the teardown broadcast (EN-3),
  invalidate the pass token, stop minting TURN creds, refuse re-join.

---

## 7. Signaling protocol contract (`internal/signaling`)

One WS endpoint (`GET /ws`), auth via JWT cookie (host/co-host) or `?pass=` /
`?src=` token (guest / OBS source page). **Role is inferred from auth, never trusted
from the frame.** The server relays SDP/ICE between peers and is the **sole
authority** for room state, moderation, and roles. All frames are small JSON objects
with a `t` discriminator; **media never rides the WS** — only signaling and control.

### Server-enforced invariants

- **Server-authoritative moderation & roles** (D-13/D-15). Forces, releases, role
  changes, kicks, select-live, and slot bindings are decided and applied
  server-side; clients render the result. UI gating is convenience only and is
  assumed bypassable (EN-7).
- **Locks enforced against self-state** (EN-7). A `{t:"state"}` frame from a target
  attempting `mic:true`/`cam:true`/`screen:true` on a modality with an active
  suppression lock is **rejected** (no-op + re-broadcast of authoritative state).
  Forces are suppressive-only: nobody at any rank can force a modality **on**
  (consent — D-13).
- **Role-filtered roster** (EN-8). The roster is a per-recipient server projection.
- **Signal relay carries only the payload** (D-23). A `{t:"signal"}` is re-emitted as a
  clean `{t, from, sdp|ice}` frame: the opaque sdp/ice is relayed byte-for-byte and
  stamped with the sender; no other client-supplied fields are echoed, so a peer can't
  inject roster/slot/control fields into a frame the addressee acts on.
- **One writer goroutine per connection** (EN-12) — fan-out to a peer is serialized.
- **Tokens redacted from logs**; `{t:"chat"}.text` is relayed, never persisted,
  never logged (EN-20).

### Client → server

```jsonc
{"t":"join"}                                              // enter the room for my session
{"t":"signal","to":"<peerId>","sdp":…}                    // relayed verbatim, server never inspects
{"t":"signal","to":"<peerId>","ice":…}
{"t":"state","cam":true,"mic":false,"screen":false,"level":0.42}
                                                          // self-presence, throttled. Rejected per-modality
                                                          // if a suppression lock is active (EN-7)
{"t":"chat","text":"…"}                                   // relayed to room, NEVER stored (EN-20)
{"t":"hand","raised":true}                                // soft "bring me in" nudge (raise/lower own)
{"t":"hand","peerId":"<id>","raised":false}               // host dismisses another's raised hand (lower-only)
                                                          // → roster `handRaised`; auto-cleared on leave + on promote to co-host
{"t":"screen-start"}   {"t":"screen-stop"}                // sharer enters/leaves the preview pool (D-21, RF-15)
{"t":"stats","turn":true,"rttMs":48,                      // periodic — feeds signal bars, admin TURN-%,
        "qualityLimitationReason":"cpu"}                  // and the degradation ladder (D-33)
                                                          // qualityLimitationReason ∈ none|cpu|bandwidth|other
{"t":"ice-refresh"}                                       // request a fresh TURN cred before TTL expiry (AD-14)

// from OBS source pages only (auth = slot src token; EN-15: opaque slot id only)
{"t":"obs","event":"sourceActive","active":true,"epoch":7} // per-guest on-program reflection (D-24);
                                                           // echoes the slot epoch so the server resolves
                                                           // slot→current occupant AT signal time (EN-1/EN-3)
{"t":"obs","event":"streamingStarted"}                     // global "we're live" reflection (D-11/D-24)
{"t":"obs","event":"streamingStopped"}
```

`sourceActive.active` fires only on **transitions** (`obsSourceActiveChanged`) —
never `visible`. On a fresh slot bind the server treats on-air as
`status-unavailable` until a real transition arrives (EN-3). `level` may be
forwarded in the roster but is never stored (EN-11).

### Host / co-host → server (rank-authorized)

Authority is evaluated **server-side against current rank** (Host > Co-host >
Guest, D-15) at apply time (demotion-safe, EN-7). An actor may act only on someone
**strictly below** them; never a peer or a superior. The host is immune and may
release/override anything.

A control frame names its target by the string `peerId` key (NOT `peer`, which is the
roster-entry OBJECT key in `peer-joined`) — the same distinction `peer-left` uses, so one
flat `Frame` envelope can carry either without a string-vs-object collision.

```jsonc
// Suppressive, authority-locked forces (D-13). Carry NO "on" direction — always toward off.
{"t":"force-mute","peerId":"<id>"}                        // stop outbound audio at source (lock kind: mic)
{"t":"force-no-cam","peerId":"<id>"}                      // stop outbound video at source (lock kind: cam)
{"t":"force-no-share","peerId":"<id>"}                    // revoke screenshare (lock kind: share); pull from preview pool + slot
{"t":"release","peerId":"<id>","kind":"mic"|"cam"|"share"} // unlock only; target then re-enables itself

// Roles — host only (D-15)
{"t":"role","peerId":"<id>","role":"cohost"|"guest"}      // promote / demote (live, from the greenroom)

// Screenshare preview-switcher — host only (D-21); the one sanctioned exception to "OBS owns composition"
{"t":"screen-select","peerId":"<id>"}                     // promote this active backstage share to LIVE
{"t":"screen-select","peerId":null}                       // clear slot → placeholder (no auto-advance)

// Lifecycle
{"t":"kick","peerId":"<id>"}                              // disconnect + invalidate pass (D-25)
{"t":"end-session"}                                       // host only (D-40)
```

### Server → clients

```jsonc
// ICE config join-ack (AD-14) — first frame after join; re-sent on {t:ice-refresh}.
// STUN always (D-38); a TURN entry with a fresh ephemeral HMAC cred + ttlSec is added when
// a relay is configured (EN-4). The iceServers entries mirror the browser RTCIceServer
// dictionary; ttlSec is a top-level hint (NOT inside the dict) for scheduling the refresh.
// The cred is coturn's REST shape: username "<expiryUnix>:<peerId>",
// credential base64(HMAC-SHA1(TURN_SECRET, username)).
{"t":"ice","iceServers":[{"urls":["stun:stun.example.org:3478"]}]}                  // STUN-only
{"t":"ice","ttlSec":90,"iceServers":[                                               // with a relay
  {"urls":["stun:stun.example.org:3478"]},
  {"urls":["turns:turn.example.org:5349"],"username":"1700000090:<peerId>","credential":"<base64>"}]}

// Role-filtered roster projection (EN-8) — per-recipient; guests get the reduced shape.
// `self` is the recipient's own peer id, so a client can locate its own entry (e.g. the guest
// self on-air pill, whose value now folds in via the entry's `onAir`). `level` rides the
// batched {t:levels} tick (AD-13), NOT the roster, so it is absent here.
{"t":"roster","self":"<peerId>","peers":[{
  "id":"<peerId>","name":"…","role":"host"|"cohost"|"guest"|"obs"|"obs_screen",
  "cam":true,"mic":false,"screen":false,"handRaised":false,
  "onAir":"on-air"|"not-on-air"|"status-unavailable",   // three-state, OBS-reflected (D-24)
  "signal":3,"rttMs":48,"degraded":{"dir":"lowering"|"recovering","reason":"cpu"|"bandwidth"},
  "locks":[{"kind":"mic"|"cam"|"share","applierPeerId":"<id>","applierRank":"host"|"cohost"}],
  "self":true                                            // present only on the recipient's own entry
                                                          // applierRank tells clients WHO may release (EN-7)
}]}

{"t":"peer-joined","peer":{…}}                            // same per-recipient shape as a roster entry
{"t":"peer-left","peerId":"<id>"}                         // string id (distinct key from peer-joined's object)
{"t":"signal","from":"<peerId>","sdp"|"ice":…}            // relayed SDP/ICE
{"t":"chat","from":"<peerId>","text":"…"}                 // relayed only (EN-20)

// Batched audio-meter tick (AD-13) — every participant's last-reported {t:state} level coalesced
// onto ONE ~6–7 Hz room tick instead of riding the roster (no N² spam at the cap). In-memory
// only, never persisted (EN-11); stays silent in a quiet room (one trailing all-zero frame when
// it falls silent so clients settle their meters). OBS source virtual peers have no meter.
{"t":"levels","levels":{"<peerId>":0.4,…}}                // peerId → level (0..1), to each participant

// Screenshare preview-switcher state (host-only) (D-21)
{"t":"screen-roster","previews":["<peerId>",…],"live":"<peerId>"|null}

// Slot rebind protocol (EN-3 / D-20)
{"t":"slot-rebind","slot":"cam-3","occupantPeerId":"<id>","epoch":8}
{"t":"slot-unbound","slot":"cam-3","epoch":9}

// On-air reflection (D-24). Per-guest on-air folds into the roster entry's `onAir` field (M3
// PR-1 retired the interim standalone {t:onair} frame): the server recomputes the occupant's
// folded on-air from the slot state and re-broadcasts the roster on any change, and a (re)joiner
// reads its on-air straight from the roster it receives. The broadcast-level "we're live" stays
// a room-wide {t:streaming} broadcast (it is room-scoped, not per-guest), still replayed to a
// participant on (re)join so a mid-stream joiner isn't stuck at the default. Both are driven by
// the source page's {t:obs,event:"sourceActive"/"streamingStarted"/...}.
{"t":"streaming","active":true}                          // global "we're live" → all participants

// Terminate-reason taxonomy (EN-9) — sent BEFORE close so the client routes correctly
{"t":"terminate","reason":"reconnect"}                    // TRANSIENT → retry with backoff (keyed by pass_id)
{"t":"terminate","reason":
   "kicked"|"expired"|"revoked"|"session-ended"|"token-rotated"}   // TERMINAL → stop, route to error screen
```

### Roster projection (EN-8)

The roster is a **per-recipient server projection**, filtered by the recipient's
rank:

- **Guest projection** omits emails, source/slot URLs, other passes, and host-only
  `obs`/`obs_screen` virtual peers.
- **Co-host projection** shows moderation state (locks, applier rank) **but not**
  source URLs or quality controls (D-15).
- **Host projection** is the full shape, including the host-only `obs` virtual peers
  (one per slot source page).

> **Status (M3 PR-1).** The roster carries the full entry shape — `name`, `cam`/`mic`/`screen`
> (a `{t:state}` self-presence snapshot, EN-7), `handRaised`, three-state `onAir` (folded from
> the slot, retiring the interim `{t:onair}`), `signal`/`rttMs`/`degraded`, and `locks[]` — plus
> a per-recipient `self` marker. The `obs`/`obs_screen` source virtual peers stay **host-only**
> (omitted from guest/co-host projections), and OBS source pages receive no roster of their own
> (EN-13). Fields that later PRs drive are present in the shape from PR-1 but unset until their
> PR populates them: `locks` (PR-3), `handRaised` (PR-7), `signal`/`rttMs`/`degraded` (PR-13);
> the audio meter rides the `{t:levels}` tick (PR-2), not the roster.

Full `roster` / `peer-joined` / `peer-left` frames go out only on structural change
(join/leave/presence/lock/role/on-air/slot): a join/leave uses the `peer-joined`/`peer-left`
delta, and any other change re-broadcasts each participant its projected `roster`. Continuous
audio meters ride a separate batched tick frame (AD-13), never the roster.

### Suppression-lock state machine (D-13 / EN-7)

Each force creates **one lock per `(target, modality)`**, keyed to
`{applierPeerId, rank-floor}` and evaluated against the actor's **current** rank
(demotion-safe).

```
                  force at rank R (R strictly above target)
   (unlocked) ───────────────────────────────────────────▶ (locked: owner=applierPeerId, floor=R)
       ▲                                                          │  │  │
       │                                                          │  │  └─ lower-rank force → NO-OP
       │ release by applier OR any rank ≥ floor OR host           │  └──── higher-rank force → RAISE floor, owner := new applier
       └──────────────────────────────────────────────────────────┘
       (target self-release: FORBIDDEN — target can never unlock itself)
```

- A higher-rank force on a locked modality **raises** the owner; a lower-rank force
  is a **no-op**.
- The **target cannot self-release**; `release` from the applier or any higher rank
  unlocks; the **host always can** (no orphaned lock if the applier disconnects).
- Each force stops the relevant outbound track **at source**, so it dies in the mesh
  *and* in any OBS source. The server also **rejects self-state frames** that
  violate an active lock (a `{t:"state",mic:true}` from a force-muted target is
  dropped, not relayed) — this is the server-side enforcement point, since UI blocking
  is bypassable.
- **Receiver-side enforcement too (RF-8).** Server-reject-self-state is necessary but
  not sufficient in a P2P mesh: a modified client can keep *sending* media to
  cooperating peers. So on a lock, every cooperating peer must **detach/mute the
  target's corresponding remote track** from rendering *and* OBS output, independent of
  the target's self-state. The force also stops the outbound track at source on
  cooperating clients, so it dies in the mesh and in any OBS source.
- Co-hosts may `force-no-share` (moderation) but **cannot** `screen-select`
  (host-only direction).

### Slot-rebind / epoch handling (EN-3)

Each slot carries a monotonic **epoch**. On rebind: close the old PeerLink,
renegotiate to the new occupant, stop the old encoder, and **reset on-air to
`status-unavailable`** until a fresh `obsSourceActiveChanged` transition arrives (so
a stale `active:true` can't mislight the new occupant). On kick / force-end:
**atomically clear the binding + bump epoch BEFORE the teardown broadcast** so a
reconnecting modified source resolves to placeholder, not the kicked occupant.

**Epoch source of truth (RF-6).** The epoch is authoritative **in-memory**, seeded from
`slots.epoch` when the room spawns and persisted back only at lifecycle edges (never on
the hot path — EN-11). A (re)binding source always **re-fetches** its epoch from the
server rather than trusting a cached value, so a source that survived a server restart
can't out-number the server. The server applies an OBS `sourceActive` event **only when
`event.epoch == current slot epoch`**: a stale (lower) epoch is ignored; a future
(higher) epoch terminates + reconnects that source.

### Terminate-reason taxonomy (EN-9)

The server emits `terminate` with a reason, **then closes**.

| Class | Reasons | Client action |
|---|---|---|
| **Transient** | `reconnect` (and network blips) | Reconnect with exponential backoff, re-keyed by stable `pass_id` (D-40) so OBS sources auto-reattach. |
| **Terminal** | `kicked`, `expired`, `revoked`, `session-ended`, `token-rotated` | Stop reconnection; route to the matching error screen — `kicked` → "removed by host", `expired`/`revoked` → pass error screens, `session-ended` → "stream ended", `token-rotated` → re-auth (e.g. after D-22 slot-token rotation). |

**No-frame fallback (RF-22).** Real WS failures often close without a final frame.
Define close-code conventions; **absence of a `terminate` frame ⇒ treat as transient**
(reconnect with backoff), *except* when reconnect-time token validation returns a
terminal status (kicked/expired/revoked/rotated), which routes to the error screen.

---

## 8. Frontend architecture

### Rendering posture — Go-rendered HTML + Preact islands (D-32)

The frontend is **not** an SPA. The vast majority of screens are mostly static and
are **server-rendered with Go `html/template`**; only the real-time, stateful
surfaces ship JavaScript, as **vendored Preact islands** that mount into a
server-rendered page. There is **no SPA hash-router** — server routes own
navigation, islands mount per-page against a known root element.

| Surface | Rendering | JS |
|---|---|---|
| Marketing / comparison / parody | Go `html/template` | none |
| Host sign-in (Google) | Go `html/template` | none |
| Guest ticket / pass acceptance | Go `html/template` | none |
| Host dashboard / calendar / invites / sources tabs | Go `html/template` | none |
| Admin console | Go `html/template` | minimal (poll/refresh) |
| Error / state screens | Go `html/template` | none (except `reconnecting`) |
| **Device check** | `/p/{token}` server page + island | Preact island |
| **Guest session** | in-session phase of the device-check island (same page, signaling connection, and captured camera) | Preact island |
| **Greenroom** (host + co-host + guest) | server page + island | Preact island |
| **OBS source page** (cam + screen) | minimal standalone HTML | **separate** minimal entry (EN-13) |

Islands are confined to ~4–5 screens, keeping the JS attack-and-maintenance surface
tiny and the frontend off the DB/PII path entirely (server-side authz EN-6/EN-8 is
the guard).

### Islands — plain JS + JSDoc (D-32)

Islands are authored in **plain JS with JSDoc type annotations, not TypeScript**:
esbuild transpiles but never typechecks, and `tsc`/`tsgo` are npm-only — shipping
`.ts` would deliver TS *syntax with zero type safety*. JSDoc gives editor-aware
types npm-free, with an optional external `tsc`/`tsgo` gate bolt-on later.

- **Preact is vendored** into `web/vendor/preact/` (MIT, committed); no registry
  fetch.
- **Island state = Preact hooks only** (AD-18): `useState`/`useReducer`/`useContext`
  from vendored `preact/hooks`. A **`Room` orchestrator holds canonical state** and
  pushes via context/reducer. Zero extra deps; adequate for ~4 island screens.
- AGPL §13 (EN-17): the source link must resolve to the **running** build (embed the
  build/commit ref) and appear in the **guest/greenroom** UI — guests are §13
  network users, so a host-dashboard-only link is insufficient.

### The `web/src/rtc/` module

Shared by the app entry; a trimmed subset by the OBS entry.

- **device enumeration + `getUserMedia` wrapper** (device-check and the cam-blocked
  mic-only fallback).
- a **`PeerLink` class — one per remote consumer** (offer/answer, ICE restart,
  per-link outbound cap via `RTCRtpSender.setParameters()` — the per-publisher-local
  degradation mechanism, AD-21).
- a **`Room` orchestrator** bound to the signaling WS (roster, slot-rebind,
  force-lock state) holding canonical island state (AD-18).
- **`getStats()` health sampling** — the principled signal feeding the degradation
  ladder (D-33) and per-guest health UI: `qualityLimitationReason`, framerate,
  bitrate, loss, RTT, `used_turn`. There is **no pre-flight speed test** (D-12).

### Build — esbuild as a Go library (AD-7 / EN-13)

The build runs **esbuild as a Go library** (`github.com/evanw/esbuild/pkg/api`),
invoked via `go run ./cmd/build` (no node, no `package.json`). The output
(`web/dist/`) is **gitignored** and `go:embed`-ed at release; served from disk via
esbuild watch in dev. **Build ordering (RF-13):** because `go:embed web/dist` requires
the bundle to exist at compile time, `go generate ./...` runs `cmd/build` first, and a
committed **embed stub** keeps a bare `go build` from failing on a fresh checkout; CI
and the Dockerfile run the build before the server build (the same path). Two pinned
aspects are load-bearing in `BuildOptions`:

1. **Vendored-Preact resolution** — an `Alias` map redirects bare specifiers
   (`preact`, `preact/hooks`, `preact/jsx-runtime`) to the exact committed vendored
   files; JSX mode `automatic` against the vendored runtime. No `node_modules`
   resolution is ever attempted.
2. **Conservative CSS target — the oldest supported GUEST browser, NOT OBS-CEF-127**
   (EN-13). The binding constraint is the guest's (older, varied) browser, not OBS's
   CEF. `color-mix()` down-leveling has known Safari bugs and styles use it heavily,
   so the down-leveled output gets a **manual Safari check** (a build gate).

**Two deliberately separate entries (EN-13):**

- **App entry** — device-check + guest-session + greenroom islands; loads
  self-hosted fonts + full token CSS.
- **OBS source-page entry** — cam + screen pages only; **no `@font-face`, no
  `system-ui` stack, minimal JS** (PeerLink + `window.obsstudio` relay + reconnect).
  Not an SPA route. Keeps the ~412 KB font payload and the full app out of up to 9
  browser sources per show and out of guest phones rendering source pages.

### Public greenroom demo (D-43)

The landing "Tour the greenroom" CTA → a public, no-auth `/demo` route mounts the
**real greenroom island** with a **canned read-only adapter swapped in for the
`Room` orchestrator** — fixture roster/state, no WS, no `PeerLink`/WebRTC, no media,
no session. Because it is the *same* island (not a fork), greenroom changes
propagate to the demo automatically (zero divergence). Labeled "Demo · sample data,
no real session"; host-only/destructive actions are inert. Rule: **one greenroom,
pluggable data/transport, demo = canned adapter** — never a copy.

---

## 9. Security & retention pointers

Brief here; operational detail lives in `CONVENTIONS.md` (logging, error handling)
and `DEPLOYMENT.md` (config, secrets, TURN ops, purge job scheduling).

### Token families (EN-5)

Three token families, all 128-bit random, **stored as `HMAC(server_secret, token)`**,
constant-time compared, one active value at a time:

| Token | Borne by | Lifetime | Sensitivity |
|---|---|---|---|
| **Pass token** | guest magic link `GET /p/{token}` | per-pass; PII purged 24h post-stream | medium |
| **Slot source token** | OBS source URLs (cam/host/screenshare slots) | **permanent, reused every stream** | **crown jewel** (EN-5) — a leaked slot token is a standing subscription to every future occupant |
| **TURN cred** | ICE config, BYO-TURN only (D-38) | 60–120s (EN-4) | low, short-lived |

**v1 posture (AD-23).** The slot/source token authenticates the WS handshake **and**
authorizes the media leg directly — the short-lived per-session media grant is deferred
to v1.1. Because the token is permanent and reused, v1 leans on three mitigations: the
host's **"my URLs leaked" panic button** (D-22) rotates all slot tokens at once (tearing
down live subscriptions; per-slot rotation for a single leaked URL); **last-used-at +
last-source-IP metadata** on each slot/source token (the `source_token_last_*` /
`token_last_*` columns — slot/source tokens only, never pass tokens) lets a host spot
an unexpected subscription; and source pages send **`Referrer-Policy: no-referrer`** with no
third-party requests so the token can't leak via referrer/history (RF-24). The binary
**fails closed** on empty/placeholder `JWT_SECRET`/`TURN_SECRET` (EN-14).

### Privacy boundary (D-14 / EN-20)

The admin/owner is a host with an `is_admin` flag (D-14). When acting on **other
hosts'** sessions, the admin console gets **metadata + force-end only, never their
backstage media or chat**. Enforced as tests:

- **No admin route opens a media or chat subscription on a foreign session.**
- **`{t:"chat"}.text` is never logged** anywhere, at any level.
- **The chat relay path has no DB or file writer in scope** — backstage chat is
  relay-only, structurally unpersistable (EN-20).

### 24h purge (D-37)

Guest PII (`passes.name` / `passes.email`) is **deleted within 24h of stream end** by
an in-binary hourly purge ticker (`internal/jobs`); no guest self-service deletion
(auto-purge makes it moot — guests are told before/after via copy). Session/peer rows
are transient, then purged or reduced to anonymous, never-per-person-trackable
aggregates (streams run, guest-minutes, TURN-relay %, uptime). Host PII is the only
non-anonymous surface and drives host self-service export/amend/delete. Operational
purge-job behavior and GDPR self-service routes are detailed in `DEPLOYMENT.md`.
