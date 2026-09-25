# Testing

How GuestPass is tested: the TDD workflow, the test layers and what belongs in
each, the security/privacy invariants that must exist *as tests*, the manual
gates that cannot be automated, and the CI gates.

This doc is self-contained. It cites the governing decisions inline
(`D-*` = product/design, `EN-*` = engineering invariant, `AD-*` = implementation
architecture). Where something is genuinely undecided it is marked `> OPEN:`.

---

## 1. TDD is non-negotiable

**Every behavior change is red → green.** This is a repo hard rule, not a
preference:

1. **Write the failing test first.**
2. **Run it and watch it fail** — confirm it fails *for the expected reason*
   (the assertion you care about), not a compile error or an unrelated panic.
3. **Implement the minimum** to make it pass — no speculative extra behavior.
4. **Rerun to green.**

Do not write production behavior ahead of a test that demands it. Do not weaken
a test, a lint rule, or a CI gate to get to green — fix the finding at its
source or surface the blocker (see [§6](#6-ci-gates-ad-11)).

Pure docs/config bootstrap (no behavior change) is exempt and is validated by
repo inspection instead.

---

## 2. Test layering (AD-5)

The actor model (AD-2) is the payoff here: each live room is one goroutine that
owns its state, and every mutation arrives as a command frame. But the room
runtime is **not** a pure function — it also owns timers, coalescing, snapshot
publication, and backpressure (RF-25). So we split the two and test each on its
own terms: from cheapest/most-numerous to slowest/fewest:

### 2.1 Room & signaling logic — pure-Go table tests

**This is where the hard logic lives, and where most tests live.** The split
(RF-25):

- **Pure reducer** — `(state, command) → (new state, outbound frames)`, no I/O,
  no clock. This is the bulk of the TDD: drive it with synthetic command frames
  and assert on the frames it emits. **No network, no WebSocket, no browser, no
  media, no timers.**
- **Effectful room runtime** — the goroutine wrapping the reducer; tested
  separately for the behavior the reducer cannot express: meter ticks,
  `sendCh` overflow-drop (backpressure), unregister ordering, snapshot
  publication, and the **boundary races** between the room and its environment —
  DB admission, hub-map lifecycle, reconnect/eviction, and token rotation
  (RF-27).

Lives in `internal/signaling/` (`room.go`, `roster.go`, `locks.go`,
`slots.go`, `frames.go`) with `*_test.go` table tests alongside (AD-4/AD-5).

What the reducer layer must cover:

- **Suppression-lock state machine** (D-13 / EN-7): one lock per
  `(target, modality)`, keyed to `applierPeerId` + a rank floor; higher-rank
  force raises the owner; lower-rank force on a locked modality is a no-op;
  target cannot self-release; host always can; no orphaned lock when the applier
  disconnects (demotion-safe, evaluated against *current* rank).
- **Roster projection** (EN-8): per-recipient, role-filtered. Guests' projection
  omits emails, source/slot URLs, other passes, and host-only `obs`/`obs_screen`
  virtual peers. Co-hosts see moderation state but not source URLs or quality
  controls (D-15).
- **Slot binding / epoch** (EN-3): monotonic epoch; on rebind, on-air resets to
  `status-unavailable` until a fresh `obsSourceActiveChanged` transition; on
  kick/force-end the binding is cleared and the epoch bumped **atomically before**
  the teardown broadcast, so a reconnecting modified source resolves to a
  placeholder, not the kicked occupant.
- **Terminate routing** (EN-9): the `{t:"terminate","reason":…}` taxonomy —
  transient (`reconnect`) → client retries with backoff keyed by `pass_id`;
  terminal (`displaced`/`kicked`/`expired`/`revoked`/`session-ended`/`token-rotated`)
  → stop and route to the correct error screen.
- **Screenshare preview-switcher** (D-21): multiple eligible sharers populate the
  host-only preview rail; `screen-select` is host-only and promotes one share
  live; co-hosts may `force-no-share` but **cannot** `screen-select`;
  revoke/force-no-share pulls the guest from the pool and, if live, drops the
  slot to placeholder with **no auto-advance**.

### 2.2 WebSocket transport — integration tests

Exercise the real `/ws` endpoint with an **in-process WebSocket client**: an
`httptest` server fronting the hub, driven by a `coder/websocket` client
(AD-16). No browser.

What this layer must cover:

- **Join / auth** for each identity: host/co-host (JWT cookie), guest (`?pass=`),
  OBS source page (`?src=`). Role is inferred from auth, never trusted from the
  frame.
- **Origin handling incl. the null OBS-CEF origin** (EN-16): host/guest
  connections get strict Origin validation; OBS browser-source connections send
  `Origin: null` and must be accepted **for source-token connections only**,
  without weakening Origin checks for host/guest. A host/guest Origin that
  matches the configured `BASE_URL` host is also admitted, so a reverse proxy
  that rewrites `Host` to an internal service name does not 403 the handshake.
- **One-connection-per-identity eviction** (EN-16): a second connection for the
  same identity evicts the prior one; reconnect is rate-limited.
- **Reconnect** keyed by stable `pass_id` (D-40 / EN-3): a dropped peer rejoins
  the same room and re-binds the same slot.

### 2.3 Islands + OBS source pages — chromedp

Browser-level tests run on **chromedp** (AD-9) — pure-Go headless Chrome, no
npm, no Playwright/Jest/any node runner. Fake media is supplied with Chrome
flags:

```
--use-fake-device-for-media-stream   # synthetic cam/mic, no hardware
--use-fake-ui-for-media-stream       # auto-accept the getUserMedia prompt
```

What this layer covers: the device-check, guest-session, and greenroom islands
mount and render against a server-rendered page; the OBS cam/screen source pages
(the separate minimal esbuild entry, EN-13) load, authenticate with a slot token
alone, and render an occupant.

### 2.4 M2 media tracer — chromedp + real Chrome WebRTC

The M2 tracer drives **multiple real-Chrome tabs** — guest, host, and
OBS-source-page tabs — with fake media (AD-10), and asserts **protocol +
plumbing correctness**: offer/answer/ICE relay through the signaling hub,
slot-rebind re-subscribe, and that an occupant actually renders.

What it does **not** prove (RF-7), and what covers each instead:

- **Capacity** (the "~6 guests" target) — chromedp cannot prove this; encoder
  feasibility on real hardware is proven by **SPIKE-0** before M1 (AD-24, the
  basis on which SPIKE-0 confirmed AD-21 — now wired in M3, see
  [§5](#5-what-cannot-be-automated)).
- **Real-network / NAT traversal** and **real OBS-CEF media receive** — neither is
  exercised by fake-media headless Chrome; both are covered by the documented
  **manual** smokes (multi-machine/multi-network + real-OBS-CEF) in
  [§5](#5-what-cannot-be-automated). (Safari / non-Chrome / mobile guests are
  **deferred from v1** — owner 2026-06-15 — so they are not a v1 gate; see §5.)

`pion` test-peers are rejected for this: production forbids a server-side media
stack (D-23), so a pion peer would be false confidence. Real Chrome WebRTC is
the highest-fidelity *automatable* proof **of protocol/plumbing** — not of
capacity or the real transport/render path.

---

## 3. Invariants enforced as tests (not prose)

These are security/privacy guarantees the suite **must assert directly** — they
are tests, not comments. This is the checklist; each item names its enforcement
point.

- [ ] **Privacy boundary — no foreign media/chat subscription** (D-14 / EN-20):
  no admin route opens a media or chat subscription on another host's session.
  Admin endpoints return metadata only (force-end, suspend/promote, session/peer
  counts, TURN-relay %); none can join a foreign room as a media/chat peer.
- [ ] **Chat is structurally unpersistable** (D-26 / EN-20): `{t:"chat"}.text` is
  never written to the DB and **never logged at any level**; the chat relay path
  has no DB or file writer in scope. Assert there is no writer on the path, not
  just that a given call didn't write.
- [ ] **Suppression-lock enforced server-side** (EN-7): a force-muted target's
  `{t:"state","mic":true}` is **rejected (no-op + re-broadcast of authoritative
  state)**, not merely UI-blocked; the same for `cam`/`screen`. The target
  cannot self-release. Enforcement is demotion-safe (evaluated against current
  rank). Forces are suppressive-only — no rank can force a modality *on* (D-13).
  Enforcement must include **receiver-side track detach** — peers drop the
  suppressed track, not just the server rejecting the publisher's self-state.
- [ ] **Suppression locks survive restart** (AD-22): locks persist to SQLite and
  a process restart **re-applies** them; assert a force-mute set before restart
  is still enforced after.
- [ ] **Token handling** (EN-5 / EN-16): pass, slot-source, and host-source
  tokens are stored as `HMAC(server_secret, token)` (not bare SHA-256), compared
  in **constant time**, and **redacted from logs** — including `/ws` handshakes
  and `?src=`/`?pass=` query strings.
- [ ] **SSRF guards for live-verify** (D-29): the live-status fetcher accepts a
  `(platform, channel-identifier)` pair and **never a raw URL**; builds the
  request from a fixed `twitch.tv`/`youtube.com` template; blocks
  private/loopback/link-local/cloud-metadata IPs (RFC1918, `127.0.0.0/8`,
  `169.254.0.0/16` incl. `169.254.169.254`); http(s)-only with tight
  timeout/size caps. The test cases must cover (RF-9):
  - **DNS rebinding** — a host that resolves to a public IP at check time but a
    blocked IP at dial time; assert the resolved IP is **pinned to the actual
    dial** (resolve→dial gap closed), not re-resolved.
  - **IP-encoding evasions** — IPv6 blocked ranges, **IPv4-mapped-IPv6**
    (`::ffff:169.254.169.254`), and **decimal/octal/hex** encodings of blocked
    IPv4 addresses.
  - **Redirects** — re-validate the target after **each** redirect hop and
    **refuse off-domain redirects**, not just the initial host.
- [ ] **Secrets fail closed** (EN-14 / AD-8): the binary **refuses to start** if
  `JWT_SECRET` or `TURN_SECRET` is empty or equals a shipped placeholder, and if
  `AUTH_MODE=dev` is set outside dev (production). Assert refuse-to-start, not a
  warning.
- [ ] **`GET /p/{token}` is side-effect-free** (EN-10): a bare GET validates and
  renders but **does not** mark the pass `opened`; the transition to `opened`
  happens only on the explicit device-check entry action. (Mail scanners and
  link unfurlers prefetch the link — this keeps it prefetch-safe.)

**Each invariant test lands with its code** (RF-18) — it attaches to the *first*
milestone that introduces the relevant path, not deferred to M5:

| Milestone | Invariant test lands |
|---|---|
| **M1** | Secrets fail closed (EN-14 / AD-8); `AUTH_MODE=dev` gated behind a `//go:build dev` tag + refuse-to-start outside dev |
| **M2** | `GET /p/{token}` side-effect-free (EN-10) |
| **M3** | Token redaction in logs (EN-5/EN-16); suppression-lock enforcement incl. **receiver-side track detach** (EN-7); chat never persisted/logged (D-26/EN-20); lock-survives-restart (AD-22) |
| **M5** | SSRF guards (D-29, when livecheck lands); admin privacy boundary (D-14/EN-20) |

> Additional invariants worth a named test as their code lands: `kid` two-key
> ring sign/verify across rotation (EN-6); live-DB authz rejecting a
> `suspended`/`pending` host mid-session (EN-6); migration runner refusing a
> binary older than the DB (AD-6); TURN credential TTL 60–120s (EN-4).

---

## 4. Local test & dev ergonomics

Two seams keep tests hermetic and onboarding cheap — both fail closed in
production:

- **`AUTH_MODE=dev`** (AD-8) mints a fake host session **without Google OAuth**,
  so integration and browser tests run with no external identity provider.
  Invariant: refuse to start if set outside dev (same posture as EN-14).
- **`MAIL_MODE=log`** (D-2) prints magic links to stdout instead of calling
  Resend, so the pass-mint flow is testable with no mail provider.

### Running the suite

```sh
# everything (the default CI command)
go test ./...

# a single layer
go test ./internal/signaling/...   # pure-Go room/lock/roster/slot/epoch tables
go test ./internal/web/...         # WS transport + handler integration (httptest)

# browser layer (chromedp): requires a local Chrome/Chromium.
# fake media is injected via chromedp flags — no real camera needed.
# NB: `web/` is the (non-Go) frontend source dir — the chromedp/Go browser
# tests live in a tagged Go package, NOT under web/ (RF-26).
go test -tags browser ./internal/browsertest/...  # islands + OBS source pages
go test -tags browser -run Tracer ./internal/browsertest/...  # M2 media tracer (multi-tab real-Chrome WebRTC)
```

> RESOLVED (M2 PR-5): the chromedp browser layer is the Go package
> **`internal/browsertest`**, compiled only under the **`//go:build browser`** tag and run
> with `go test -tags browser ./internal/browsertest/...` — locally and in the CI `browser`
> job (which installs Chrome and sets `CHROME_PATH`). The harness builds the real bundles
> via `internal/assets` and drives them in headless Chrome with the fake-media flags. It is
> a **Go** package, never under `web/` (RF-26).

---

## 5. What cannot be automated

These gates are manual by necessity:

- **SPIKE-0 — encoder-capacity probe on real hardware, BEFORE M1** (AD-24). A
  throwaway probe spinning up N `RTCPeerConnection`s on **real hardware** is the
  **primary proof of the "~6 guests" capacity** — chromedp/fake-media cannot
  prove it (RF-7). It is also the gate that **confirmed AD-21**: the
  `setParameters()` "shed an encoder" mechanism was unproven until SPIKE-0
  demonstrated it (now wired in M3, PR-13/PR-14). This runs **pre-build** and is
  **not CI-automatable**.
- **Real OBS-CEF media-receive smoke — gate at SPIKE-2 / M2 step 0** (RF-17 /
  AD-10). Before building on the OBS path, prove an **actual OBS browser source
  (CEF 127) receives and renders media** — not merely that the page renegotiates.
  This is a precondition, not just an M2-DoD afterthought.
- **Safari `color-mix()` down-level check — manual M1 DoD gate** (EN-13).
  `styles-v2.css` uses `color-mix()` heavily, and esbuild's `color-mix()`
  down-level rewrite (targeting the oldest supported *guest* browser, not
  OBS-CEF-127) has **known Safari bugs**. The transpiled output must be eyeballed
  in real Safari before M1 is done — do not assume the transpile is correct.
  (Safari / mobile guest support is now **deferred from v1** — owner 2026-06-15 —
  so this is no longer a live v1 gate.)
- **Multi-machine / multi-network smoke — manual M2 DoD** (RF-7). Real-network
  and NAT traversal are out of reach for headless fake-media Chrome; run guests
  across **separate machines on separate networks** to exercise actual ICE/NAT.
  The default posture is **STUN-only** (D-38): a direct connection is the gate;
  **relay fallback** is exercised only when an optional / BYO TURN is configured
  (off by default). See `docs/SMOKE.md`.
- **Real-OBS render smoke — manual M2 DoD** (AD-10). Beyond the SPIKE-2 receive
  gate, a manual pass with actual OBS ≥ 31 (CEF 127) confirms the browser source
  renders the occupant in a real OBS scene. This complements, not replaces, the
  automated tracer (which proves only protocol/plumbing — RF-7). The
  **`cmd/devsmoke`** dev helper (`-tags dev`, AUTH_MODE=dev only) seeds the
  fixtures this smoke needs but that have no host UI until M4 — a stream, a guest
  pass, and a cam-1 slot for the local dev host — prints the guest + OBS-source
  URLs, and binds the slot to the guest by sending a `{t:rebind}` over a host
  `/ws` connection.
- **Safari / mobile guest smoke — DEFERRED from v1** (owner decision, 2026-06-15).
  Safari and non-Chrome / mobile guest support is **post-v1**, so it is **not a v1
  gate** (was RF-7). v1 targets desktop Chrome guests (and OBS-CEF) only.

---

## 6. CI gates (AD-11)

CI is **GitHub Actions**. Every gate is Go-toolchain-only — **no npm anywhere**,
no `node`, no `package.json`, no registry access at build time (LOCKED).

| Gate | Command | Fails on |
|---|---|---|
| Tests | `go test ./...` | any failing test |
| Vet | `go vet ./...` | suspicious constructs |
| Static analysis | `staticcheck ./...` | staticcheck findings |
| Vulnerability scan (§7.5 security gate) | `govulncheck ./...` | known CVEs in deps |
| Formatting | `gofmt -l .` | **any** diff (non-empty output ⇒ fail) |
| Frontend build | `go run ./cmd/build` | esbuild build failure |
| Dev-build tag | `go vet -tags dev ./...` + `go test -tags dev ./...` | the dev-auth seam (`AUTH_MODE=dev`, AD-8 / RF-4) failing to build, or its dev-only invariant tests (e.g. loopback-`BASE_URL`) failing |

The `dev` build-tag lane compiles and tests the `AUTH_MODE=dev` seam, whose code —
and whose dev-only tests (loopback-`BASE_URL` enforcement) — exist **only** under
`-tags dev`. Release binaries never include the tag, so the seam is absent from
production builds (CONVENTIONS §1.5); the lane keeps that path from rotting.

The esbuild build runs esbuild **as a Go library** (`github.com/evanw/esbuild/pkg/api`)
with the vendored-Preact alias map and the two pinned entries (app + OBS), so the
frontend builds with the Go toolchain alone (EN-13 / AD-7).

The Safari `color-mix()` check ([§5](#5-what-cannot-be-automated)) is a **manual
M1 DoD gate**, not a CI step.

**SPIKE-0** ([§5](#5-what-cannot-be-automated)) sits *before* this picture: it is
a **pre-build, real-hardware gate** (AD-24) — the capacity proof for "~6 guests"
and the gate that confirmed AD-21 (since wired in M3). It is **not
CI-automatable** (real encoders, real hardware) and gates M1, not any CI job.

---

## Cross-references

- Package boundaries, the actor/hub model, and the signaling-core contract:
  `docs/ARCHITECTURE.md`.
- Logging/error/config conventions and the plain-JS+JSDoc island style:
  `docs/CONVENTIONS.md`.
- Config vars (`AUTH_MODE`, `MAIL_MODE`, the fail-closed secret pair),
  `SIGNUP_MODE`, and self-host/CI provisioning: `docs/DEPLOYMENT.md`.
