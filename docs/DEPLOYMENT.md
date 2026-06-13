# Deployment & operations

How to run GuestPass: the official public instance at **guest-pass.link** and an
AGPL self-host, the env config that distinguishes them, persistence/backup/job
operations, AGPL §13 obligations, the data-retention contract, and a
step-by-step provisioning checklist for first deploy.

> Source of truth: this doc distills the frozen design (`guest-pass-DESIGN.md`
> §8–§10, primarily §9) and the implementation-architecture ledger
> (`guest-pass-IMPL-ARCH.md`). Decision IDs are cited inline (`D-*` design,
> `EN-*` engineering invariant, `AD-*` architecture decision). Where a value is
> not yet decided it is marked `> OPEN:`.
>
> **No real secrets, hostnames, or IPs appear in this repo.** All examples use
> placeholders. Where the owner must supply a real value, the text says so and
> flags it as out-of-band.

---

## 1. Two deployment shapes from one binary (D-35)

GuestPass ships a single Go binary. The deployment *shape* is chosen entirely by
configuration — there is **no fork divergence** between the two. The public
instance is just GuestPass with `SIGNUP_MODE=open` and the abuse dials turned up.

| Shape | What it is | Who runs it | Differs only by |
|---|---|---|---|
| **Official public instance** — `guest-pass.link` | Free, no-ads, **multi-tenant**; self-service Google sign-in; progressive-trust abuse controls. Demonstrates "easy & cheap to run" — media is P2P so cost ≈ signaling + SQLite (no operator media relay). | Project maintainers | `SIGNUP_MODE=open`, abuse dials up, **no TURN** (D-38) |
| **AGPL self-host** (D-31) | Same binary, operator-owned. `SIGNUP_MODE` picks the onboarding posture; solo or small-team is the common case. AGPL §13 obliges source disclosure for network use. | Anyone | operator picks `SIGNUP_MODE`, optional TURN |

Both shapes are **media-blind by construction** (D-23) and run the identical
signaling + storage path. They differ only in tenancy, signup mode, abuse-dial
tightness, and whether a TURN relay is configured.

---

## 2. Topology (§9.1)

`docker compose` runs two services: the GuestPass binary and a coturn for
STUN. TURN **relay is off by default** (D-38) so the public instance never pays
for media relay and "cheap" stays true.

```
docker compose
┌─────────────────────────────────────────────────────────────────┐
│  guestpass    distroless Go binary (~25 MB)                       │
│               :443  HTTP + WebSocket signaling                    │
│               SQLite on a mounted volume (the entire state)       │
│               serves Go-rendered HTML + Preact islands + OBS      │
│               source pages; NO media path (D-23)                  │
│                                                                   │
│  coturn       STUN  — always on (public-IP/port discovery)        │
│               TURN  — relay OFF by default (D-38); config-flip on  │
│                       via TURN_URL/TURN_SECRET, or BYO/3rd-party  │
└─────────────────────────────────────────────────────────────────┘
```

- **coturn ships for STUN** (self-hosted, for privacy — peers discover their
  public IP/port and connect directly; ~85–90% of pairs connect direct).
- **TURN is operator-enableable** (config-flip on the bundled coturn) **or
  BYO / third-party** (`TURN_URL` + `TURN_SECRET`, e.g. a host's own coturn,
  Cloudflare, or a metered free tier). A host-run TURN can relay any
  P2P-blocked pair in the room (incl. guest↔guest); its only requirement is
  **public reachability** (UPnP / port-forward / public IP — fails on CGNAT).
- **No-TURN limitation, surfaced not hidden (D-38):** the ~8–15% of guests
  behind symmetric NAT / locked firewalls can't connect direct. The guest gets
  a clear **"your network blocks peer-to-peer"** error (suggest another network
  / hotspot) — never a silent hang.
- **No media server, ever** (D-23). The server's only media-adjacent role is the
  optional TURN packet relay, which forwards encrypted DTLS-SRTP it cannot
  decode.

> The binary terminates TLS on :443 in the compose default. Self-hosters
> fronting it with their own reverse proxy / cert manager should adjust
> accordingly; HTTPS is mandatory (Secure cookies, WebRTC, OAuth all require it).

> **SPIKE-0 is a pre-build gate (AD-24).** An encoder-feasibility spike on real
> hardware runs **before any build**; its results may **revise the published
> "~6 guests" mesh capacity / scaling guidance** before launch.

---

## 3. Configuration (§9.2)

All configuration is via environment variables. **Secrets fail closed
(EN-14):** the binary **refuses to start** if a required secret is empty or
still equals a shipped placeholder — no silent boot on default secrets.

| Var | Required | Fail-closed | Notes |
|---|---|---|---|
| `BASE_URL` | ✅ | **✅ outside dev** | e.g. `https://guest-pass.link`; origin for the OAuth redirect, magic links, and OBS source URLs. Must match the registered Google redirect host. **Must be `https://` outside dev** — the binary refuses a non-HTTPS `BASE_URL` in production (Secure cookies / WebRTC / OAuth all require HTTPS); `AUTH_MODE=dev` uses a loopback `http://` origin instead. |
| `GOOGLE_CLIENT_ID` | ✅ | — | Google is the only host identity provider. |
| `GOOGLE_CLIENT_SECRET` | ✅ | — | Paired with the client id; supply out-of-band (see checklist §12). |
| `JWT_SECRET` | ✅ | **✅ (EN-14)** | HS256 host-session cookies; the current signing key. Part of a **`kid` + two-key ring** so rotation isn't a global logout (EN-6). Refuses placeholder/empty/short. |
| `JWT_SECRET_PREVIOUS` | optional | **✅ when set** | The retired, **verify-only** second key during a `JWT_SECRET` rotation: set it to the old secret so sessions signed with it keep verifying until they expire, then remove it. Empty in steady state. When set it must be a real (non-placeholder, ≥32-char) secret. |
| `TOKEN_SECRET` | ✅ | **✅ (EN-14)** | **Stable** HMAC key for hashing magic-link / slot / host source tokens (EN-5): only `HMAC(TOKEN_SECRET, token)` is stored, never the raw token. Kept **separate from `JWT_SECRET`** — `JWT_SECRET` rotates via the `kid` ring, but rotating the token key would orphan every stored token hash (turn off all outstanding magic links), so this one does not rotate. Refuses placeholder/empty/short. |
| `RESEND_API_KEY` | ✅ unless `MAIL_MODE=log` | — | Invite delivery over the Resend HTTP API (D-2). |
| `MAIL_FROM` | ✅ unless `MAIL_MODE=log` | — | `From` address for invite emails, e.g. `GuestPass <invites@guest-pass.link>`. Must be a Resend-verified sender. Unused (and not required) when `MAIL_MODE=log`. |
| `MAIL_MODE` | — | — | `log` prints magic links to stdout (dev / airgapped self-host); production uses Resend. Default is the Resend path. |
| `ADMIN_EMAIL` | ✅ | — | The first sign-in matching this email is auto-approved as owner/admin (`is_admin`, D-14). |
| `SIGNUP_MODE` | ✅ | — | `open` \| `approval` \| `allowlist` (see §4). Public instance = `open`. |
| `TURN_URL` | optional | — | BYO / 3rd-party TURN, or to enable the bundled coturn relay (D-38). Empty ⇒ STUN-only. |
| `TURN_SECRET` | optional¹ | **✅ when set (EN-14)** | coturn reads this to validate ephemeral-HMAC TURN credentials. Refuses placeholder/empty **if TURN is enabled**. |
| `ALLOWED_HOSTS` | optional | — | Email allowlist consulted when `SIGNUP_MODE=allowlist`. |
| `CODEC_OPTIN` | optional | — | Higher-efficiency codecs offered beyond the H.264/VP8 default (comma list, e.g. `vp9,av1,h265`); **OFF by default** — better compression at guests' CPU/battery cost, negotiated only where both peers support it (D-39). |
| `AUTH_MODE` | dev-only | **✅ in prod (AD-8)** | `dev` mints a fake host session without Google for local dev + hermetic tests (mirrors `MAIL_MODE=log`). **INVARIANT: the binary refuses to start if `AUTH_MODE=dev` outside dev** (same fail-closed posture as EN-14, AD-8). Never set in production. |
| `DB_PATH` | optional | — | SQLite file path; defaults to `guestpass.db`. In docker-compose point it at the mounted volume (§6), e.g. `/data/guestpass.db`. |

¹ `TURN_SECRET` is unrequired when STUN-only (no TURN), but **fail-closed-critical
whenever TURN is enabled**. Together with `JWT_SECRET` it is the fail-closed pair
(EN-14): both must be strong, non-placeholder values or the binary will not boot.

> OPEN: exact env-var name(s) for the TLS/cert configuration (Let's Encrypt
> account email, cert/key paths vs. ACME) are not pinned in the frozen design;
> the compose default terminates TLS on :443 but the knobs are undecided.

---

## 4. Onboarding modes (`SIGNUP_MODE`, §9.3)

`hosts.status ∈ {pending, active, suspended}` (D-28). Every protected handler
reads `hosts.status` + `is_admin` **live from the DB on each request** (EN-6),
so approve / suspend / demote take effect mid-session without re-issuing tokens;
WS host-join / rejoin gates on `status=active`.

| Mode | Behavior | Use-case |
|---|---|---|
| `open` | Google sign-in (**email-verified required**) → host immediately, `status=active`. Abuse contained by progressive trust (§5). | **Public instance** (D-36) |
| `approval` | Google sign-in creates a `pending` host; an admin approve/deny queue gates stream creation (D-28). `ADMIN_EMAIL` is auto-approved. | Curated multi-host self-host |
| `allowlist` | Only emails in `ALLOWED_HOSTS` (plus `ADMIN_EMAIL`) may become hosts. | Solo / known-team self-host |

- A `pending` host sees a "waiting for approval" state (`GET /api/me`); admins
  work the queue via `POST /api/admin/hosts/{id}/approve`.
- A `suspended` host is blocked from creating/running future streams; if one is
  live, suspend offers a cascade prompt (§5).
- The admin/owner is a host with `is_admin` set — never a separate identity
  (D-14). The first sign-in matching `ADMIN_EMAIL` becomes that owner.

---

## 5. Public-instance abuse posture (D-36, D-42)

The public instance is an **email relay on `guest-pass.link`** — a phishing /
spam and deliverability-reputation risk. These guards are **mandatory** on a
public instance; self-hosters inherit the same machinery but run it slack
(`allowlist` / `approval`).

- **Progressive trust:** new hosts get small invite / email / concurrent-stream
  quotas that grow with account age + good standing; tightest for new accounts;
  templated/limited email content. (TURN quota is moot on the public instance —
  STUN-only, no operator relay, D-38.)
  > OPEN: the exact starting quota numbers and growth curve are "tuned at
  > launch" (D-42) — not fixed in the frozen design.
- **Email hardening (mandatory):** **SPF / DKIM / DMARC** on `guest-pass.link`,
  a `List-Unsubscribe` header on invites, and tight per-host invite rate limits.
- **Abuse reporting (D-42, EN-24):** every invite email **and** the guest pass
  page carries a **"didn't expect this? report it"** link → a public, no-auth
  report form requiring **both a category** (spam / don't-know-them / phishing /
  harassment / other) **and a free-text message**. Each submission is an
  individual report `{id, host_id, reporter_email, category, message,
  stream_id, created_at}`. Admins review them grouped per host, then **Dismiss
  all** or **Suspend host**. **Reporter identity is admin-only** — the host
  never sees who reported them (retaliation guard); reporter email + message are
  retained only for the review window, then anonymized (D-37).
- **One-click suspend cascade (D-14 / D-27):** an admin suspends a host (blocks
  future stream creation/running); if the host has a live session, an **"end the
  running live too?"** cascade prompt offers one-click suspend + force-end +
  slot-token invalidation in a single action.
- **Token-scanning defense:** per-IP rate limits on `GET /p/{token}` and any
  token-validation endpoint; constant-time compares mean no timing oracle on
  top.
- **Slot / source-token leak recovery (AD-23, D-22):** in v1 the **permanent
  slot/source tokens authorize the media leg** (per-session grants land in v1.1).
  If a source URL leaks, the operator/host recovery path is the **panic button**:
  **rotate-all** (every slot token for the session) or **per-slot rotation**,
  which invalidates the leaked token and forces OBS to re-pull the new one.
  **`last_used_at` / `last_source_ip` metadata** on each slot/source is the
  signal to spot an unexpected subscription (a source pulled from an IP the host
  doesn't recognize) before deciding to rotate. Source pages also send
  **`Referrer-Policy: no-referrer`** so the token in the URL never leaks via the
  `Referer` header to any third-party resource a page loads.
- **SSRF guard on live-verification (RF-9, D-29):** live-verify is the one place
  the server makes an outbound request to an operator-influenced URL. It uses a
  **custom validating dialer** that **pins the resolved IP to the actual dial**
  (no TOCTOU re-resolve) and **blocks private / loopback / link-local / cloud
  metadata ranges, including IPv6-mapped forms, across redirects**. Operator note
  only — full spec lives in ARCHITECTURE / CONVENTIONS; no operator config knob.

---

## 6. Persistence, migrations, backups (§9.5)

The **entire durable state is one SQLite file** on the mounted volume. Live
room/session state is in-memory only and rebuilt on reconnect (AD-3); chat is
**never** in the DB (relay-only, EN-20), so backups carry no backstage content.

### SQLite contract (EN-11)

- Open with `journal_mode=WAL`, `busy_timeout >= 5000`, `foreign_keys=ON`,
  applied **via a connection hook** (every pooled conn, not just the first).
- Enforce **single-writer**: a **writer pool `db.SetMaxOpenConns(1)` + a separate
  reader pool** (WAL allows concurrent readers) — decided, not a hedge (RF-11).
- **Never persist per-frame stats** — `peers.used_turn` is written **once at
  disconnect**; signaling/RTT telemetry stays in memory.

### Migrations (AD-6)

- Embedded (`go:embed` numbered `*.sql`), **forward-only and idempotent**,
  applied **on startup, each file in its own transaction**. Version + a **per-file
  checksum** are tracked in a `schema_version` table; the runner refuses to start on
  checksum drift of an applied file or a dirty/partial state (full contract in
  `CONVENTIONS.md` §2.3, RF-12).
- A binary **newer** than the DB migrates up before serving; a binary **older**
  than the DB **refuses to start** rather than corrupt state. Plan rollbacks as
  roll-forward — there is no down-migration path.

> **RF-20 — migration rollback is restore-from-backup, not down-migration.** The
> runner is **forward-only** and an older binary refuses to boot against a newer
> DB, so a bad deploy that ran a migration **cannot be undone in place**. The
> only rollback is **restore a pre-deploy snapshot + redeploy the prior binary**.
> Therefore: **take a backup immediately before every deploy that includes a
> migration** (WAL-consistent `VACUUM INTO` / `.backup` — see Backups below) and
> keep it until the new version is confirmed healthy. **Rollback procedure:**
> stop the new binary → restore that snapshot → deploy the prior binary → start.

### Backups

- Back up with an **online-safe, WAL-consistent copy** — `VACUUM INTO` or the
  SQLite `.backup` API — **never a raw `cp` mid-write** (a naive copy can capture
  a torn WAL state).
- **Pre-migration backup is mandatory (RF-20):** because rollback = restore, the
  snapshot taken right before a migrating deploy is the *only* recovery path if
  that migration goes bad. Automate it in the deploy step, not as an afterthought.

  ```sh
  # online-safe snapshot of the live DB (illustrative)
  sqlite3 /data/guestpass.db "VACUUM INTO '/backups/guestpass-$(date +%F).db'"
  ```

- Since the whole state is the one file on the volume, snapshotting that file
  (via the safe copy above) is the entire backup story. No external database,
  no media store, no chat archive to back up.

---

## 7. Observability (§9.6)

- **Structured logs** — leveled JSON. **No PII, no chat text, ever (EN-20):**
  `{t:"chat"}.text` is never logged; the chat relay path has no DB/file writer
  in scope. Guest name/email never hit logs. **Tokens are redacted from logs
  (EN-16)** — pass, slot, and `?src=`/`?pass=` query tokens redacted before any
  log line, including `/ws` and source-page handshakes.
- **`/ws` Origin handling tolerates the null Origin** an OBS CEF browser source
  sends (EN-16), without weakening Origin checks for host/guest connections.
  One-connection-per-identity eviction + reconnect rate-limit bound scanning and
  connection-storm abuse.
- **Admin console metrics are live / transient only:** active sessions + peers,
  TURN-relay %, force-end / suspend controls. Endpoints return **metadata only,
  never media or chat** (D-14, §7.7 of DESIGN).
- **Long-term retention is anonymous aggregates only** (streams run,
  guest-minutes, TURN-relay %, uptime) — never per-person-trackable (D-37).
  Prefer increment-only counters over retained event logs.
- **No persisted audit log in v1** (D-26) — privileged actions are visible live
  (the host sees co-host moderation in the roster; admin actions show a notice).
  A persisted metadata-only trail is deferred to v1.1.

### Process lifecycle & ops primitives (RF-21)

- **`/healthz` readiness endpoint.** Returns OK only once the binary has applied
  migrations and is ready to serve; used by the docker-compose healthcheck and
  any LB / reverse proxy in front of the instance.
- **SIGTERM graceful drain.** On shutdown the binary **sends
  `{t:terminate,reason:"reconnect"}` to all connected WS peers** (reusing the
  terminate taxonomy so clients reconnect cleanly rather than seeing a hard
  socket drop), **finishes in-flight DB writes**, then exits. Without this, every
  deploy / restart is an ungraceful mass-drop of live rooms.
- **Rate-limiter / progressive-trust-quota state is in-memory and per-instance
  (AA-1).** It is **not** persisted, so it **resets on restart** — a deploy
  clears accumulated rate-limit and quota counters. Acceptable for the
  single-instance v1; **known limitation**, revisited if/when the abuse state
  needs to survive restarts or span multiple instances.

### Mail health (EN-22)

Mail health is **measured, not guessed**: the binary instruments its own Resend
submit latency + API error rate **and** consumes Resend **delivery webhooks**
(`email.sent` / `delivered` / `delivery_delayed` / `bounced` / `complained`) for
true time-to-delivered and bounce/complaint rate. The admin "mail degraded"
signal is derived from these. GuestPass does **not** claim inbox-vs-spam
placement (no provider reports it reliably).

---

## 8. Background jobs (§9.7)

Both jobs are **in-binary tickers** — no external scheduler / cron is required.

| Job | Trigger | Action |
|---|---|---|
| **24h PII purge (D-37)** | scheduled hourly sweep | Delete guest PII (pass `name` + `email`) **24h after stream end** — no retention reason. For streams that never ran, ≤24h after scheduled-end + grace. Long-term data reduced to anonymous aggregates. Guests are told **before** (invite / device-check) and **after** (leave / session-ended screens) — a copy requirement (§11), not optional. |
| **Idle-session reaper (D-40)** | per-session timer | Zero connected peers for **N minutes** → auto-end the session, freeing the one-live-session-per-host slot. |

> OPEN: the idle-reaper threshold `N` minutes is not pinned in the frozen design.

### Session lifecycle (D-40)

- **Host opening the greenroom = session start.** Early guests see a "host not
  arrived" auto-refresh waiting screen.
- The room **persists through host disconnects** — the host auto-reconnects with
  WS backoff and resumes; co-hosts keep moderation in the gap but cannot assume
  host or end the session.
- **Intentional end** = explicit **"End session"** (host), **admin force-end**
  (D-25), or the **reaper**. Reconnects key on the stable `pass_id` (EN-3) so
  OBS sources auto-reattach.

### One-live-session-per-host (§9.8, D-20)

A host may have **at most one live session at a time** in v1 so slot→occupant
source URLs resolve unambiguously. Starting a greenroom while one is already
live is refused (or ends the prior); the admin sessions view is explicitly
cross-host because one host owning two concurrent live rows is a bug, not a
feature (EN-2). Concurrent shows / layouts → v1.1.

---

## 9. TURN credential hygiene (when TURN is enabled, §9.9)

Only relevant if a relay is configured (`TURN_URL`/`TURN_SECRET`); the public
instance runs none (D-38). coturn HMAC creds have **no per-credential
revocation** (EN-4), so:

- **Short credential TTL — 60–120s.** Clients re-request over the **revocable
  signaling WS** (ICE config rides the WS join-ack, AD-14), so a kicked peer
  loses relay access within the TTL.
- On kick / force-end, optionally tear down the allocation via coturn admin.
- Set coturn **quotas** (total / per-user / max-bps) regardless, to bound relay
  abuse (EN-4).

---

## 10. AGPL §13 compliance (D-31, EN-17)

GuestPass is **AGPL-3.0**; network use triggers §13's source-offer obligation.
The running binary must let its network users reach the corresponding source.
These are **product requirements**, not just legal text:

- **Embed the build / commit ref at compile time.** The in-app source link must
  resolve to the **exact running version**, not just `HEAD`.
- **The source link appears in the guest / greenroom UI**, not only the host
  dashboard — **guests are §13 network users too**, so a host-only link is
  insufficient.
- **Ship the license/notice files:**
  - `LICENSE` = **AGPL-3.0** (relicensed from Apache-2.0 before first release;
    fix any lingering "MIT licensed" copy in the bundle to AGPL-3.0).
  - `THIRD_PARTY_NOTICES`.
  - Per-font **`OFL.txt`** for each family (Newsreader, Schibsted Grotesk,
    Spline Sans Mono — all SIL OFL 1.1 with no Reserved Font Names).
- **License compatibility is clean:** coturn (BSD, runs as a separate process),
  Preact (MIT, vendored), fonts (OFL 1.1). The marketing license claim must
  match the repo's actual license — if they diverge, the page is wrong, not the
  repo (D-30 ↔ D-31).

> Repo note: `LICENSE` is currently deleted in the working tree and must be
> recreated as AGPL-3.0 (tracked as AD/track-I in the impl-arch ledger).

---

## 11. Data retention summary (§8)

The operator of a **public instance is a GDPR data controller** for every host's
guests' emails (D-35) and **must publish a privacy policy** that points at this
contract. The backstop principle is **bare-minimum data** — minimal *data*, not
just minimal *PII*.

| Data | Classification | Lifetime |
|---|---|---|
| Guest pass name + email | PII | **Deleted ≤24h after stream end** (D-37) — no retention reason |
| Invited-guest emails (on a host's streams) | PII | Same 24h post-stream purge |
| Host Google id / email / name | PII | Lifetime of the host account (host-deletable) |
| Host's planned/scheduled streams | PII-linked | Lifetime of the host account |
| Session / peer operational rows | transient | Live + a brief window, then purged or reduced to anonymous aggregates |
| Backstage chat | **never stored** | Relayed over WS only; never in DB or logs (EN-20) |
| Media | **never stored** | P2P; never transits or is processed server-side (D-23) |
| Per-frame / per-link stats | not persisted | `used_turn` written once at disconnect (EN-11) |
| High-level counters + anonymized diagnostics | anonymous | Long-term, aggregate-only, never per-person-trackable |

### Guest PII — 24h auto-purge, no self-service deletion

There is **no guest self-service deletion**: auto-purge makes a guest erasure
endpoint moot (the data is gone in a day, and there is no guest account to
authenticate against). The obligation is met by **transparency** — a **copy/UX
requirement** (not optional), telling guests before and after:

| When | Surface | Notice |
|---|---|---|
| Before | Invite email + device-check screen | "Your name and email are only used for this stream and are deleted within 24 hours of it ending." |
| After | Leave screen + session-ended screen | "This stream's over. Your name and email will be deleted within 24 hours." |

### Host self-service rights (host-facing in v1)

The host is the only meaningfully non-anonymous data subject, and the surface is
tiny — so full GDPR self-service is offered in v1:

| Right | Mechanism | Scope |
|---|---|---|
| **Export** (portability) | `GET /api/me/export` — account "takeout", one download | The host PII surface (account + scheduled streams + invited-guest emails) |
| **Amend** (rectification) | `PATCH /api/me` — in-app account edit | The same set |
| **Delete** (erasure) | `DELETE /api/me` — "delete my account" | Account **+ all the host's data** (streams, passes, any not-yet-purged guest PII) |

Self-hosters inherit the same job and defaults; the privacy-policy obligation
applies to whoever operates a public-facing instance.

---

## 12. Owner provisioning checklist

Step-by-step the owner runs **before / at first deploy**. Real values
(client secrets, API keys, IPs) are supplied **out-of-band** and must **never**
be committed to this repo.

### 12.1 Google OAuth client

1. In Google Cloud Console, create an **OAuth 2.0 client** (Web application).
2. Set the authorized redirect URI to **`BASE_URL/auth/google/callback`**
   (e.g. `https://guest-pass.link/auth/google/callback`).
3. Capture the **client id** → `GOOGLE_CLIENT_ID` and **client secret** →
   `GOOGLE_CLIENT_SECRET`. Supply the secret out-of-band (env/secret store).
4. On the sign-in screen, GuestPass uses Google's real "Sign in with Google" /
   One Tap (Google Identity Services / FedCM) — no custom popup (EN-21); the
   client id above is what wires it up.

### 12.2 Resend (email delivery)

> Skip this entire step for dev/airgapped self-host by setting `MAIL_MODE=log`
> (magic links print to stdout).

1. Create a **Resend** account.
2. **Verify the sending domain** (`guest-pass.link`, or the self-host's domain).
3. Configure **SPF / DKIM / DMARC** DNS records as Resend instructs (mandatory
   for the public instance, §5).
4. Set up **delivery webhooks** so GuestPass receives `email.sent` /
   `delivered` / `delivery_delayed` / `bounced` / `complained` events
   (EN-22) — this drives the admin "mail degraded" signal.
5. Capture the **API key** → `RESEND_API_KEY` (out-of-band).

### 12.3 Generate strong secrets

1. Generate a strong random **`JWT_SECRET`** (e.g. `openssl rand -base64 48`).
2. Generate a strong random **`TOKEN_SECRET`** (the stable magic-link/slot/host
   token-hashing key, EN-5). Unlike `JWT_SECRET`, **do not rotate it** — rotating
   would orphan every stored token hash and turn off all outstanding magic links.
3. If enabling TURN, generate a strong random **`TURN_SECRET`** (coturn reads it
   for ephemeral-HMAC credential validation).
4. **Fail-closed reminder (EN-14):** the binary **refuses to start** if any of
   `JWT_SECRET` / `TOKEN_SECRET` / (when TURN is on) `TURN_SECRET` is empty,
   equals a shipped placeholder, or is **shorter than 32 characters**.
   `openssl rand -base64 48` (64 chars) clears this comfortably; set real values
   before first boot. `JWT_SECRET` supports rotation via the `kid` two-key
   ring (EN-6, not a global logout); `TOKEN_SECRET` is intentionally fixed.

### 12.4 GitHub repo + CI secrets

1. Configure repo settings and **CI secrets** for any deploy automation
   (deploy keys, registry creds, the env values above as needed).
2. CI runs `go test`, `go vet`, `staticcheck`, `govulncheck`, `gofmt -l`, and
   the esbuild build (AD-11) — keep these gates green; do not weaken them.

### 12.5 DNS (public instance launch)

`guest-pass.link` currently serves the coming-soon page from **GitHub Pages**
(apex A records → Pages IPs). **At app launch:**

1. **Repoint the apex** away from GitHub Pages to the **app host**.
2. Serve **HTTPS via the app's own cert / Let's Encrypt** (not Pages).
3. Ensure `BASE_URL` matches the live origin and the Google redirect URI (§12.1).

> Do **not** commit the real Pages IPs or the app-host IP to this repo — supply
> them out-of-band at deploy time (repo rule).

### 12.6 Instance policy

1. Decide **`SIGNUP_MODE`** for the instance (`open` for the public instance;
   `approval` / `allowlist` for curated/solo self-host).
2. Set **`ADMIN_EMAIL`** — the **first matching sign-in becomes the owner/admin**
   (`is_admin`, D-14). For `allowlist`, also populate `ALLOWED_HOSTS`.
3. Decide whether to enable TURN (`TURN_URL`/`TURN_SECRET`) or stay STUN-only
   (D-38). Public instance: stay STUN-only.
4. **Public-instance operators must publish a privacy policy** (GDPR controller,
   §11).
