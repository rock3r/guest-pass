# Admin & moderation guide

Operating notes for **instance administrators** — the people who run a GuestPass
deployment and keep it healthy. This document lives in the repository on purpose:
it is **not** part of the public user guide served at `/guide`, because it
describes cross-host moderation that only operators should see.

For deploying, configuring, and self-hosting the binary, see
[DEPLOYMENT.md](DEPLOYMENT.md). This guide covers what an admin *does* once the
instance is running.

## What "admin" means

Admin is a flag on a host account, not a separate kind of user. An admin is an
ordinary host (they can run their own streams) who additionally sees the **admin
console** and can act on other hosts.

- The **`ADMIN_EMAIL`** configured at deploy designates the bootstrap admin —
  the account that signs in with that address gets the admin flag, so there is
  always someone who can promote others.
- Admins promote or demote other hosts from the console.
- **Self-suspend and self-demote are blocked.** An admin can't lock themselves
  (or the instance) out, and the last remaining admin can't be removed.

## Onboarding mode

`SIGNUP_MODE` decides how new hosts get in (full details in
[DEPLOYMENT.md](DEPLOYMENT.md)):

- **`open`** — anyone can sign in with Google and start hosting. The public
  instance posture; lean on the abuse controls below.
- **`approval`** — new hosts land in a *pending* state and an admin approves
  them from the console before they can host.
- **`allowlist`** — only pre-listed addresses can sign in at all.

## The admin console (`/admin`)

The console is a **metadata-only** snapshot of the instance. By design it shows
no media and no chat — GuestPass never has access to either. You'll see:

- **Live activity** — how many sessions are live and how many peers are
  connected right now (counts only).
- **Hosts** — the host roster with status, with the actions below.
- **Abuse reports** — guest reports grouped by reported host.

### Acting on hosts

- **Approve** a pending host (in `approval` mode) to let them start hosting.
- **Suspend** a host to stop them hosting. If the host is **currently live**,
  the console offers a **"+ end live now"** cascade that force-ends the running
  session immediately and disconnects its peers — use it when something abusive
  is happening on air right now.
- **Promote / demote** the admin flag on another host.

Suspension is reversible; it gates a host out (they see an explanatory screen)
without deleting their data.

## Abuse reports

Any guest who receives an invite they didn't expect can **report** it from their
pass landing page. Reports surface on the admin console, grouped per reported
host, newest first.

- The **reporter's email is visible only on the console**, never to the reported
  host, so reporting is safe.
- Reporter and message are **anonymized** once the retention window passes, so
  old reports don't keep personal data around indefinitely.
- Use the pattern of reports against a host (volume, categories) to decide
  whether to suspend.

## Progressive-trust quotas

To keep an open instance healthy without manual policing, brand-new accounts get
**lower caps** on invites and concurrent streams that **grow as the account
ages**. This blunts drive-by abuse (a fresh account can't blast thousands of
invites) while legitimate hosts ramp up automatically. The exact caps and
age curve are configurable per deployment — see [DEPLOYMENT.md](DEPLOYMENT.md).

## Data retention

What an admin should know about what the instance keeps:

- **No media, no chat logs** — ever. Backstage chat is relayed live and never
  written down.
- **Guest PII** (name + email on a pass) is **purged within 24 hours** of a
  stream ending, automatically.
- Hosts can export or delete their own account data themselves (the GDPR
  controls in their Settings); an admin does not need to do this for them.

Everything else the instance stores is operational metadata (accounts, streams,
slot tokens, ephemeral session state). Keep this guide and `DEPLOYMENT.md` in
sync with the code if these behaviors change.
