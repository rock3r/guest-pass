# Manual smoke runbook

The chromedp suite proves protocol/plumbing, not the real transport (RF-7). This runbook
covers the **manual** gates that can't be automated, with a one-command harness to make them
as smooth as possible:

- **Real OBS-CEF media receive** — an actual OBS browser source renders a guest (AD-10 / RF-17).
- **Multi-machine / real-network NAT traversal** — guests across separate networks exercise
  real ICE (RF-7); STUN-only direct connects (D-38).
- **~6-guest capacity + live degradation** — load the mesh and watch shedding/recovery (AD-21).
- **RF-8 receiver-side suppression-lock enforcement** — a force really suppresses a guest on
  air and on peers' tiles, independent of the guest's client.
- **Safari / mobile guest** rendering (RF-7).

It is **dev-only**: `AUTH_MODE=dev` + the `dev` build tag. Nothing here ships in a release build.

---

## Prerequisites

- **Go** (the repo toolchain) and a checkout of this repo.
- **A tunnel tool** so phones / other machines / Safari reach the loopback dev server over a
  secure origin (browser camera/mic require a secure context):
  - `cloudflared` — **recommended, no account needed**: `brew install cloudflared`
  - or `ngrok` — `brew install ngrok && ngrok config add-authtoken <token>`
- **`qrencode`** (optional, for phone QR codes): `brew install qrencode`
- **OBS ≥ 31 (CEF 127)** on a machine, for the real-OBS-CEF receive check.
- A **second device** (phone on cellular is ideal) for the real-network / Safari checks.

---

## Start it

From the repo root:

```sh
scripts/smoke.sh                 # 6 guests + a co-host + a screenshare slot, public tunnel
scripts/smoke.sh --guests 4      # fewer guests
scripts/smoke.sh --no-tunnel     # same-machine only (cameras work on localhost, not other devices)
```

What it does: generates + persists dev secrets in `.smoke/` (gitignored), uses a **fresh** SQLite
DB, builds the frontend, opens a public HTTPS tunnel, starts the dev server (STUN-only,
`MAIL_MODE=log`), then prints a **link dashboard** and an interactive bind prompt. Ctrl-C tears the
server + tunnel down.

> The server stays on a loopback `BASE_URL` (which `AUTH_MODE=dev` requires); the **printed links
> use the tunnel URL**. The client opens its WebSocket from `window.location`, so the
> loopback-server / tunnel-link split is invisible to guests.

The dashboard gives you, for the host: a **sign-in** link (`/auth/dev`) and the **greenroom**; for
each participant: a **guest link** (+ QR), an **OBS source URL**, and the pass id; plus the
**screenshare** source URL.

---

## Walkthrough

1. **Guests join.** Open each guest link on a device/tab (scan the QR on phones), allow camera/mic,
   click **Enter the greenroom**, wait for "your camera is live in the greenroom". The co-host link
   joins as a co-host. Spread guests across machines/networks for the NAT checks.
2. **Host watches.** Open the **sign-in** link, then the **greenroom**.
3. **Bind for OBS.** Press **Enter** in the `smoke.sh` terminal to bind every cam slot to its
   participant. Add one or more **OBS source URLs** as Browser Sources (1280×720).

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

### 3. ~6-guest capacity + degradation (AD-21 / AC-15)
- [ ] With ~6 guests connected, the mesh stays usable; tiles keep rendering.
- [ ] Under CPU/bandwidth pressure, **degrading/recovering badges** appear on tiles (host + co-host
      see all, read-only; a guest sees only its own).
- [ ] The host **"Bump quality now"** control recovers shed senders immediately.

### 4. RF-8 — suppression locks really suppress (D-13 / EN-7 / RF-8) ★ the new bit
For a guest who is **on a bound OBS source** and **visible as a thumbnail** to other participants:
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

### 5. Backstage chat privacy (EN-20)
- [ ] Backstage chat relays between participants and shows the "not recorded — off the record"
      note. (It is never written to disk — that's the server-tested invariant; nothing to inspect.)

### 6. Real-network / NAT (RF-7 / D-38)
- [ ] A guest on a **different network** (e.g. phone on cellular) still connects and renders.
- [ ] STUN-only: a guest behind **symmetric NAT / a locked firewall** may get the clear "your
      network blocks peer-to-peer" message instead of a silent hang — **expected without TURN**.

### 7. Safari / mobile (RF-7)
- [ ] A guest on **Safari** and on a **phone** joins (via the tunnel link / QR) and renders.

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

- **Dev-only / STUN-only.** This harness mints a dev host session without Google and serves over a
  tunnel for convenience; it is not a deployment. To exercise the **TURN relay** path (the ~10%
  symmetric-NAT case), run a coturn and set `TURN_URL`/`TURN_SECRET` (see `DEPLOYMENT.md` §2, §9) —
  out of scope for this STUN-only harness.
- See `docs/TESTING.md` §5 for why these gates are manual, and `docs/ARCHITECTURE.md` (RF-8, §7)
  for the suppression-lock receiver-side contract this smoke confirms on real OBS + peers.
