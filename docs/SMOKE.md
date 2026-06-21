# Manual smoke runbook

The chromedp suite proves protocol/plumbing, not the real transport (RF-7). This runbook
covers the **manual** gates that can't be automated, with a one-command harness to make them
as smooth as possible:

- **Real OBS-CEF media receive + on-air** — an actual OBS browser source (CEF 127) renders a guest,
  and bringing it on-program lights the on-air pill via the real OBS event (AD-10 / RF-17 / D-24).
- **Multi-machine / real-network NAT traversal** — guests on separate networks connect over real
  ICE. This smoke is **STUN-only**: a direct connection is the gate. A **TURN relay is OFF by
  default** (D-38) and isn't run here, so symmetric-NAT / locked-firewall pairs won't connect (TURN
  is an optional/BYO self-host config — see `DEPLOYMENT.md` §2 — not part of this smoke).

The behavioral checks (multi-guest grid, RF-8 detach, on-air *state*, degradation) are
**browser-automated** — by the headful driver below and the chromedp suite in CI — so they need no
real machines. **Not tested here:** mobile / non-Chrome incl. **Safari** (deferred from v1 — see
`TESTING.md` §5), and **~6-guest capacity** (already proven by **SPIKE-0** on real hardware, AD-24/
AD-21 — not re-tested).

It is **dev-only**: `AUTH_MODE=dev` + the `dev` build tag. Nothing here ships in a release build.

> ### Shortcut: don't hand-juggle the multi-guest + RF-8 check
>
> To watch the **multi-guest grid + RF-8** flow without juggling, run the **headful driver** — it
> launches N fake-media guest tabs + the host greenroom + an OBS source tab, binds a cam slot, and
> drives it on screen: a **non-cooperating** guest (one that keeps sending) is forced off camera →
> the host tile **and** the OBS source go black (genuinely consumer-side detach), then release
> restores them. A screenshot is saved at each step:
>
> ```sh
> scripts/smoke-drive.sh --guests 3       # headful; --headless for screenshots only
> ```
>
> What the driver does **not** cover: it renders the OBS source but never fires
> `obsSourceActiveChanged`, so it does **not** exercise the **on-air pill**, and it injects no
> shedding, so it does **not** exercise **degradation** — those are covered headless by the chromedp
> suite (`onair_browser_test.go`, `degradation_browser_test.go`) in CI, not by this driver. The only
> **physical** residue a fake-media browser on one machine can't prove stays a manual pass (the
> checklist below): the real **OBS app** (CEF) rendering + its real on-air event, and real
> cross-network **NAT** (STUN-only). (Mobile/non-Chrome is deferred from v1; ~6-guest capacity is
> already proven by SPIKE-0 — neither is tested here.)

> ### Shortcut: the M4 screenshare check (real OBS on `/s/screen`)
>
> To confirm a guest's **screen share** reaches a real OBS source without juggling a sharer + host +
> viewer, run the **screenshare driver**. It auto-drives the whole preview-switcher flow on screen —
> guest 1 shares its screen (a **stubbed, animated** `getDisplayMedia` capture, so there's no real
> OS screen-picker), the host greenroom shows it in the **preview rail**, the driver clicks **Put
> live**, and guest 2 **renders the live share** (D-21/AC-11) — then it **prints a `/s/screen?token=…`
> URL** and holds the windows open so you point a real OBS Browser Source at it:
>
> ```sh
> scripts/smoke-drive.sh --screenshare         # headful; --watch N for a longer OBS-setup hold (default 180s)
> ```
>
> **Confirm in OBS:** add a **Browser Source** with the printed URL (1280×720) → it renders the
> moving colour-cycling test pattern (CEF consuming the live share over the **screen channel**). Then,
> in the host greenroom window, click **Take screen off air** → the OBS source goes **black**
> (slot-unbound → the source clears). Append `&name=1` to the URL to also confirm the nameplate.
>
> What the driver does **not** need to cover: the in-browser render + select-live + everyone-renders +
> re-select-swap are all proven headless in CI by `screenshare_media_browser_test.go` (T-12) — the
> only **physical** residue is the real **OBS-CEF** render of `/s/screen`, which is the manual step
> above (the same CEF path as the cam `/s/{slot}` check below, just on the screen channel). The screen
> capture here is a synthetic pattern; a real desktop/window share is the same media path (D-41).

> ### ⚠ Security: dev instance over a tunnel
>
> The harness exposes a **dev instance** over a public HTTPS tunnel. The dev sign-in `/auth/dev`
> grants a **host/admin** session, and a guest link unavoidably reveals the tunnel origin — so a
> guest could otherwise trim `/p/<token>` to `/auth/dev` and take over (the loopback guard passes
> for tunnel-proxied requests, and `SIGNUP_MODE`/Google do **not** gate `/auth/dev`). To prevent
> that, **the tunnel points at a path-allowlist proxy** (`cmd/smokeproxy`) that forwards **only** the
> guest journey (`/p`, `/ws`, `/static`) and **refuses `/auth/dev`, the greenroom, the admin API, and
> the OBS source pages `/s`** — so neither a host/admin capability nor a slot **source token** ever
> traverses the tunnel (OBS runs on the host machine over loopback). The **host signs in on loopback**
> (`localhost`) on the machine running `smoke.sh`. (`/auth/dev?as=host2` mints a second, **non-admin**
> dev host for the local admin-gate smoke below; it is equally loopback-only and the same path-block
> keeps it off the tunnel.) It's still a throwaway dev instance — don't reuse
> the tunnel URL beyond the smoke, and **Ctrl-C to tear it down** when done.

---

## Prerequisites

- **Go** (the repo toolchain) and a checkout of this repo.
- **A tunnel tool** so a second machine on another network reaches the loopback dev server over a
  secure origin (browser camera/mic require a secure context):
  - `cloudflared` — **recommended, no account needed**: `brew install cloudflared`
  - or `ngrok` — `brew install ngrok && ngrok config add-authtoken <token>`
- **`qrencode`** (optional, for scannable guest-link QR codes): `brew install qrencode`
- **OBS ≥ 31 (CEF 127)** on the host machine, for the real-OBS-CEF receive check.
- A **second machine on a different network** for the real-network NAT check.

---

## Start it

From the repo root:

```sh
scripts/smoke.sh                 # 6 guests + a co-host + a screenshare slot, public tunnel
scripts/smoke.sh --guests 4      # fewer guests
scripts/smoke.sh --no-tunnel     # same-machine only (cameras work on localhost, not other devices)
```

What it does: generates + persists dev secrets in `.smoke/` (gitignored), uses a **fresh** SQLite
DB, builds the frontend, starts the dev server (STUN-only, `MAIL_MODE=log`), opens a public HTTPS
tunnel **in front of a guest-routes-only proxy** (so `/auth/dev` + host pages aren't exposed — see
the security note above), then prints a **link dashboard** and an interactive bind prompt. Ctrl-C
tears the server + proxy + tunnel down.

> The server stays on a loopback `BASE_URL` (which `AUTH_MODE=dev` requires); the **guest links use
> the tunnel URL**. The client opens its WebSocket from `window.location`, so the loopback-server /
> tunnel-link split is invisible to guests.

The dashboard has three sections: **Host** (open on **this machine** — loopback `localhost`): the
**sign-in** link (`/auth/dev`) and the **greenroom**. **Guest links** (the public tunnel URLs + QRs):
the **only** links to hand out — one per guest. **OBS source URLs** (loopback, **host-only**): paste
into OBS on this machine — their slot **source token is a credential (EN-5), so never share them** (a
holder could impersonate that OBS source). The tunnel proxy refuses `/s/` for the same reason.

---

## Walkthrough

1. **Guests join.** Open each guest link in a Chrome tab (or on a second machine for the NAT check),
   allow camera/mic, click **Enter the greenroom**, wait for "your camera is live in the greenroom".
   The co-host link joins as a co-host.
2. **Bind for OBS.** Press **Enter** in the `smoke.sh` terminal to bind every cam slot to its
   participant. **Do this before opening the greenroom** (next step): the bind connects briefly as
   the host (peer `host`), and one-connection-per-identity (EN-16) means it would kick an open
   greenroom page offline. For a guest who joins *after* you've opened the greenroom, press Enter to
   re-bind, then **refresh the greenroom**.
3. **Host watches.** On **this machine**, open the loopback **sign-in** link, then the **greenroom**
   (loopback keeps `/auth/dev` off the tunnel; the host needs no camera). Every guest tile renders —
   the grid shows guests **without** binding; binding only drives the OBS source + the on-air pill.
   Force/release from the greenroom tiles uses the page's own socket, so it does **not** evict it.
4. **OBS.** On **this machine**, add a bound **OBS source URL** (loopback, from the host-only section
   — its token is a credential, don't share) as a Browser Source (1280×720).

---

## Checklist (what to confirm)

### 1. Multi-guest grid (D-10 / AC-10)
- [ ] The greenroom renders **every** guest tile with live video over P2P (one tile each,
      role-filtered — no host/OBS tiles).
- [ ] Each guest's **in-session** view shows its own self-view + the **backstage thumbnails** of
      the others (the guest↔guest mesh).

### 2. Real OBS-CEF media receive + on-air (D-24 / D-41 / AD-10)
- [ ] An **OBS source URL** added as a Browser Source renders the bound guest's camera in OBS.
- [ ] The source carries the guest's **audio** (mic rides the cam source, D-41) — not muted/silent.
- [ ] Bringing that source **on-program** lights the guest's **on-air pill** (greenroom + the
      guest's own pill); taking it off-program clears it; with no source the pill is
      "status-unavailable" (never asserted without a live signal).

### 3. RF-8 — suppression locks really suppress on real OBS + peers (D-13 / EN-7 / RF-8)
The driver already proves this headless; this confirms it on the **real OBS-CEF output**. For a guest
**on a bound OBS source** and **visible as a thumbnail** to other participants:
- [ ] **Force-mute** the guest (from the host greenroom tile, or a co-host's guest-session tile):
  - [ ] their **OBS source goes silent** (audio track muted on air),
  - [ ] their **thumbnail on other participants** goes silent,
  - [ ] the lock notice ("Muted by host") shows on the moderator's tile and the guest's session.
- [ ] **Force-off-camera** (force-no-cam): their **OBS source goes black** and their **thumbnail
      goes black** for everyone — even though the guest's own camera is still on locally.
- [ ] **Release** restores audio/video on the OBS source and the thumbnails.
- [ ] **Force-no-share** shows the "Screen share stopped by host" notice (M3 screenshare is
      moderation-only; live screen media + the `/s/screen` render are M4 / D-21, so that source
      won't show video in this smoke).

> The *non-cooperating-publisher* angle (a tampered client that keeps sending) is covered by the
> automated browser tests; this manual pass confirms the same detach holds on the **real OBS-CEF
> output** and **real peer browsers**.

### 4. Backstage chat privacy (EN-20)
- [ ] Backstage chat relays between participants and the guest session shows the "not recorded —
      off the record" note; the host **greenroom** shows the matching "Backstage chat is never
      recorded" trust line. (It is never written to disk — that's the server-tested invariant;
      nothing to inspect.)

### 5. Real-network / NAT — STUN-only (RF-7 / D-38)
- [ ] A guest on a **second machine on a different network** connects and renders — STUN gives the
      reflexive candidates for a direct P2P path through the common cone-NAT types (the v1 gate).
- [ ] A pair behind **symmetric NAT / a UDP-blocking firewall won't connect** with this STUN-only
      smoke: a **TURN relay is OFF by default** (D-38) and not run here (it's an optional / BYO
      self-host config, `DEPLOYMENT.md` §2). The guest client now **detects** this (the
      `ConnectivityWatch` watchdog, D-38): within ~20 s of no P2P connection ever forming, it replaces
      the false "you're live" with the **"Your network blocks peer-to-peer video"** screen — different
      network / phone-hotspot guidance + a **Retry** — instead of a silent hang. **Confirm that screen
      appears** (a silent hang, or a stuck "you're live" with no media reaching OBS/peers, is a finding
      to file). Test mainly from ordinary networks (not both symmetric); the automated
      `netblocked_browser_test.go` already forces the relay-only path headless, so this manual pass
      just confirms it on a real blocked network.

---

## Troubleshooting

- **Camera blocked / "not allowed":** the page must be a secure origin. Use the **tunnel URL**
  (https), not the raw `localhost`/LAN address, on any device that isn't this machine.
- **No tunnel URL printed:** install `cloudflared` (no account) or configure an `ngrok` authtoken;
  see `.smoke/tunnel.log`.
- **Server won't start / `:8137` in use:** stop the other process (`lsof -iTCP:8137`); the harness
  refuses to start if the port is taken. Server logs: `.smoke/server.log`.
- **A guest tile is blank in OBS but fine in the greenroom:** make sure you pressed **Enter** to
  bind after that guest entered; re-press Enter to re-send the binds.
- **Start clean:** the harness already uses a fresh DB each run. To also rotate the dev secrets,
  delete `.smoke/` and re-run.

---

## Notes

- **Dev-only / STUN-only smoke.** This harness mints a dev host session without Google and serves
  over a tunnel for convenience; it is not a deployment. It runs **no TURN relay** — relay is **OFF
  by default** (D-38), so the ~10–15% symmetric-NAT / locked-firewall pairs are an accepted,
  un-relayed limitation in this smoke. TURN itself is a supported **optional / BYO** self-host config
  (`TURN_URL`/`TURN_SECRET` + a coturn, `DEPLOYMENT.md` §2/§9) — it's simply not exercised here.
- See `docs/TESTING.md` §5 for why these gates are manual, and `docs/ARCHITECTURE.md` (RF-8, §7)
  for the suppression-lock receiver-side contract this smoke confirms on real OBS + peers.

---

## Local M5 gate: admin suspend-cascade (two dev hosts, no remote deploy)

The D-27 **admin suspend-cascade** — plus the §7.7 metadata-only boundary and the non-admin
`/admin` 403 — needs a SECOND, non-admin host, which the single always-admin dev identity can't
provide. **`/auth/dev?as=host2`** mints a distinct non-admin active host (`host2@localhost`), so the
whole gate runs on **one local instance with no tunnel and no Google**: `localhost` is a secure
context, so a same-machine guest's camera works over loopback.

Run a `dev` binary that includes this seam (e.g. `scripts/smoke.sh --no-tunnel` from a checkout that
has it). Use **three separate browser contexts** (distinct cookie jars — e.g. a normal window, an
incognito window, and a second profile):

1. **Admin (A):** open `http://localhost:8137/auth/dev` → active + admin host.
2. **Host B:** open `http://localhost:8137/auth/dev?as=host2` → a distinct **non-admin** host. In B's
   `/app`: create a stream → invite a guest → **Go live**.
3. **Guest:** open B's guest link (`/p/<token>`) on `localhost`, allow camera, **Enter the greenroom**
   → B now has a live session with a connected peer.
4. **A → `/admin` → Live sessions:** B's session lists host name/email, stream title, and a participant
   **count** — never B's guest names/emails or any media/chat.
5. **A → Hosts → B's row:** tick **"end live session now"** (shown only because B is live) → **Suspend**.

Confirm:
- [ ] B is blocked from starting a new stream (suspended).
- [ ] B's session is force-ended — the guest gets a terminal "session ended" reason and the session
      drops off `/admin` Live sessions.
- [ ] A never saw B's guest PII or backstage media/chat (metadata only).
- [ ] Signing in as B (non-admin) and opening `/admin` → **403**.
- [ ] A cannot suspend its **own** account (`error=self`).

`?as=host2` is **dev-build-only** and **loopback-only** (same guard as the primary dev login); the
smoke proxy's path-block keeps `/auth/dev` — including this variant — off any tunnel.
