# GuestPass — Conventions

Coding, data, frontend, security, and repo conventions for GuestPass. This is a
committed source-of-truth doc; it distills the approved design and
implementation-architecture ledgers. Decision IDs are cited inline (`D-*` design,
`EN-*` engineering invariants, `AD-*` implementation-architecture decisions) so a
convention can be traced to the call that set it. Where something is genuinely
undecided it is marked `> OPEN:`.

For *what* the system is and *why*, see `ARCHITECTURE.md`. For test layering and
CI gates, see `TESTING.md`. For config, secrets, and self-hosting, see
`DEPLOYMENT.md`. This file is the *how to write the code* layer.

**Hard-locked, non-negotiable:**

- **The backend is npm/node-free** — no `node`, no `package.json`, no registry
  access at build time. The whole project builds with the Go toolchain alone
  (D-32, AD-7). The frontend build runs esbuild *as a Go library*, not as an npm
  tool.
- **No media touches the server, ever** (D-23). No conventions below may
  introduce a media path.

---

## 1. Go conventions

### 1.1 Package boundaries

The module path is `github.com/rock3r/guest-pass`; Go **1.26+** is a floor, not a
pin (AD-15) — the project tracks current deps, so the floor rose to 1.25 for
`modernc.org/sqlite` and then to 1.26 for `chromedp` (the browser-test driver). Layout (AD-4):

| Package | Owns | Must not |
|---|---|---|
| `cmd/guestpass` | `main`: wire `config → store → hub → web`; serve `:443` HTTP+WS | hold business logic |
| `cmd/build` | esbuild Go-API build → `web/dist` (`go run ./cmd/build [--watch]`) | be reachable from server code |
| `internal/config` | env load into one struct; **fail-closed secrets** (EN-14, AD-8) | be mutated after load |
| `internal/store` | `modernc.org/sqlite`; migrations; repos; conn-hook + single-writer pool (EN-11) | leak `*sql.DB` / SQL strings upward |
| `internal/auth` | Google OAuth, JWT (kid two-key ring), live-DB authz middleware (EN-6), `AUTH_MODE=dev` (AD-8) | bake roles/status into the token |
| `internal/mail` | Resend HTTP, `MAIL_MODE=log`, delivery-webhook intake (EN-22) | use SMTP |
| `internal/turn` | ICE-config assembly; ephemeral HMAC TURN creds (EN-4); STUN default (D-38) | persist creds |
| `internal/signaling` | **the core** (AD-2): hub + room + conn + roster/locks/slots/epochs/frames | touch the DB on a per-frame path; write a socket outside the conn's `writeLoop` |
| `internal/web` | chi router, `html/template` render, route table, CSP/SRI/cookies (§7.5), OBS source pages | reach into another package's private state |
| `internal/livecheck` | D-29 live scraping, SSRF-closed (§7.4) | accept a raw URL (identifier + platform only) |
| `internal/jobs` | 24h PII purge + idle-session reaper tickers (§9.7) | run as an external scheduler |
| `web/` | frontend source, vendored deps, fonts, build output | be imported by Go code except via `go:embed` |

Conventions:

- `internal/` is mandatory for all non-`cmd` packages — nothing here is a public
  API. Infra is hard-locked, so do **not** add hexagonal ports/adapters: they
  would be dead weight (AD-4).
- One concern per package; do not let `web` reach into `signaling` internals or
  vice-versa. Cross-package contact is through exported types and constructor
  functions only.
- The realtime core is an **actor model** (AD-2): one goroutine owns each room's
  state; all mutations arrive on a command channel; there are **no locks on room
  state**. Do not reach into a room's fields from another goroutine — send it a
  command. Cross-room/admin reads use an atomically-published read-only snapshot;
  cross-room mutations (force-end) are commands (AD-2a).
- **WS library = `coder/websocket` (AD-16, RF-19).** The honest reasons to pick it
  are: it is actively maintained, has a context-first API, and is ergonomic.
  coder's own write self-serialization is **redundant with our own invariant, not
  additive safety** — the per-conn `writeLoop` already makes single-writer-per-conn
  structural (EN-12), so we never rely on the library to serialize writes. The
  `internal/signaling/conn` wrapper is an **in-process test seam** (lets tests
  drive a conn without a real socket), **not** a "swap the WS library later"
  abstraction; describe it that way, not as a swappable transport port.

### 1.2 Error handling

- **Wrap with `%w`** when crossing a function boundary that adds context:
  `fmt.Errorf("loading pass %s: %w", id, err)`. Preserve the chain; never
  `fmt.Errorf("...: %v", err)` for an error you want callers to inspect.
- Expose **sentinel or typed errors at package edges** so callers branch on
  `errors.Is` / `errors.As`, not on string matching. Example: `store` exports
  `ErrNotFound`; handlers map it to 404 without knowing it is SQLite.
- Keep raw infra errors (SQL text, driver errors) **inside** the package that
  produced them; translate at the edge.
- Never let a token, PII value, or `chat.text` ride inside an error string that
  may be logged (EN-16, EN-20) — see §2 logging invariants.

### 1.3 Logging — structured, leveled, JSON

Logging is **structured, leveled JSON** (DESIGN §9.6). Prefer `log/slog` with a
JSON handler. Levels: `debug` / `info` / `warn` / `error`.

**Hard invariants (these are tested, not stylistic — §7.7):**

| Rule | Why |
|---|---|
| **Never log PII** — guest `name`/`email`, host email/name never appear in a log line | D-37, EN-20 |
| **Never log tokens** — pass, slot-source, host-source, TURN creds; redact **before** the log call | EN-16 |
| **Redact token query strings** — `?src=…` and `?pass=…` on `/ws` and source-page handshakes are redacted from any logged URL/path | EN-16 |
| **Never log `{t:"chat"}.text`** — at any level; the chat relay path has no DB/file/log writer in scope | EN-20, D-26 |

- Build a single redaction helper (strip `src`/`pass` query params, mask token
  fields) and route request/WS logging through it. Do not hand-format URLs into
  log lines.
- Log identifiers, not secrets: log a `pass_id` / `slot` / `epoch`, never the
  token that authenticates them.
- The chat relay has **no logger** wired into its path — this is structural, not
  a discipline. A `slog` call inside the chat fan-out is a bug.

### 1.4 Context propagation

- `context.Context` is the **first argument** of any function that does I/O,
  blocks, or spans a request/WS lifetime: `func (s *Store) GetPass(ctx, id)`.
- Propagate the request/connection context down; never `context.Background()` in
  a handler path. Background contexts belong only to long-lived owners (the hub,
  job tickers).
- Pass cancellation through to SQLite and HTTP fetches (livecheck, Resend) so a
  dropped client or shutdown actually stops the work.

### 1.5 Configuration

- All config is **environment variables**, loaded **once** at startup into one
  immutable `Config` struct in `internal/config`. Read fields off the struct;
  never call `os.Getenv` deep in the code.
- **Fail-closed secrets (EN-14, AD-8).** The binary **refuses to start** if
  `JWT_SECRET`, `TOKEN_SECRET`, or `TURN_SECRET` is empty, equals a shipped
  placeholder, or is **shorter than 32 characters** (a trivial non-placeholder
  value would otherwise leave the HS256 cookie / token / TURN HMAC material
  brute-forceable). `TURN_SECRET` is only checked when TURN is enabled (`TURN_URL`
  set; STUN-only deployments don't need it). `TOKEN_SECRET` is the **stable** HMAC
  key for magic-link/slot/host token hashing (EN-5) — kept separate from the
  rotating `JWT_SECRET` because rotating it would orphan every stored token hash.
- **Dev-auth is build-tag-gated, not runtime-gated (RF-4, AD-8).** The dev-auth
  seam (`AUTH_MODE=dev`, which mints a fake host session) is compiled **only under
  a `//go:build dev` build tag** — it is **not present in a release binary at
  all**. This is strictly stronger than a runtime "refuse if set outside dev"
  check: there is no dev-auth code path to disable in production. "Production" =
  the default release build (no `dev` tag). When dev-auth *is* compiled in (the
  `dev` build), the binary additionally **refuses to start with a non-loopback
  `BASE_URL`**, so a dev binary cannot be pointed at a real origin.
- No silent boot on default secrets, no "warn and continue." A missing required
  var is a startup error, not a runtime surprise.
- See `DEPLOYMENT.md` for the full env-var table and required/optional split.

---

## 2. Database conventions

### 2.1 Driver and connection contract

- Driver is **`modernc.org/sqlite`** — pure Go, **no CGO**. This keeps the single
  static binary and clean cross-compilation; do **not** add the CGO `mattn`
  driver.
- Every pooled connection is opened with, applied via a **connection hook** (so
  it runs on *every* conn, not just the first — EN-11):
  - `journal_mode = WAL`
  - `busy_timeout >= 5000` (ms)
  - `foreign_keys = ON`
- **Single-writer enforcement (EN-11, RF-11) — decided:** a **writer pool with
  `db.SetMaxOpenConns(1)` plus a separate reader pool**. WAL allows concurrent
  readers, so reads never contend with the single writer. This is the chosen
  pattern (no longer an open hedge); do not introduce a second writer connection.
  A connect-storm concurrency test is **required** to prove no writer
  serialization is lost under load (see `TESTING.md`).
- **Never persist per-frame stats** (EN-11). Signal/RTT/quality telemetry stays
  in memory. The only operational write is `peers.used_turn`, written **once at
  disconnect**. A per-frame DB write is a bug.
- Keep the schema **Postgres-portable** (EN-11) — avoid SQLite-only constructs
  where a portable form exists, for a possible future backend.

### 2.2 IDs and timestamps (AD-17)

- **IDs = UUIDv4 stored as `TEXT`.**
- **Timestamps = `INTEGER` Unix-seconds, UTC.** Store absolute UTC; render local
  in the UI (EN-25). Integer seconds make expiry/purge/reaper comparisons trivial
  and remove TZ ambiguity.

### 2.3 Migrations (AD-6)

- Migrations are **numbered `*.sql` files**, embedded via `go:embed` (no external
  migration tool — zero new deps).
- **Forward-only.** No down-migrations in v1.
- **Runner state machine (RF-12, AD-6).** A `schema_version` table records the
  applied version(s) **and a per-file checksum**. On startup the runner **refuses
  to start** if either (a) an already-applied file's checksum has **drifted** from
  the recorded value (a migration was edited after the fact), or (b) the DB is in
  a **dirty/partial** state (a previous run did not complete cleanly).
- **Each migration file is applied in its own transaction (all-or-nothing).** A
  file either fully applies and records its version+checksum, or rolls back
  entirely — no half-applied file.
- **Binary-older-than-DB refuses to start** rather than corrupt state; a binary
  newer than the DB migrates up before serving (AD-6, §9.5).

### 2.4 Token storage (EN-5)

- All three token families (pass, slot-source, host-source) are stored as
  **`HMAC(server_secret, token)`** — *not* bare SHA-256. A bare hash of a
  long-lived reused secret is offline-grindable if the DB leaks; keying to a
  server secret defeats precomputation.
- **Constant-time compare** on every token check (`hmac.Equal` /
  `subtle.ConstantTimeCompare`) — no early-exit byte compare, no timing oracle.
- One active value per token at a time (§4). Rotation replaces the stored hash;
  there is no list of valid historical tokens.
- TURN creds are HMAC-*derived*, short-lived, and **not stored** (EN-4).

### 2.5 Schema relationships & new tables

The authoritative DDL lives in `ARCHITECTURE.md` §6 — reference it, do not
reproduce it here. Conventions that bind the developer:

- **`passes.slot_id` is an FK to `slots(id)`** (replaces the old bare `slot TEXT`).
  SQLite cannot express a cross-table CHECK, so two app-layer checks are **hard
  invariants** enforced when a pass is bound to a slot: (a) **same-host** — the slot
  must belong to the pass's stream's host (RF-2); and (b) **cam-only** — pass occupants
  bind only to `cam` slots, never the `host` (D-18) or shared `screenshare` (D-21) slot
  (D-20). Neither can be a DB constraint.
- **One live session per host** is enforced by `sessions.host_id` plus a **partial
  unique index** on the live-session state (EN-2). Do not open a second live
  session for a host; rely on the index as the backstop.
- **`pass_locks` persists suppression-lock state (AD-22).** Suppression locks are
  written to the `pass_locks` table so they **survive a server restart**. This is
  **not** a per-frame write — lock transitions are infrequent, so EN-11's
  no-per-frame-DB-write rule still holds. Do not move lock state onto a per-frame
  path.
- **Slot/source-token rows carry `source_token_last_used_at` +
  `source_token_last_source_ip` (AD-23, EN-5).** These are updated on use so a host
  can spot an unexpected live subscription. They are operational metadata, not a
  per-frame write; redact the IP per §1.3 logging rules where it would be logged.
- **Slot-token posture is v1 (AD-23):** the slot/source token both authenticates
  the WS handshake **and** authorizes the media leg in v1; the short-lived
  per-session media grant is **deferred to v1.1**. v1 mitigations are D-22
  rotation, the `last_used_at`/`last_source_ip` metadata above,
  `Referrer-Policy: no-referrer`, and no third-party requests on source pages
  (§3.5, §3.6).

---

## 3. Frontend conventions

The frontend is **not an SPA** (D-32). Most screens are server-rendered Go
`html/template`; only the real-time stateful surfaces ship JavaScript, as
vendored **Preact islands** mounted into a server-rendered page. There is **no
hash/SPA router** — server routes own navigation; islands mount per-page against
a known root element.

### 3.1 Render posture — what gets JS

| Surface | Rendering | JS |
|---|---|---|
| Marketing / comparison / parody, sign-in, guest pass page, dashboard / calendar / invites / sources tabs, error & state screens | Go `html/template` | none |
| Admin console | `html/template` | minimal (poll/refresh only) |
| Device check (+ guest session — its in-session phase, same page/connection/camera), greenroom | server page + **Preact island** | island |
| OBS source page (cam + screen) | minimal standalone HTML | **separate** minimal entry (EN-13) |

Server-side authz (EN-6/EN-8) is the guard; the frontend never touches the DB or
PII directly.

> **One exception (M5.5):** the host **stream-detail** page carries a *minimal,
> read-only* liveness poll. It computes its "● Live" pill once at render, so when a
> session is force-ended out from under the page (admin D-27 cascade, idle reaper,
> or an end from another tab) the pill would otherwise go stale until a manual
> refresh; the poll (`GET /api/streams/{id}/session` → `{"live":bool}`) swaps it for
> an "ended" notice **in place**. It never mutates state (the page's forms stay
> server-rendered POST), submits nothing, and reloads nothing — so the
> no-interactive-JS posture holds, same spirit as the admin console's poll/refresh.

### 3.2 Authoring islands — plain JS + JSDoc (D-32)

- Islands are authored in **plain JS with JSDoc type annotations** — **never
  `.ts`**. esbuild *transpiles, never typechecks*, and `tsc`/`tsgo` are npm-only;
  shipping `.ts` would deliver TS syntax with zero type safety, violating
  "do not weaken quality gates." JSDoc gives editor-aware, npm-free types; an
  external `tsc`/`tsgo` gate can be bolted on later without changing source.
- Treat JSDoc types as **real**, not documentation-only.
- **State management: Preact hooks only** (`useState` / `useReducer` /
  `useContext` from vendored `preact/hooks`) — AD-18, D-32. No extra state
  library.
- The **`Room` orchestrator holds canonical state** and pushes it via
  context/reducer; islands render from it (AD-18). `PeerLink` is one instance per
  remote consumer (offer/answer, ICE restart, per-link bitrate cap via
  `RTCRtpSender.setParameters()`). Keep RTC logic in `web/src/rtc/`, shared by the
  islands.
- **Mount pattern:** each island mounts once, per-page, against a known root
  element. No client-side router, no global app shell. The public `/demo` route
  mounts the *real* greenroom island with a canned read-only adapter swapped in
  for `Room` (D-43) — one greenroom, pluggable data/transport, demo = adapter,
  never a fork.

### 3.3 Vendored Preact (D-32)

- Preact is **vendored** into `web/vendor/preact/` (MIT, committed). No registry
  fetch, ever.
- The vendored file list is **exact and committed**. Adding a Preact sub-path
  requires adding **both** the file *and* its esbuild `alias` entry (§3.4).

### 3.4 Build — esbuild as a Go library, two entries (EN-13)

The build is `cmd/build` invoking esbuild's Go API (`github.com/evanw/esbuild/pkg/api`).
Two pinned, load-bearing aspects live in `BuildOptions`:

- **Vendored-Preact resolution:** an `Alias` map redirects bare specifiers
  (`preact`, `preact/hooks`, `preact/jsx-runtime`) to the exact vendored files;
  JSX mode `automatic`, `jsxImportSource: preact`. No bare `node_modules`
  resolution is ever attempted.
- **CSS target = the oldest supported GUEST browser, NOT OBS CEF-127** (EN-13).
  OBS ships CEF/Chromium 127, but the binding constraint is the *guest's* browser
  (older, more varied). Set esbuild's CSS `target` to that floor.
  - `color-mix()` caveat: esbuild's down-level rewrite has known **Safari** bugs
    and `styles-v2.css` uses it heavily — the down-leveled output gets a **manual
    Safari check in the M1 DoD** (do not assume the transpile is correct).

**Two deliberately-separate entries (EN-13):**

| Entry | Contains | Fonts |
|---|---|---|
| **App** | device-check + guest-session + greenroom islands | full self-hosted font + token CSS |
| **OBS source-page** | cam + screen source pages only — `PeerLink` + `window.obsstudio` relay + reconnect | **none** — no `@font-face`, no `system-ui` stack |

The OBS entry stays minimal so the ~412 KB font payload and full app are kept out
of up to 9 browser sources per show and out of guest phones rendering source
pages.

- **Dev loop:** esbuild watch/serve mode (npm-free), served from disk; release
  builds emit to `web/dist/` which is **gitignored** and `go:embed`-ed at release
  (AD-7). No generated bundles in git.

### 3.5 Browser hardening (DESIGN §7.5)

- **CSP (RF-10) — concrete policy.** No inline script beyond the island-bootstrap
  **per-request nonce**; no third-party origins. Template baseline:

  ```
  default-src 'none'; script-src 'self' 'nonce-<per-request>'; style-src 'self';
  connect-src 'self' wss: <turn-host?>; img-src 'self' data:; font-src 'self';
  base-uri 'none'; frame-ancestors 'none'
  ```

  `connect-src` **must include the WS origin** (the `wss:` signaling endpoint) and
  a **TURN host only when one is configured** — omit `<turn-host?>` in the
  STUN-only default. On a **non-secure (plain-HTTP) dev/loopback origin** `connect-src`
  also includes `ws:` so the signaling socket isn't blocked (some browsers don't treat
  `'self'` as covering WebSocket schemes); a production HTTPS origin stays `wss:`-only.
- **SRI (RF-10) — via build manifest.** `cmd/build` emits a **build manifest**
  mapping each emitted JS/CSS bundle to its integrity hash; templates inject the
  `integrity=` attribute from that manifest. SRI hashes are never hand-maintained.
- **Referrer / third-party leakage (RF-24, AD-23).** Serve
  `Referrer-Policy: no-referrer` **globally**, and OBS source pages make **zero
  third-party requests** — both protect the slot/source token carried in source
  URLs from referrer- and history-based leakage (ties to the §3.6 / §4
  slot-token posture).
- **Cookies:** the host JWT cookie is `httpOnly` + `SameSite` + `Secure`.

### 3.6 OBS source-page invariants (EN-15)

- The guest **display name renders as escaped `textContent` only** — never
  `innerHTML`, never interpolated into a style/attribute context. The server
  enforces a **charset + length cap** on the name.
- The source-page DOM and JS carry **zero secrets** — only an **opaque slot id**.
  Never the slot token, host email, or any pass data. The slot page authenticates
  with the slot token at the **WS handshake**; nothing sensitive lives in the
  served HTML.
- Nameplate visibility is a **show/hide URL param** (no DB column); styling is the
  host's OBS native Custom CSS against documented selectors, not a GuestPass
  control (D-16).

---

## 4. Token & secret handling

Three token families, summarized for the developer who is about to mint, store,
or compare one. Full threat model is in `ARCHITECTURE.md` / DESIGN §7.1.

| Token | Borne by | Lifetime | Stored | Sensitivity |
|---|---|---|---|---|
| **Pass token** | guest magic link `GET /p/{token}` | per-pass; PII purged 24h post-stream | `HMAC(server_secret, token)` | medium |
| **Slot source token** | OBS source URLs (cam 1–8, host, screenshare) | **permanent, reused every stream** | `HMAC(server_secret, token)` | **crown jewel** (EN-5) |
| **TURN cred** | ICE config, BYO-TURN only | 60–120s (EN-4) | not stored (HMAC-derived) | low |

Rules that apply to **all** families:

- **128-bit random, base64url.**
- **Stored hashed** as `HMAC(server_secret, token)` (§2.4) — never bare hash,
  never plaintext.
- **Constant-time compared** on validation (EN-5).
- **One active value at a time** — rotation replaces, never appends.
- **Redacted everywhere** — never logged, never in error strings, `?src=`/`?pass=`
  query strings stripped before logging (EN-16, §1.3).
- **Fail-closed** — the server won't start without real `JWT_SECRET` /
  `TURN_SECRET` (EN-14, §1.5).

Family-specific:

- **Slot source tokens are the crown jewel (EN-5):** permanent and reused, so a
  leak is a standing subscription to every future occupant. The page (`?token=`)
  and the WS (`?src=`) carry the **same secret over two transports**, never a
  second token. **v1 posture (AD-23):** the slot/source token **both
  authenticates the WS handshake and authorizes the media leg** directly; the
  short-lived per-session media grant that would decouple the standing secret from
  a live media leg is **deferred to v1.1**. v1 mitigations: D-22 rotation, the
  **`last_used_at` + `last_source_ip`** metadata per slot/source token so a host
  can spot an unexpected live subscription (EN-5), `Referrer-Policy: no-referrer`,
  and no third-party requests on source pages (§3.5).
- **Guests never see slot/source URLs** — pass auth and slot auth are disjoint;
  the role-filtered roster (EN-8) never ships a source URL to a guest *or*
  co-host (D-15).
- **Rotation tears down live subscriptions.** The host "my URLs leaked" panic
  button rotates **all** slot tokens at once (per-slot rotation also available);
  the old URL stops working mid-show (D-22). Pass regeneration is a *separate*
  family and does not touch slot tokens.

---

## 5. File placement

Mirror the AD-4 tree and the `web/` layout exactly. Place new code where its
concern already lives; do not invent sibling packages.

```
cmd/guestpass/        main: wire + serve
cmd/build/            esbuild Go-API build → web/dist
internal/
  config/             env load + fail-closed secrets
  store/              sqlite; repos; migrations/  (numbered *.sql, go:embed)
  auth/               OAuth + JWT + live-DB authz mw + AUTH_MODE=dev
  mail/               Resend HTTP + MAIL_MODE=log + webhook intake
  turn/               ICE assembly + ephemeral TURN creds
  signaling/          hub.go room.go conn.go frames.go roster.go locks.go slots.go (+ *_test.go)
  web/                handlers, html/template, route table, CSP/SRI/cookies, OBS source pages
  livecheck/          D-29 scraping (SSRF-closed)
  jobs/               24h PII purge + idle-session reaper
web/
  src/rtc/            PeerLink (consume), Publisher (publish), Room (signaling WS + ICE
                      config from the join-ack + {t:ice-refresh}), getStats sampling
  src/islands/        device-check(+publish), greenroom grid, guest-session (APP entry)
  src/obs/            cam + screen source pages                (OBS esbuild entry — no fonts)
  src/styles/         design tokens verbatim from styles-v2.css (D-9)
  vendor/preact/      vendored Preact (MIT, committed)
  fonts/              OFL woff2 ×3 (+ each family's OFL.txt)   (EN-17)
  dist/               build output — GITIGNORED, go:embed at release (AD-7)
docs/                 ARCHITECTURE / CONVENTIONS / TESTING / DEPLOYMENT
LICENSE               AGPL-3.0 (D-31)
THIRD_PARTY_NOTICES   coturn BSD, Preact MIT, fonts OFL (EN-17)
```

Placement rules:

- **Go HTML templates** live with the `web` package; **islands** in
  `web/src/islands/`; **OBS source pages** in `web/src/obs/` (separate esbuild
  entry — no fonts).
- **Migrations** are numbered `*.sql` under `store` and embedded via `go:embed`.
- **Fonts** (3 families, woff2, ~412 KB total) live in `web/fonts/` with each
  family's `OFL.txt`. They are **not** loaded by the OBS entry (EN-13/EN-17).
- **Vendored deps** (Preact) live in `web/vendor/preact/`, committed.
- `web/dist/` is build output: **gitignored**, `go:embed`-ed only at release.
- Session plans / scratch notes go under `.plans/` (gitignored); worktrees under
  `.worktrees/` (gitignored). Do **not** commit either.

---

## 6. Commit messages

- **Plain, descriptive messages.** Describe what changed and why in prose.
- **No conventional-commit prefixes** — never `feat:`, `fix:`, `chore:`, etc.
  This is a **hard rule** (global agent rule). A commit subject is a normal
  sentence-fragment, not a typed prefix.
- Commit or push **only when the user asks**; if on `main`, branch/worktree first
  (CLAUDE.md). Opening/merging PRs and pushing need explicit approval.

---

## 7. Licensing in source

GuestPass is **AGPL-3.0** (D-31). Network use triggers §13's source-offer
obligation, and satisfying it is a **product requirement**, not just a `LICENSE`
file (EN-17):

- **Embed the build/commit ref at compile time** (e.g. `-ldflags -X`); the in-app
  source link must resolve to the **exact running build**, not just `HEAD`.
- **The source link appears in the guest / greenroom UI**, not only the host
  dashboard — **guests are §13 network users** (EN-17). A host-dashboard-only
  link is non-compliant.
- Ship `LICENSE` (AGPL-3.0) and `THIRD_PARTY_NOTICES`. License compatibility is
  clean and must stay so: **coturn** BSD (separate process), **Preact** MIT
  (vendored), **fonts** OFL 1.1 with **no Reserved Font Names** — ship each
  family's `OFL.txt`.
- Any in-bundle "open source" / license copy must read **AGPL-3.0** and must
  match the repo's actual license (D-30 ↔ D-31). If they diverge, the copy is
  wrong, not the repo.

> Note: the repo currently has `LICENSE` deleted in-tree (`D LICENSE`); the
> Apache→AGPL-3.0 relicense lands at M1 step 1 before first release (AD-I / EN-17).
