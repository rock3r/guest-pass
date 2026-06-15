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
	SourceURL     string              // AGPL §13 link to the running build's source (EN-17)
	Hub           *signaling.Hub      // signaling hub for /ws
	OAuth         *auth.GoogleOAuth   // Google sign-in; nil disables /auth/google*
	Auth          *auth.Authenticator // session lifecycle (logout); nil disables /auth/logout
	DevLogin      http.HandlerFunc    // dev sign-in handler; nil (release) disables /auth/dev
	TURNHost      string              // CSP connect-src TURN host; empty = STUN-only
	Secure        bool                // HTTPS origin; false (HTTP dev) also allows ws: in connect-src
	StaticDir     string              // built frontend assets (web/dist), served at /static
	RateLimiter   *RateLimiter        // per-IP limiter applied to /auth routes; nil disables
	WSRateLimiter *RateLimiter        // per-IP limiter applied to /ws (reconnect throttle); nil disables
	WSInflight    *sync.WaitGroup     // tracks live /ws handlers so a drain can wait for terminate flush
	ICE           ICEConfigurer       // per-peer ICE join-ack provider (AD-14); nil = no ICE servers offered
	Logger        *slog.Logger        // structured logger for the WS path; nil uses slog.Default()

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
	rd, err := newRenderer(cfg.SourceURL, manifest, cfg.DevLogin != nil)
	if err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	r.Use(SecurityHeaders(SecurityOptions{TURNHost: cfg.TURNHost, Secure: cfg.Secure}))

	r.Get("/", rd.landing)
	r.Get("/signin", rd.signin)
	r.Get("/healthz", healthz)

	// The signaling-backed pages (greenroom, OBS source) and /ws all need the authenticator,
	// store, token hasher, and hub. Without them (a minimal landing-only config) none of
	// these are registered — there's no point serving a page where signaling can't run.
	wsReady := cfg.Hub != nil && cfg.Auth != nil && cfg.Store != nil && cfg.Hasher != nil

	if wsReady {
		// Host-only greenroom monitor page (host-authenticated, EN-6).
		r.Group(func(gr chi.Router) {
			gr.Use(cfg.Auth.RequireHost)
			gr.Get("/greenroom", rd.greenroom)
		})
		// Chromeless OBS cam source page. PUBLIC: the slot source token in the URL
		// authenticates the /ws?src= the page opens, not the page itself (EN-15). The {slot}
		// segment is an opaque label; the token resolves the slot server-side.
		r.Get("/s/{slot}", func(w http.ResponseWriter, r *http.Request) {
			rd.sourcePage(w, r, chi.URLParam(r, "slot"))
		})
	}

	if wsReady {
		resolver := &wsResolver{auth: cfg.Auth, hasher: cfg.Hasher, store: cfg.Store}
		wsh := newWSHandler(cfg.Hub, resolver, cfg.WSInflight, cfg.ICE, cfg.Logger)
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
		app := &appServer{store: cfg.Store, rd: rd, hasher: cfg.Hasher, mailer: cfg.Mailer, baseURL: cfg.BaseURL, reveals: newRevealStore()}

		r.Group(func(hr chi.Router) {
			hr.Use(cfg.Auth.RequireHost)
			hr.Get("/api/streams", api.listStreams)
			hr.Post("/api/streams", api.createStream)
			hr.Get("/api/streams/{id}", api.getStream)
			hr.Delete("/api/streams/{id}", api.deleteStream)
			hr.Get("/api/streams/{id}/passes", api.listPasses)
			hr.Post("/api/streams/{id}/passes", api.createPass)

			// Host-app shell (D-32): server-rendered dashboard + stream CRUD via POST-redirect-GET.
			// Same RequireHost gate as the JSON API (EN-6); no JS (CONVENTIONS §3.1).
			hr.Get("/app", app.dashboard)
			hr.Get("/app/calendar", app.calendar)
			hr.Post("/app/streams", app.createStream)
			hr.Get("/app/streams/{id}", app.streamDetail)
			hr.Get("/app/streams/{id}/sources", app.sourcesTab) // read-only Sources tab (EN-26)
			hr.Get("/app/streams/{id}/edit", app.editStreamForm)
			hr.Post("/app/streams/{id}", app.updateStream)
			hr.Post("/app/streams/{id}/delete", app.deleteStream)
			// Invites tab (EN-23): guest list + invite form (name/email/role only) +
			// inline role edit + re-issue + revoke. No live production controls here.
			hr.Post("/app/streams/{id}/passes", app.createInvite)
			hr.Post("/app/streams/{id}/passes/{pid}/role", app.setInviteRole)
			hr.Post("/app/streams/{id}/passes/{pid}/reissue", app.reissueInvite)
			hr.Post("/app/streams/{id}/passes/{pid}/revoke", app.revokeInvite)
		})

		// Public guest landing + device-check entry. Rate-limited (when configured) to blunt
		// token scanning; GET is side-effect-free, the explicit POST /enter marks opened (EN-10).
		r.Group(func(pr chi.Router) {
			if cfg.RateLimiter != nil {
				pr.Use(cfg.RateLimiter.Middleware(ClientIP))
			}
			pr.Get("/p/{token}", api.passLanding)
			pr.Post("/p/{token}/enter", api.passEnter)
		})
	}

	return r, nil
}
