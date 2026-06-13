// Command guestpass is the GuestPass server — a single static Go binary
// (signaling relay + SQLite + embedded frontend & OBS source pages).
// See docs/ARCHITECTURE.md for the design.
//
//	guestpass          # print version + AGPL §13 source link
//	guestpass serve    # run the HTTP + signaling server (SPIKE-2 scaffold)
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/rock3r/guest-pass/internal/buildinfo"
	"github.com/rock3r/guest-pass/internal/config"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		if err := serve(":8137"); err != nil {
			log.Fatal(err)
		}
		return
	}
	fmt.Printf("guestpass %s (%s)\n", buildinfo.Version(), buildinfo.Commit())
	fmt.Printf("source: %s\n", buildinfo.SourceURL())
}

// serve runs the SPIKE-2 server: the signaling WebSocket endpoint plus static assets
// from web/dist. The full route table, TLS, and embedded assets land in M1/M2.
//
// Configuration is loaded and validated FIRST, so the binary fails closed (EN-14 /
// AD-8) on an empty/placeholder JWT_SECRET, a TURN secret missing while TURN is
// enabled, or AUTH_MODE=dev in a release build — before any socket is bound. (The
// bare `guestpass` version/source path deliberately does not load config: the AGPL
// §13 source link must resolve without any secrets configured.)
func serve(addr string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	log.Printf("config loaded: signup_mode=%q mail_mode=%q turn_enabled=%t auth_mode=%q",
		cfg.SignupMode, cfg.MailMode, cfg.TURNEnabled(), cfg.AuthMode)
	// Config fields are wired into server behavior (routes, OAuth, mail, ICE) in the
	// later M1 steps; here the load is the fail-closed gate.

	hub := signaling.NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", web.ServeWS(hub))
	mux.Handle("/", http.FileServer(http.Dir("web/dist")))
	log.Printf("guestpass serving on %s (source: %s)", addr, buildinfo.SourceURL())
	return http.ListenAndServe(addr, mux)
}
