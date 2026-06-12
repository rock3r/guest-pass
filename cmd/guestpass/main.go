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
func serve(addr string) error {
	hub := signaling.NewHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", web.ServeWS(hub))
	mux.Handle("/", http.FileServer(http.Dir("web/dist")))
	log.Printf("guestpass serving on %s (source: %s)", addr, buildinfo.SourceURL())
	return http.ListenAndServe(addr, mux)
}
