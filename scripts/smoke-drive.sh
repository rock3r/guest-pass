#!/usr/bin/env bash
#
# smoke-drive.sh — headful, browser-driven multi-guest smoke (dev-only). Spins up N fake-media guest
# tabs + the host greenroom + an OBS source tab, binds a cam slot, and walks the grid / RF-8 / on-air
# flow ON SCREEN (a guest is forced off camera → the host tile AND the OBS source go black, then
# release restores them), saving a screenshot at each step. No guest juggling; needs a local Chrome.
#
# Usage: scripts/smoke-drive.sh [--guests N] [--watch SECONDS] [--headless]
#   --headless   capture the screenshots without popping browser windows
set -euo pipefail

GUESTS=3
WATCH=45
HEADLESS=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --guests) [[ $# -ge 2 ]] || { echo "missing value for --guests" >&2; exit 2; }; GUESTS="$2"; shift 2 ;;
    --guests=*) GUESTS="${1#*=}"; shift ;;
    --watch) [[ $# -ge 2 ]] || { echo "missing value for --watch" >&2; exit 2; }; WATCH="$2"; shift 2 ;;
    --watch=*) WATCH="${1#*=}"; shift ;;
    --headless) HEADLESS=1; shift ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) echo "unknown flag: $1 (try --help)" >&2; exit 2 ;;
  esac
done

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"
SHOTS="$REPO_ROOT/.smoke/drive-shots"

export SMOKE_DRIVE=1 SMOKE_GUESTS="$GUESTS" SMOKE_WATCH_SEC="$WATCH" SMOKE_SHOTS="$SHOTS"
[[ "$HEADLESS" == 1 ]] && export SMOKE_HEADLESS=1

echo "headful smoke driver: $GUESTS guests → grid / RF-8 / on-air, screenshots in $SHOTS"
echo "(builds the frontend, launches the browsers, drives the flow, then holds the windows ${WATCH}s)"
go test -tags browser -run TestSmokeDrive_MultiGuest ./internal/browsertest/ -v -count=1 -timeout 20m

echo "screenshots: $SHOTS"
command -v open >/dev/null 2>&1 && open "$SHOTS" 2>/dev/null || true
