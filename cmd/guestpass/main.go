// Command guestpass is the GuestPass server — a single static Go binary
// (signaling relay + SQLite + embedded frontend & OBS source pages).
// See docs/ARCHITECTURE.md for the design.
//
//	guestpass          # print version + AGPL §13 source link
//	guestpass serve    # run the HTTP + signaling server
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/buildinfo"
	"github.com/rock3r/guest-pass/internal/config"
	"github.com/rock3r/guest-pass/internal/jobs"
	"github.com/rock3r/guest-pass/internal/livecheck"
	"github.com/rock3r/guest-pass/internal/mail"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
	"github.com/rock3r/guest-pass/internal/turn"
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

// serve loads configuration (fail-closed FIRST — EN-14/AD-8 — so the binary refuses to
// start on a bad secret/required-var before binding a socket; the bare `guestpass`
// version path deliberately skips this so the §13 link resolves without secrets), opens
// the store (running migrations), builds the HTTP handler, and serves it with a SIGTERM
// graceful drain (RF-21): stop accepting, finish in-flight HTTP, terminate WS peers with
// reason "reconnect", then flush DB writes.
func serve(addr string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	log.Printf("config loaded: signup_mode=%q mail_mode=%q turn_enabled=%t auth_mode=%q db=%q",
		cfg.SignupMode, cfg.MailMode, cfg.TURNEnabled(), cfg.AuthMode, cfg.DBPath)

	st, err := store.Open(context.Background(), cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}

	// The hub persists suppression locks through the store (AD-22), so a force-muted guest stays
	// muted across a restart; rooms default a nil logger to slog.Default().
	hub := signaling.NewHub(web.NewLockPersistence(st), nil)
	limiter := web.NewRateLimiter(5, 20)         // ~5 req/s/IP sustained, burst 20, on /auth routes
	wsLimiter := web.NewRateLimiter(10, 40)      // /ws reconnect throttle: lenient so OBS sources + tabs reattach
	inviteLimiter := web.NewRateLimiter(0.1, 30) // per-host email-send throttle (D-36): burst 30 (a full panel), ~6/min sustained
	var wsInflight sync.WaitGroup                // live /ws handlers, so the drain can wait for terminate flush
	handler, err := buildHandler(cfg, st, hub, limiter, wsLimiter, inviteLimiter, &wsInflight)
	if err != nil {
		_ = st.Close()
		return fmt.Errorf("building handler: %w", err)
	}

	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	stop := make(chan struct{})

	// Background jobs (DESIGN §9.7) run on their own goroutines under jobsCtx, cancelled at
	// drain so they stop before the store closes: the 24h guest-PII purge (D-37) and the
	// idle-session reaper (D-40).
	jobsCtx, jobsCancel := context.WithCancel(context.Background())
	jobsLog := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	var jobsWG sync.WaitGroup // background jobs, joined at drain before the store closes
	jobsWG.Add(1)
	go func() {
		defer jobsWG.Done()
		jobs.NewPurger(st, jobs.PurgeConfig{Interval: cfg.PurgeInterval, Retention: cfg.PurgeRetention, ReportRetention: cfg.ReportRetention}, jobsLog).Run(jobsCtx)
	}()
	// The reaper ends sessions whose room has had no connected participants for ReapIdleAfter
	// (D-40), freeing the one-live-session-per-host slot and making the guests' PII purge-eligible
	// (the purge keys off ended_at). ReapIfIdle is the atomic gate — it refuses to reap while a
	// participant is connected, and it runs the ended_at write WHILE the room is still registered +
	// terminating so a reconnect in the gap is refused, not spawned into a fresh room for the
	// ending session.
	reaper := jobs.NewReaper(jobs.ReaperDeps{
		ActiveHosts:  st.ActiveSessionHostIDs,
		Participants: hub.ParticipantCount,
		Reap: func(ctx context.Context, hostID string) (bool, error) {
			return hub.ReapIfIdle(hostID, func() error { return st.EndActiveSession(ctx, hostID) })
		},
	}, jobs.ReaperConfig{Interval: cfg.ReapInterval, IdleAfter: cfg.ReapIdleAfter}, jobsLog)
	jobsWG.Add(1)
	go func() {
		defer jobsWG.Done()
		reaper.Run(jobsCtx)
	}()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("draining: terminating WS peers and finishing in-flight writes…")
		jobsCancel() // stop background jobs before the store closes
		sctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		// Close the listener and terminate WS rooms CONCURRENTLY: srv.Shutdown closes the
		// listener at once (no new traffic) and drains in-flight HTTP handlers, while
		// Hub.Shutdown sends terminate to unblock the read-blocked WS handlers. Running
		// them together means a slow Room.Terminate can't keep the listener open, and
		// srv.Shutdown isn't left waiting on WS handlers that haven't been told to stop.
		var phase sync.WaitGroup
		phase.Add(2)
		go func() { defer phase.Done(); _ = srv.Shutdown(sctx) }()
		go func() { defer phase.Done(); hub.Shutdown("reconnect") }()
		phase.Wait()
		// The listener is now closed, so no new /ws handlers can start; only the ones that
		// began earlier remain, finishing as they flush terminate. Waiting on them here
		// can't race a fresh Add-from-zero.
		waitTimeout(&wsInflight, 10*time.Second)
		waitTimeout(&jobsWG, 5*time.Second) // let background jobs observe cancel before the store closes
		_ = st.Close()                      // flush in-flight DB writes (writer pool drains on Close)
		close(stop)
	}()

	// Periodically evict idle rate-limit buckets to bound memory (AA-1).
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				limiter.Cleanup(10 * time.Minute)
				wsLimiter.Cleanup(10 * time.Minute)
				inviteLimiter.Cleanup(30 * time.Minute)
			case <-stop:
				return
			}
		}
	}()

	log.Printf("guestpass serving on %s (source: %s)", addr, buildinfo.SourceURL())
	err = srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		<-stop // let the drain finish (hub terminated, store closed)
		return nil
	}
	jobsCancel()                        // serve failed before a signal-drain ran; stop jobs…
	waitTimeout(&jobsWG, 5*time.Second) // …and let them finish before releasing the store
	_ = st.Close()
	return err
}

// waitTimeout waits for wg or the timeout, whichever comes first, so a drain can't hang
// indefinitely on a stuck connection.
func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// buildHandler assembles the HTTP handler from config, the store, and the hub: the JWT
// key ring + live-DB authenticator (EN-6), the Google OAuth flow, the dev-login seam
// (nil unless this is a dev build with AUTH_MODE=dev), and the route table.
func buildHandler(cfg *config.Config, st *store.Store, hub *signaling.Hub, limiter, wsLimiter, inviteLimiter *web.RateLimiter, wsInflight *sync.WaitGroup) (http.Handler, error) {
	ring, err := auth.NewKeyRing(cfg.JWTSecret, cfg.JWTSecretPrevious)
	if err != nil {
		return nil, fmt.Errorf("building key ring: %w", err)
	}
	secure := cfg.Secure() // normalized https check; the single source of truth (config.Secure)
	authn := auth.NewAuthenticator(ring, st, secure)
	oauth := auth.NewGoogleOAuth(auth.GoogleConfig{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		BaseURL:      cfg.BaseURL,
		Policy:       auth.LoginPolicy{SignupMode: cfg.SignupMode, AdminEmail: cfg.AdminEmail, AllowedHosts: cfg.AllowedHosts},
		Secure:       secure,
		SuccessURL:   "/app", // land on the host-app dashboard after Google sign-in (M4)
	}, authn, st)

	hasher, err := token.NewHasher(cfg.TokenSecret)
	if err != nil {
		return nil, fmt.Errorf("building token hasher: %w", err)
	}
	var mailer mail.Mailer
	if cfg.MailMode == config.MailModeLog {
		mailer = mail.NewLogMailer(os.Stdout)
	} else {
		mailer = mail.NewResendMailer(cfg.ResendAPIKey, cfg.MailFrom)
	}

	return web.NewRouter(web.RouterConfig{
		SourceURL:     buildinfo.SourceURL(),
		Hub:           hub,
		OAuth:         oauth,
		Auth:          authn,
		DevLogin:      devLoginHandler(cfg, authn, st),
		TURNHost:      web.CSPTURNHost(cfg.TURNURL), // empty in the STUN-only default (D-38)
		Secure:        secure,
		StaticDir:     "web/dist",
		RateLimiter:   limiter,
		WSRateLimiter: wsLimiter,
		InviteLimiter: inviteLimiter,
		WSInflight:    wsInflight,
		// STUN always (D-38); a TURN entry with a fresh ephemeral HMAC cred (EN-4) is added
		// per peer when TURN_URL/TURN_SECRET are set.
		ICE:       turn.NewProvider(cfg.STUNURL, cfg.TURNURL, cfg.TURNSecret),
		Store:     st,
		Hasher:    hasher,
		Mailer:    mailer,
		BaseURL:   cfg.BaseURL,
		LiveCheck: livecheck.NewChecker(), // D-29 SSRF-closed live-verify (watch link + verified status)
		// D-36 progressive-trust per-host invite/stream quotas (tightest for new accounts).
		Trust: auth.TrustPolicy{
			TrustAfter:     cfg.RateTrustAfter,
			NewInvites:     cfg.RateNewInvites,
			TrustedInvites: cfg.RateTrustedInvites,
			NewStreams:     cfg.RateNewStreams,
			TrustedStreams: cfg.RateTrustedStreams,
		},
		Logger: slog.New(slog.NewJSONHandler(os.Stdout, nil)),
		// Global request-body cap (D-M5.5-4 / AC-8): reject an oversized body instance-wide with 413.
		MaxRequestBodyBytes: cfg.MaxRequestBodyBytes,
	})
}
