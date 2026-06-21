# Third-party notices

GuestPass is licensed under **UEL v1.0** (see `LICENSE`). It includes or depends on the
third-party components below. This list is finalized as dependencies land; entries marked
**(planned)** are designed-for but not yet vendored/imported. License compatibility with
UEL v1.0 is affirmed per `docs/ARCHITECTURE.md` and `docs/DEPLOYMENT.md` (D-31).

## Bundled / vendored (shipped in the binary or repo)

- **Preact** (planned) — MIT — vendored under `web/vendor/preact/` when the frontend
  lands (D-32); no registry fetch at build time.
- **Newsreader**, **Schibsted Grotesk**, **Spline Sans Mono** fonts (planned) —
  SIL Open Font License 1.1, **no Reserved Font Names** — each family ships its
  `OFL.txt` (D-9, EN-17).

## Go module dependencies (planned)

| Module | License |
| --- | --- |
| `github.com/go-chi/chi/v5` | MIT |
| `github.com/coder/websocket` | ISC |
| `modernc.org/sqlite` | BSD-3-Clause |
| `golang.org/x/oauth2` | BSD-3-Clause |
| `github.com/golang-jwt/jwt/v5` | MIT |
| `github.com/google/uuid` | BSD-3-Clause |
| `github.com/evanw/esbuild` | MIT (build-time library, via `cmd/build` / `internal/assets`) |
| `github.com/chromedp/chromedp` | MIT (test-time only, via `internal/browsertest`; not in the binary) |

Exact licenses are verified at import time; full texts are included alongside vendored
components as they are added.

## Separate processes (not linked)

- **coturn** — BSD-3-Clause — runs as a separate STUN/TURN process via docker-compose;
  not linked into the binary (D-38).
