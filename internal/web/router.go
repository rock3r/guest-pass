package web

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/mail"
	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
	"github.com/rock3r/guest-pass/internal/token"
)

// RouterConfig wires the HTTP surface. Auth-dependent routes are registered only when
// their dependency is present, so a minimal config (e.g. in tests) still serves the
// landing, sign-in, health, and static routes. /ws needs the authenticator, store, and
// token hasher because it authenticates by credential.
type RouterConfig struct {
	SourceURL     string                // AGPL §13 link to the running build's source (EN-17)
	Hub           *signaling.Hub        // signaling hub for /ws
	OAuth         *auth.GoogleOAuth     // Google sign-in; nil disables /auth/google*
	Auth          *auth.Authenticator   // session lifecycle (logout); nil disables /auth/logout
	DevLogin      http.HandlerFunc      // dev sign-in handler; nil (release) disables /auth/dev
	TURNHost      string                // CSP connect-src TURN host; empty = STUN-only
	Secure        bool                  // HTTPS origin; false (HTTP dev) also allows ws: in connect-src
	StaticDir     string                // built frontend assets (web/dist), served at /static
	RateLimiter   *RateLimiter          // per-IP limiter applied to /auth routes; nil disables
	WSRateLimiter *RateLimiter          // per-IP limiter applied to /ws (reconnect throttle); nil disables
	WSInflight    *sync.WaitGroup       // tracks live /ws handlers so a drain can wait for terminate flush
	ICEServers    []signaling.ICEServer // ICE config sent in the {t:"ice"} join-ack (AD-14); empty = no STUN
	Logger        *slog.Logger          // structured logger for the WS path; nil uses slog.Default()

	// Host API + guest magic-link page. All four must be set together to enable the
	// /api/streams* and /p/{token} routes; if any is nil they are not registered.
	Store   *store.Store  // persistence for streams/passes
	Hasher  *token.Hasher // magic-link token hashing (EN-5)
	Mailer  mail.Mailer   // invite delivery (LogMailer in MAIL_MODE=log)
	BaseURL string        // absolute origin used to build magic links
}

// NewRouter builds the GuestPass HTTP handler: strict security headers globally, the
// server-rendered landing/sign-in pages, /healthz, the signaling WebSocket, static
// assets, and the (rate-limited) auth routes.
func NewRouter(cfg RouterConfig) (http.Handler, error) {
	manifest := loadManifest(cfg.StaticDir)
	rd, err := newRenderer(cfg.SourceURL, manifest["app.css"], cfg.DevLogin != nil)
	if err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	r.Use(SecurityHeaders(SecurityOptions{TURNHost: cfg.TURNHost, Secure: cfg.Secure}))

	r.Get("/", rd.landing)
	r.Get("/signin", rd.signin)
	r.Get("/healthz", healthz)

	// The signaling WebSocket authenticates by credential against the live DB, so it
	// needs the authenticator, store, and token hasher. Without them (a minimal
	// landing-only config) /ws is not registered.
	if cfg.Hub != nil && cfg.Auth != nil && cfg.Store != nil && cfg.Hasher != nil {
		resolver := &wsResolver{auth: cfg.Auth, hasher: cfg.Hasher, store: cfg.Store}
		wsh := newWSHandler(cfg.Hub, resolver, cfg.WSInflight, cfg.ICEServers, cfg.Logger)
		r.Group(func(wr chi.Router) {
			if cfg.WSRateLimiter != nil { // per-IP reconnect throttle (D-36)
				wr.Use(cfg.WSRateLimiter.Middleware(ClientIP))
			}
			wr.Get("/ws", wsh.serve)
		})
	}
	if cfg.StaticDir != "" {
		r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir(cfg.StaticDir))))
	}

	// Auth routes, rate-limited per IP to blunt credential/token scanning (§5).
	r.Group(func(ar chi.Router) {
		if cfg.RateLimiter != nil {
			ar.Use(cfg.RateLimiter.Middleware(ClientIP))
		}
		if cfg.OAuth != nil {
			ar.Get("/auth/google", cfg.OAuth.StartLogin)
			ar.Get("/auth/google/callback", cfg.OAuth.Callback)
		}
		if cfg.Auth != nil {
			ar.Post("/auth/logout", func(w http.ResponseWriter, r *http.Request) {
				cfg.Auth.ClearSession(w)
				http.Redirect(w, r, "/", http.StatusFound)
			})
		}
		if cfg.DevLogin != nil {
			ar.Get("/auth/dev", cfg.DevLogin)
		}
	})

	// Host JSON API + guest magic-link page. Registered only when persistence and the
	// authenticator are wired; this keeps the minimal test/landing config intact.
	if cfg.Store != nil && cfg.Hasher != nil && cfg.Mailer != nil && cfg.Auth != nil {
		api := &apiServer{store: cfg.Store, hasher: cfg.Hasher, mailer: cfg.Mailer, baseURL: cfg.BaseURL, rd: rd}

		r.Group(func(hr chi.Router) {
			hr.Use(cfg.Auth.RequireHost)
			hr.Get("/api/streams", api.listStreams)
			hr.Post("/api/streams", api.createStream)
			hr.Get("/api/streams/{id}", api.getStream)
			hr.Delete("/api/streams/{id}", api.deleteStream)
			hr.Get("/api/streams/{id}/passes", api.listPasses)
			hr.Post("/api/streams/{id}/passes", api.createPass)
		})

		// Public guest landing. Rate-limited (when configured) to blunt token scanning;
		// the handler is side-effect-free (EN-10).
		r.Group(func(pr chi.Router) {
			if cfg.RateLimiter != nil {
				pr.Use(cfg.RateLimiter.Middleware(ClientIP))
			}
			pr.Get("/p/{token}", api.passLanding)
		})
	}

	return r, nil
}
