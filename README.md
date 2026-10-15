# guest-pass

GuestPass is a free, self-hosted WebRTC guest greenroom and browser-source system.
Hosts bring remote guests into OBS without plugins or guest accounts. Guests open
an invite link in desktop Chrome, check their devices, and join the greenroom.
Hosts assign guests to reusable OBS browser sources and control the broadcast in OBS.
GuestPass is source-available under the UEL v1.0.

## Documentation

**Using GuestPass** — the user guide (hosts + guests) is served by the app at
**`/guide`** and authored in [`docs/user-guide/`](docs/user-guide/).

**Running GuestPass** — operator docs live in the repo:

- [`docs/SELF-HOSTING.md`](docs/SELF-HOSTING.md) — what it is and how to stand it up (start here).
- [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) — full deploy reference: docker-compose, environment variables, TLS, TURN, email, backups, migrations.
- [`docs/ADMIN.md`](docs/ADMIN.md) — admin console, moderation, abuse reports, and quotas.

**Contributing / internals** — [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md),
[`docs/CONVENTIONS.md`](docs/CONVENTIONS.md), and [`docs/TESTING.md`](docs/TESTING.md).
