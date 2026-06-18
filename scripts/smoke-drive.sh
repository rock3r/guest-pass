#!/usr/bin/env bash
#
# smoke-drive.sh — headful, browser-driven smoke (dev-only). Needs a local Chrome; no guest juggling.
#
# Default (multi-guest + RF-8): spins up N fake-media guest tabs + the host greenroom + an OBS source
# tab, binds a cam slot, and walks the grid + RF-8 flow ON SCREEN (a NON-cooperating guest is forced
# off camera → the host tile AND the OBS source go black, then release restores them).
#
# --screenshare (M4): a guest auto-shares its screen (stubbed animated capture), the host preview rail
# shows it, the driver selects it live, and a second guest renders the live share — all on screen.
# It then PRINTS a /s/screen?token=… URL and holds the windows open so you paste it into a real OBS
# Browser Source and confirm OBS-CEF renders the moving test pattern (the one manual gate).
#
# (On-air + degradation are covered by the chromedp suite, not this driver.)
#
# Usage: scripts/smoke-drive.sh [--screenshare] [--guests N] [--watch SECONDS] [--headless]
#   --screenshare  drive the M4 screenshare flow (rail → select-live → everyone) + print the OBS URL
#   --headless     capture the screenshots without popping browser windows
set -euo pipefail

GUESTS=3
WATCH=""
HEADLESS=0
MODE=multiguest
while [[ $# -gt 0 ]]; do
  case "$1" in
    --screenshare) MODE=screenshare; shift ;;
    --guests) [[ $# -ge 2 ]] || { echo "missing value for --guests" >&2; exit 2; }; GUESTS="$2"; shift 2 ;;
    --guests=*) GUESTS="${1#*=}"; shift ;;
    --watch) [[ $# -ge 2 ]] || { echo "missing value for --watch" >&2; exit 2; }; WATCH="$2"; shift 2 ;;
    --watch=*) WATCH="${1#*=}"; shift ;;
    --headless) HEADLESS=1; shift ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "unknown flag: $1 (try --help)" >&2; exit 2 ;;
  esac
done

# Default hold: the screenshare run gives you time to set up OBS; the multi-guest run is shorter.
if [[ -z "$WATCH" ]]; then
  if [[ "$MODE" == screenshare ]]; then WATCH=180; else WATCH=45; fi
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"
SHOTS="$REPO_ROOT/.smoke/drive-shots"

export SMOKE_DRIVE=1 SMOKE_GUESTS="$GUESTS" SMOKE_WATCH_SEC="$WATCH" SMOKE_SHOTS="$SHOTS"
# Set/clear explicitly so an inherited SMOKE_HEADLESS can't force headless on a headful run.
if [[ "$HEADLESS" == 1 ]]; then export SMOKE_HEADLESS=1; else unset SMOKE_HEADLESS; fi

if [[ "$MODE" == screenshare ]]; then
  TESTNAME=TestSmokeDrive_Screenshare
  echo "headful smoke driver: M4 screenshare (rail → select-live → everyone) + OBS /s/screen URL, screenshots in $SHOTS"
else
  TESTNAME=TestSmokeDrive_MultiGuest
  echo "headful smoke driver: $GUESTS guests → multi-guest grid + RF-8, screenshots in $SHOTS"
fi
echo "(builds the frontend, launches the browsers, drives the flow, then holds the windows ${WATCH}s)"
# Derive the go-test timeout from the watch hold + a generous flow budget (build + guests + flow),
# so a long --watch isn't killed by a fixed -timeout before the watch window completes.
GO_TIMEOUT=$((WATCH + 900))
go test -tags browser -run "$TESTNAME" ./internal/browsertest/ -v -count=1 -timeout "${GO_TIMEOUT}s"

echo "screenshots: $SHOTS"
command -v open >/dev/null 2>&1 && open "$SHOTS" 2>/dev/null || true
