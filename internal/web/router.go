package web

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/signaling"
)

// RouterConfig wires the HTTP surface. Auth-dependent routes are registered only when
// their dependency is present, so a minimal config (e.g. in tests) still serves the
// landing, sign-in, health, static, and WS routes.
type RouterConfig struct {
	SourceURL   string              // AGPL §13 link to the running build's source (EN-17)
	Hub         *signaling.Hub      // signaling hub for /ws
	OAuth       *auth.GoogleOAuth   // Google sign-in; nil disables /auth/google*
	Auth        *auth.Authenticator // session lifecycle (logout); nil disables /auth/logout
	DevLogin    http.HandlerFunc    // dev sign-in handler; nil (release) disables /auth/dev
	TURNHost    string              // CSP connect-src TURN host; empty = STUN-only
	StaticDir   string              // built frontend assets (web/dist), served at /static
	RateLimiter *RateLimiter        // per-IP limiter applied to /auth routes; nil disables
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
	r.Use(SecurityHeaders(SecurityOptions{TURNHost: cfg.TURNHost}))

	r.Get("/", rd.landing)
	r.Get("/signin", rd.signin)
	r.Get("/healthz", healthz)

	if cfg.Hub != nil {
		r.Get("/ws", ServeWS(cfg.Hub))
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

	return r, nil
}
