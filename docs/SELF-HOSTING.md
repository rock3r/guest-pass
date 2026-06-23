# Self-hosting GuestPass

An orientation for running your own GuestPass instance. This document lives in
the repository on purpose — it is **not** part of the public user guide served at
`/guide`, since it's operator material and you need the repo to self-host anyway.

The **authoritative, step-by-step instructions** — docker-compose file, full
environment-variable reference, TLS, TURN, backups, and migrations — are in
[DEPLOYMENT.md](DEPLOYMENT.md). Moderation and abuse handling are in
[ADMIN.md](ADMIN.md). This page is the high-level shape and a map to those.

## What you're running

A single **Go binary**: signaling relay + SQLite + the embedded frontend and OBS
source pages. No separate app server, no database to administer, no media server.
`docker compose` runs two things:

- the **GuestPass binary**, and
- a **coturn** for STUN (public-address discovery).

Media is **peer-to-peer**, so the server never carries video — which is why a
small box can host real streams cheaply.

## One binary, two postures

You don't fork or build a different edition — the deployment *shape* is pure
configuration, chosen with `SIGNUP_MODE`:

- **`open`** — anyone can sign in with Google and host (the public-instance
  posture). Lean on the built-in abuse controls (see [ADMIN.md](ADMIN.md)).
- **`approval`** — new hosts wait for an admin to approve them.
- **`allowlist`** — only addresses you list can sign in at all.

## TURN is off by default

Out of the box you're **STUN-only** — no media relay, nothing extra to pay for.
You can enable a **TURN relay** (the bundled coturn, or a bring-your-own /
third-party one) for guests behind strict firewalls; it's a config flip. Details
in [DEPLOYMENT.md](DEPLOYMENT.md).

> Note the product direction: longer term, TURN is intended to be **host-provided**
> (a host attaches their own relay credentials to their account), not something an
> operator runs for everyone. The public `guest-pass.link` instance deliberately
> stays STUN-only.

## What you'll need

- A **host or VM** with Docker, and a **domain with TLS** — GuestPass requires an
  `https://` `BASE_URL` outside dev.
- **Google OAuth credentials** (`GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET`) —
  host sign-in runs through Google.
- A way to **send guest invite emails**, plus a verified `MAIL_FROM` sender.
  Point `SMTP_HOST` at any SMTP relay (Brevo, Gmail, Mailgun — STARTTLS on `587`
  or implicit TLS on `465`) to use the SMTP backend, or supply `RESEND_API_KEY`
  to use Resend instead; if neither is set you can run `MAIL_MODE=log` for local
  testing, which prints the magic link to the server log. Full setup and the
  backend-selection rules are in [DEPLOYMENT.md](DEPLOYMENT.md).
- Strong, random `JWT_SECRET` and `TOKEN_SECRET` (and `TURN_SECRET` if you enable
  TURN). **Secrets fail closed** — the server refuses to start on an empty, short,
  or placeholder secret rather than booting half-secure.

See [DEPLOYMENT.md](DEPLOYMENT.md) for the complete variable reference and a
worked compose example.

## Data retention

Guest PII (a pass's name + email) is **purged within 24 hours** of a stream
ending, automatically. No media is ever stored and backstage chat is never
recorded. Everything else is operational metadata. The full retention contract is
in [DEPLOYMENT.md](DEPLOYMENT.md).
