//go:build dev

// devsmoke seeds the fixtures the M2 manual smoke needs but that have no host UI yet — a
// stream, a guest pass, and a cam-1 slot for the LOCAL DEV HOST (the same identity
// /auth/dev signs you in as, so the greenroom and the OBS source resolve to one room) —
// prints the guest + OBS-source URLs, then binds cam-1 to the guest on demand by sending a
// {t:rebind} over a host /ws connection.
//
// It is a DEV-ONLY tool: it mints a host session straight from JWT_SECRET, so it is compiled
// only under `-tags dev` and refuses to run unless AUTH_MODE=dev. Run it with the SAME
// environment as the server:
//
//	AUTH_MODE=dev MAIL_MODE=log BASE_URL=http://localhost:8137 \
//	JWT_SECRET=… TOKEN_SECRET=… DB_PATH=guestpass.db \
//	go run -tags dev ./cmd/devsmoke
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
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

func run() error {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config (use the same env as the server): %w", err)
	}
	if cfg.AuthMode != "dev" {
		return errors.New("devsmoke is local-dev only — set AUTH_MODE=dev (run the server the same way)")
	}

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening store %q: %w", cfg.DBPath, err)
	}
	defer func() { _ = st.Close() }()

	hasher, err := token.NewHasher(cfg.TokenSecret)
	if err != nil {
		return fmt.Errorf("token hasher: %w", err)
	}

	// The same host /auth/dev signs you in as, so the greenroom + OBS source share one room.
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
		HostID: host.ID, Title: "M2 smoke", Status: store.StreamLive,
	})
	if err != nil {
		return fmt.Errorf("create stream: %w", err)
	}

	passRaw, err := token.Mint()
	if err != nil {
		return err
	}
	pass, err := st.CreatePass(ctx, store.CreatePassParams{
		StreamID: stream.ID, Name: ptr("Smoke Guest"), Role: store.RoleGuest,
		TokenHash: hasher.Hash(passRaw), Status: store.PassSent,
	})
	if err != nil {
		return fmt.Errorf("create pass: %w", err)
	}

	srcRaw, err := token.Mint()
	if err != nil {
		return err
	}
	if _, err := st.CreateSlot(ctx, store.CreateSlotParams{
		HostID: host.ID, Kind: store.SlotCam, Idx: ptr(int64(1)), SourceTokenHash: hasher.Hash(srcRaw),
	}); err != nil {
		return fmt.Errorf("create slot: %w", err)
	}

	base := strings.TrimRight(cfg.BaseURL, "/")
	fmt.Printf(`
Smoke fixtures ready (host=dev-local-host, stream=%s).

  Guest link:   %s/p/%s
  OBS source:   %s/s/cam-1?token=%s
  Greenroom:    %s/greenroom   (sign in at %s/auth/dev first; not needed for the OBS test)
  pass id:      %s

Steps:
  1. Open the Guest link, allow camera/mic, click "Enter the greenroom" — wait until it
     says "your camera is live in the greenroom".
  2. Press Enter here to bind cam-1 -> the guest.
  3. Add the OBS source URL as a Browser Source in OBS (width 1280, height 720); it should
     render the guest. Bring that source on-program to light the guest's on-air pill.

`, stream.ID, base, passRaw, base, srcRaw, base, base, pass.ID)

	// Mint a host session JWT with the same ring the server uses, for the rebind connection.
	ring, err := auth.NewKeyRing(cfg.JWTSecret)
	if err != nil {
		return fmt.Errorf("key ring: %w", err)
	}
	session, err := ring.Issue(host.ID, time.Hour)
	if err != nil {
		return fmt.Errorf("issue host session: %w", err)
	}
	wsURL := strings.Replace(base, "http", "ws", 1) + "/ws"

	in := bufio.NewScanner(os.Stdin)
	fmt.Print("Press Enter to bind cam-1 -> the guest (Ctrl-C to quit): ")
	for in.Scan() {
		if err := sendRebind(ctx, wsURL, session, pass.ID); err != nil {
			fmt.Printf("  rebind failed: %v\n", err)
		} else {
			fmt.Println("  rebind sent. OBS should render the guest; bring the source on-program to light the on-air pill.")
		}
		fmt.Print("Press Enter to re-send the rebind (e.g. if the guest wasn't connected yet), Ctrl-C to quit: ")
	}
	return nil
}

// sendRebind opens a short-lived host /ws connection and sends one {t:rebind} for cam-1. The
// binding is held in room state by the still-connected guest + OBS source, so the host
// connection can close immediately afterward (a host leaving does not unbind a slot).
func sendRebind(ctx context.Context, wsURL, session, passID string) error {
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
	if err := wsjson.Write(dctx, c, signaling.Frame{T: "rebind", Slot: "cam-1", OccupantPeerID: passID}); err != nil {
		return err
	}
	time.Sleep(300 * time.Millisecond) // let the room apply the rebind before the socket closes
	return nil
}
