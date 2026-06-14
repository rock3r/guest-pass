//go:build dev

// devsmoke seeds the fixtures the MANUAL SMOKE needs (which have no host UI yet), prints a full
// link dashboard, and binds the cam slots to the participants on demand. Everything is seeded under
// the LOCAL DEV HOST (`dev-local-host`, the same identity /auth/dev signs you in as — so the
// greenroom and every OBS source resolve to ONE room):
//
//   - a live stream,
//   - N guest passes, each with its own cam slot (cam-1, cam-2, …),
//   - optionally a co-host pass (joins AS a co-host so you can moderate from the guest-session),
//   - optionally a /s/screen source slot + one screenshare-eligible guest (can_screen).
//
// It prints the greenroom URL, each guest/co-host magic link (+ pass id), and each OBS source URL,
// then on Enter sends host {t:rebind} frames to bind every cam slot to its participant (re-send
// after guests connect, or to re-bind).
//
// PRINTED links use $PUBLIC_BASE_URL (or -public-base) when set — e.g. the smoke launcher's public
// HTTPS tunnel URL — falling back to $BASE_URL. The SERVER keeps a loopback BASE_URL (which
// AUTH_MODE=dev requires, RF-4); the client opens its signaling WS from window.location, not
// BASE_URL, so the loopback-server / tunnel-link split just works for phones and other machines.
//
// DEV-ONLY: it mints a host session straight from JWT_SECRET, so it is compiled only under `-tags
// dev` and refuses to run unless AUTH_MODE=dev. Run it with the SAME env as the server — see
// scripts/smoke.sh, which wires the whole thing up.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/config"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func ptr[T any](v T) *T { return &v }

// participant is one seeded pass plus the cam slot bound to it (the OBS source for that slot).
type participant struct {
	name      string
	role      string // guest | cohost
	canScreen bool
	passRaw   string // magic-link token (/p/{passRaw})
	passID    string // == the signaling peer id; the rebind occupant
	camLabel  string // cam-N (the slot this participant is bound to)
	srcRaw    string // that cam slot's source token (/s/{camLabel}?token=…)
}

func run() error {
	guests := flag.Int("guests", 6, "number of plain guest passes to seed (each gets a cam slot)")
	cohost := flag.Bool("cohost", true, "also seed a co-host pass (joins as co-host) + a cam slot")
	screenshare := flag.Bool("screenshare", true, "also seed a /s/screen source slot + mark one guest can_screen")
	qr := flag.Bool("qr", true, "render a QR code per guest link via `qrencode` if it is installed")
	publicBaseFlag := flag.String("public-base", "", "base URL for PRINTED links (overrides $PUBLIC_BASE_URL); falls back to $BASE_URL")
	flag.Parse()

	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config (use the same env as the server): %w", err)
	}
	if cfg.AuthMode != "dev" {
		return errors.New("devsmoke is local-dev only — set AUTH_MODE=dev (run the server the same way)")
	}

	// The links guests actually open. Server keeps a loopback BASE_URL; the printed base may be a
	// public tunnel URL so phones / other machines can reach it (the WS rides window.location).
	publicBase := strings.TrimRight(firstNonEmpty(*publicBaseFlag, os.Getenv("PUBLIC_BASE_URL"), cfg.BaseURL), "/")

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening store %q: %w", cfg.DBPath, err)
	}
	defer func() { _ = st.Close() }()

	hasher, err := token.NewHasher(cfg.TokenSecret)
	if err != nil {
		return fmt.Errorf("token hasher: %w", err)
	}

	camsNeeded := *guests
	if *cohost {
		camsNeeded++
	}
	if camsNeeded < 1 {
		return errors.New("nothing to seed: -guests must be >= 1 (or enable -cohost)")
	}
	if camsNeeded > 8 {
		return fmt.Errorf("need %d cam slots but only cam-1..8 exist — lower -guests (capacity target is ~6)", camsNeeded)
	}

	// The same host /auth/dev signs you in as, so the greenroom + OBS sources share one room.
	host, err := st.GetHostByGoogleSub(ctx, "dev-local-host")
	if errors.Is(err, store.ErrNotFound) {
		host, err = st.CreateHost(ctx, store.CreateHostParams{
			GoogleSub: "dev-local-host", Email: "dev@localhost", Name: "Dev Host",
			Status: store.HostActive, IsAdmin: true,
		})
	}
	if err != nil {
		return fmt.Errorf("dev host: %w", err)
	}

	stream, err := st.CreateStream(ctx, store.CreateStreamParams{
		HostID: host.ID, Title: "Manual smoke", Status: store.StreamLive,
	})
	if err != nil {
		return fmt.Errorf("create stream: %w", err)
	}

	// Build the participant list: N guests, then the co-host (if any). The first guest is the
	// screenshare-eligible one when -screenshare is set.
	var parts []participant
	for i := 0; i < *guests; i++ {
		parts = append(parts, participant{
			name:      fmt.Sprintf("Guest %d", i+1),
			role:      store.RoleGuest,
			canScreen: *screenshare && i == 0,
		})
	}
	if *cohost {
		parts = append(parts, participant{name: "Co-host", role: store.RoleCohost})
	}

	// Allocate a cam slot + a pass for each participant. Cam idx starts at the lowest free index so
	// repeated runs against the same DB don't collide on the unique (host_id, idx) cam constraint.
	camIdx, err := freeCamIdx(ctx, st, host.ID)
	if err != nil {
		return err
	}
	for i := range parts {
		p := &parts[i]
		p.passRaw, err = token.Mint()
		if err != nil {
			return err
		}
		pass, err := st.CreatePass(ctx, store.CreatePassParams{
			StreamID: stream.ID, Name: ptr(p.name), Role: p.role,
			TokenHash: hasher.Hash(p.passRaw), CanScreen: p.canScreen, Status: store.PassSent,
		})
		if err != nil {
			return fmt.Errorf("create pass %q: %w", p.name, err)
		}
		p.passID = pass.ID

		p.srcRaw, err = token.Mint()
		if err != nil {
			return err
		}
		idx := camIdx
		camIdx++
		if _, err := st.CreateSlot(ctx, store.CreateSlotParams{
			HostID: host.ID, Kind: store.SlotCam, Idx: ptr(idx), SourceTokenHash: hasher.Hash(p.srcRaw),
		}); err != nil {
			return fmt.Errorf("create cam slot %d: %w", idx, err)
		}
		p.camLabel = fmt.Sprintf("cam-%d", idx)
	}

	// Optional shared screenshare source slot (/s/screen). M3 screenshare is moderation-only — this
	// lets you exercise force-no-share's lock notice; live screen media + screen-select are M4 (D-21).
	var screenSrcRaw string
	if *screenshare {
		screenSrcRaw, err = token.Mint()
		if err != nil {
			return err
		}
		if _, err := st.CreateSlot(ctx, store.CreateSlotParams{
			HostID: host.ID, Kind: store.SlotScreenshare, SourceTokenHash: hasher.Hash(screenSrcRaw),
		}); err != nil {
			return fmt.Errorf("create screenshare slot: %w", err)
		}
	}

	printDashboard(publicBase, stream.ID, parts, screenSrcRaw, *screenshare)
	if *qr {
		printQRCodes(publicBase, parts)
	}

	// Mint a host session JWT with the same ring the server uses, for the rebind connection.
	ring, err := auth.NewKeyRing(cfg.JWTSecret)
	if err != nil {
		return fmt.Errorf("key ring: %w", err)
	}
	session, err := ring.Issue(host.ID, time.Hour)
	if err != nil {
		return fmt.Errorf("issue host session: %w", err)
	}
	// The rebind always goes to the LOOPBACK server (cfg.BaseURL), not the public tunnel base.
	wsURL := strings.Replace(strings.TrimRight(cfg.BaseURL, "/"), "http", "ws", 1) + "/ws"

	in := bufio.NewScanner(os.Stdin)
	fmt.Print("Press Enter to bind every cam slot -> its participant (do this AFTER guests have entered; Ctrl-C to quit): ")
	for in.Scan() {
		bound := 0
		for _, p := range parts {
			if err := sendRebind(ctx, wsURL, session, p.camLabel, p.passID); err != nil {
				fmt.Printf("  %s -> %s: rebind failed: %v\n", p.camLabel, p.name, err)
				continue
			}
			bound++
		}
		fmt.Printf("  sent %d rebind(s). Each bound OBS source should render its participant; bring a source on-program to light its on-air pill.\n", bound)
		fmt.Print("Press Enter to re-send the rebinds (e.g. for guests who hadn't connected yet), Ctrl-C to quit: ")
	}
	return nil
}

// printDashboard prints the participant links + OBS source URLs + greenroom URL, using publicBase
// for everything a guest/OBS opens.
func printDashboard(base, streamID string, parts []participant, screenSrcRaw string, screenshare bool) {
	fmt.Printf("\nSmoke fixtures ready (host=dev-local-host, stream=%s).\n", streamID)
	fmt.Printf("\nHost:\n  Sign in:    %s/auth/dev   (then open the greenroom)\n  Greenroom:  %s/greenroom\n", base, base)
	fmt.Printf("\nParticipants (%d):\n", len(parts))
	for _, p := range parts {
		role := p.role
		if p.canScreen {
			role += ", can-screen"
		}
		fmt.Printf("\n  %s (%s)\n", p.name, role)
		fmt.Printf("    Guest link:  %s/p/%s\n", base, p.passRaw)
		fmt.Printf("    OBS source:  %s/s/%s?token=%s   (binds to %s)\n", base, p.camLabel, p.srcRaw, p.camLabel)
		fmt.Printf("    pass id:     %s\n", p.passID)
	}
	if screenshare {
		fmt.Printf("\nScreenshare source (M3 = moderation-only; live screen media is M4/D-21):\n")
		fmt.Printf("    OBS source:  %s/s/screen?token=%s\n", base, screenSrcRaw)
	}
	fmt.Printf(`
Quick start:
  1. Open each Guest link on a device/tab, allow camera/mic, click "Enter the greenroom"
     (wait for "your camera is live in the greenroom"). Phones: scan the QR codes below.
  2. Open the Host sign-in link, then the Greenroom — every guest tile should render.
  3. Press Enter HERE to bind the cam slots, then add an OBS source URL as a Browser Source
     (1280x720) and bring it on-program to light that guest's on-air pill.
  4. Work the checklist in docs/SMOKE.md (multi-guest grid, on-air, degradation, and the
     RF-8 force checks: a force-mute/-no-cam must go silent/black on the OBS source AND on
     other participants' tiles, even for a guest who keeps sending).

`)
}

// printQRCodes renders a scannable QR per guest link via `qrencode` (best-effort; phones can then
// join without typing). No-op with a one-line hint when qrencode isn't installed.
func printQRCodes(base string, parts []participant) {
	if _, err := exec.LookPath("qrencode"); err != nil {
		fmt.Println("QR codes: install `qrencode` (brew install qrencode) to get scannable guest links here.")
		return
	}
	fmt.Println("Scan to join (guest links):")
	for _, p := range parts {
		url := fmt.Sprintf("%s/p/%s", base, p.passRaw)
		out, err := exec.Command("qrencode", "-t", "UTF8", "-o", "-", url).Output()
		if err != nil {
			continue
		}
		fmt.Printf("\n  %s — %s\n%s\n", p.name, url, out)
	}
}

// firstNonEmpty returns the first non-blank string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// freeCamIdx returns the lowest unused cam-slot index (1..8) for the host, so repeated runs
// against the same DB don't collide on the unique (host_id, idx) cam constraint.
func freeCamIdx(ctx context.Context, st *store.Store, hostID string) (int64, error) {
	slots, err := st.ListSlotsByHost(ctx, hostID)
	if err != nil {
		return 0, fmt.Errorf("listing slots: %w", err)
	}
	used := map[int64]bool{}
	for _, sl := range slots {
		if sl.Kind == store.SlotCam && sl.Idx != nil {
			used[*sl.Idx] = true
		}
	}
	for i := int64(1); i <= 8; i++ {
		if !used[i] {
			return i, nil
		}
	}
	return 0, errors.New("all 8 cam slots are in use for the dev host — reset with `rm $DB_PATH*` and re-run")
}

// sendRebind opens a short-lived host /ws connection and sends one {t:rebind} for the slot. The
// binding is held in room state by the still-connected guest + OBS source, so the host connection
// can close immediately afterward (a host leaving does not unbind a slot).
func sendRebind(ctx context.Context, wsURL, session, slot, passID string) error {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cookie := (&http.Cookie{Name: auth.SessionCookie, Value: session}).String()
	c, _, err := websocket.Dial(dctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": {cookie}},
	})
	if err != nil {
		return fmt.Errorf("dial host /ws: %w", err)
	}
	defer func() { _ = c.Close(websocket.StatusNormalClosure, "done") }()
	if err := wsjson.Write(dctx, c, signaling.Frame{T: "rebind", Slot: slot, OccupantPeerID: passID}); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond) // let the room apply the rebind before the socket closes
	return nil
}
