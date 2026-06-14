#!/usr/bin/env bash
#
# smoke.sh — one-command manual-smoke harness for GuestPass (dev-only).
#
# Brings up everything the real-machine / real-network manual smoke needs:
#   - generates + persists dev secrets (.smoke/env), uses a FRESH SQLite DB each run
#   - builds the frontend bundle
#   - opens a public HTTPS tunnel (cloudflared, or ngrok) so guests on phones / other
#     machines / Safari can reach the loopback dev server over a secure origin (cameras
#     require a secure context; the server stays on a loopback BASE_URL as AUTH_MODE=dev
#     requires — the client opens its WebSocket from window.location, so this just works)
#   - starts the dev server (AUTH_MODE=dev, MAIL_MODE=log, STUN-only)
#   - seeds + prints the link dashboard via cmd/devsmoke (guests, co-host, screenshare,
#     OBS sources, QR codes) and lets you bind the cam slots on demand
#   - tears the server + tunnel down on exit
#
# Usage:
#   scripts/smoke.sh [--guests N] [--no-cohost] [--no-screenshare] [--no-qr] [--no-tunnel]
#
#   --no-tunnel   serve on http://localhost:8137 only (same-machine sanity check; cameras
#                 work on localhost but NOT from other devices). Default: public tunnel.
#
# Then follow docs/SMOKE.md for the checklist (multi-guest grid, on-air, degradation, and
# the RF-8 force checks). DEV-ONLY: requires the `dev` build tag + AUTH_MODE=dev.
set -euo pipefail

PORT=8137 # the server binds :8137 (cmd/guestpass main.go); not configurable here
GUESTS=6
COHOST=1
SCREENSHARE=1
QR=1
TUNNEL=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --guests) [[ $# -ge 2 ]] || { echo "missing value for --guests" >&2; exit 2; }; GUESTS="$2"; shift 2 ;;
    --guests=*) GUESTS="${1#*=}"; shift ;;
    --no-cohost) COHOST=0; shift ;;
    --no-screenshare) SCREENSHARE=0; shift ;;
    --no-qr) QR=0; shift ;;
    --no-tunnel) TUNNEL=0; shift ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "unknown flag: $1 (try --help)" >&2; exit 2 ;;
  esac
done

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"
SMOKE_DIR="$REPO_ROOT/.smoke"
mkdir -p "$SMOKE_DIR"

# --- secrets: reuse a persisted pair, else generate (>=32 chars to clear the fail-closed gate) ---
ENV_FILE="$SMOKE_DIR/env"
# shellcheck disable=SC1090
[[ -f "$ENV_FILE" ]] && source "$ENV_FILE"
: "${JWT_SECRET:=$(openssl rand -base64 48 | tr -d '\n')}"
: "${TOKEN_SECRET:=$(openssl rand -base64 48 | tr -d '\n')}"
umask 077
cat >"$ENV_FILE" <<EOF
# Persisted dev-smoke secrets (gitignored). Delete this file to rotate.
JWT_SECRET=$JWT_SECRET
TOKEN_SECRET=$TOKEN_SECRET
EOF

# --- fresh room every run: a clean DB means exactly one live stream + no stale slots ---
DB_PATH="$SMOKE_DIR/smoke.db"
rm -f "$DB_PATH" "$DB_PATH-shm" "$DB_PATH-wal"

export AUTH_MODE=dev MAIL_MODE=log BASE_URL="http://localhost:$PORT"
export JWT_SECRET TOKEN_SECRET DB_PATH
export STUN_URL="${STUN_URL:-stun:stun.l.google.com:19302}" # public STUN for dev; override via env
# Required even in dev (config.validateRequired). allowlist (no ALLOWED_HOSTS) only blocks Google
# self-signup — it does NOT gate /auth/dev, which bypasses Google + SIGNUP_MODE and grants an admin
# session. Over the tunnel /auth/dev IS reachable (the loopback guard passes for proxied requests),
# so the tunnel URL is effectively a secret — see the SECURITY note in docs/SMOKE.md. The host signs
# in over loopback (devsmoke prints loopback host links), not the tunnel.
export ADMIN_EMAIL="${ADMIN_EMAIL:-dev@localhost}"
export SIGNUP_MODE="${SIGNUP_MODE:-allowlist}"

# --- teardown: kill the server + tunnel on any exit ---
PIDS=()
cleanup() {
  trap - EXIT INT TERM
  echo
  echo "tearing down smoke (server + tunnel)…"
  local pid
  for pid in "${PIDS[@]:-}"; do [[ -n "${pid:-}" ]] && kill "$pid" 2>/dev/null || true; done
  sleep 1
  for pid in "${PIDS[@]:-}"; do [[ -n "${pid:-}" ]] && kill -9 "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT INT TERM

# --- preflight: refuse to start if :8137 is already taken (a stale server / another smoke) ---
if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "!! something is already listening on :$PORT — stop it first (lsof -iTCP:$PORT)." >&2
  exit 1
fi

echo "building frontend bundle…"
go run ./cmd/build

echo "building dev binaries…"
go build -tags dev -o "$SMOKE_DIR/guestpass" ./cmd/guestpass
go build -tags dev -o "$SMOKE_DIR/devsmoke" ./cmd/devsmoke

# --- public HTTPS tunnel (so phones / other machines / Safari can reach the loopback server) ---
PUBLIC_BASE_URL="http://localhost:$PORT"
start_tunnel() {
  if command -v cloudflared >/dev/null 2>&1; then
    echo "opening cloudflared quick tunnel…"
    cloudflared tunnel --url "http://localhost:$PORT" >"$SMOKE_DIR/tunnel.log" 2>&1 &
    PIDS+=("$!")
    local url i
    for i in $(seq 1 60); do
      url="$(grep -Eo 'https://[a-z0-9-]+\.trycloudflare\.com' "$SMOKE_DIR/tunnel.log" | head -1 || true)"
      [[ -n "$url" ]] && { PUBLIC_BASE_URL="$url"; return; }
      sleep 0.5
    done
    echo "!! cloudflared did not report a URL in 30s; see $SMOKE_DIR/tunnel.log" >&2
    exit 1
  elif command -v ngrok >/dev/null 2>&1; then
    echo "opening ngrok tunnel…"
    ngrok http "$PORT" >"$SMOKE_DIR/tunnel.log" 2>&1 &
    PIDS+=("$!")
    local url i
    for i in $(seq 1 60); do
      url="$(curl -fsS http://127.0.0.1:4040/api/tunnels 2>/dev/null | grep -Eo 'https://[a-z0-9.-]+\.ngrok[a-z0-9.-]*' | head -1 || true)"
      [[ -n "$url" ]] && { PUBLIC_BASE_URL="$url"; return; }
      sleep 0.5
    done
    echo "!! ngrok did not report a URL; is an authtoken configured? see $SMOKE_DIR/tunnel.log" >&2
    exit 1
  else
    cat >&2 <<'EOF'
!! No tunnel tool found. Install one (cloudflared needs no account):
     brew install cloudflared
   or use ngrok:
     brew install ngrok && ngrok config add-authtoken <token>
   Or run on this machine only (no remote devices):
     scripts/smoke.sh --no-tunnel
EOF
    exit 1
  fi
}
[[ "$TUNNEL" == 1 ]] && start_tunnel
export PUBLIC_BASE_URL

# --- start the server, wait for readiness (/healthz is registered after migrations, RF-21) ---
echo "starting server on http://localhost:$PORT …"
"$SMOKE_DIR/guestpass" serve >"$SMOKE_DIR/server.log" 2>&1 &
PIDS+=("$!")
for i in $(seq 1 60); do
  if curl -fsS "http://localhost:$PORT/healthz" >/dev/null 2>&1; then break; fi
  if [[ "$i" == 60 ]]; then
    echo "!! server did not become healthy in 30s; see $SMOKE_DIR/server.log" >&2
    exit 1
  fi
  sleep 0.5
done

if [[ "$TUNNEL" == 1 ]]; then
  echo "tunnel: $PUBLIC_BASE_URL  (guests open this; the server stays on localhost)"
  echo "  ⚠ SECURITY: anyone with this URL can reach /auth/dev and claim the host/admin session."
  echo "    Share it with guests only, and Ctrl-C to tear it down when the smoke is finished."
else
  echo "no tunnel: links use $PUBLIC_BASE_URL (cameras only work on this machine)"
fi

# --- seed + print the dashboard, then the interactive bind loop (foreground) ---
DEVSMOKE_FLAGS=(--guests "$GUESTS")
[[ "$COHOST" == 0 ]] && DEVSMOKE_FLAGS+=(--cohost=false)
[[ "$SCREENSHARE" == 0 ]] && DEVSMOKE_FLAGS+=(--screenshare=false)
[[ "$QR" == 0 ]] && DEVSMOKE_FLAGS+=(--qr=false)

"$SMOKE_DIR/devsmoke" "${DEVSMOKE_FLAGS[@]}"
