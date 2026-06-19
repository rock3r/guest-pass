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
	Store     *store.Store  // persistence for streams/passes
	Hasher    *token.Hasher // magic-link token hashing (EN-5)
	Mailer    mail.Mailer   // invite delivery (LogMailer in MAIL_MODE=log)
	BaseURL   string        // absolute origin used to build magic links
	LiveCheck LiveChecker   // D-29 live-verify (watch link + verified status); nil disables the channel routes
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

	// One per-host slot-binding lock shared by the /ws join-replay and the binding API, so a
	// host's binding ops are serialized across both surfaces (D-20).
	binds := newBindingLocks()

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
		wsh := newWSHandler(cfg.Hub, resolver, cfg.WSInflight, cfg.ICE, binds, cfg.Logger)
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
		api := &apiServer{store: cfg.Store, hasher: cfg.Hasher, mailer: cfg.Mailer, baseURL: cfg.BaseURL, rd: rd, hub: cfg.Hub, binds: binds, auth: cfg.Auth, liveCheck: cfg.LiveCheck}
		app := &appServer{store: cfg.Store, rd: rd, hasher: cfg.Hasher, mailer: cfg.Mailer, baseURL: cfg.BaseURL, reveals: newRevealStore(), hub: cfg.Hub, binds: binds, auth: cfg.Auth, liveCheck: cfg.LiveCheck}

		r.Group(func(hr chi.Router) {
			hr.Use(cfg.Auth.RequireHost)
			hr.Get("/api/streams", api.listStreams)
			hr.Post("/api/streams", api.createStream)
			hr.Get("/api/streams/{id}", api.getStream)
			hr.Delete("/api/streams/{id}", api.deleteStream)
			hr.Get("/api/streams/{id}/passes", api.listPasses)
			hr.Post("/api/streams/{id}/passes", api.createPass)
			// Live slot↔guest (re)bind from the greenroom People controls (D-20): persist +
			// re-route /s/{slot} with no OBS edit (EN-3). Host-only (RequireHost), RF-2.
			hr.Put("/api/passes/{id}/slot", api.putPassSlot)
			// Nameplate override (D-16/AC-7): set a guest's sticky display name (capped EN-15) →
			// persist to passes.name + refresh the live OBS nameplate. Host-only (RequireHost), RF-2.
			hr.Put("/api/passes/{id}/name", api.putPassName)
			// Screenshare eligibility (EN-23/AC-9): grant/revoke can_screen live → persist + re-project
			// the room (a revoke runs force-no-share). Host-only (RequireHost), RF-2.
			hr.Patch("/api/passes/{id}", api.patchPass)
			// Host's persisted pass→slot bindings, so the greenroom can seed its picker on load and a
			// pre-live (DB-only) selection survives a refresh / new tab (codex).
			hr.Get("/api/passes/slot-bindings", api.listSlotBindings)
			// Quality ceiling (D-19/AC-8): adjust a stream's program-encoder cap → persist streams.max_*
			// + re-cap the live room's publishers. Host-only (RequireHost), RF-2.
			hr.Put("/api/streams/{id}/ceiling", api.putStreamCeiling)
			// Active session's stream id + current ceiling, so the greenroom populates + targets its
			// ceiling control (404 until Go live). Host-only.
			hr.Get("/api/session/ceiling", api.getSessionCeiling)

			// Live-verify (D-29/AC-8): the host links a Twitch channel to a stream (stream-detail
			// form), and the greenroom polls the verified-live status (the D-24 broadcast-layer fold).
			// Registered only when a checker is wired.
			if cfg.LiveCheck != nil {
				hr.Post("/app/streams/{id}/channel", app.setStreamChannel)
				hr.Get("/api/streams/{id}/livecheck", api.livecheckStatus)
			}

			// GDPR host self-service (D-37 / §8 / AC-3..5), host-only. Export is a single JSON
			// download; amend rectifies the display name; delete erases the account + all the host's
			// data and is refused while a live session exists (D-M5-3). The no-JS settings page drives
			// the same ops via the POST forms below (HTML forms can't PATCH/DELETE).
			hr.Get("/api/me/export", api.exportMe)
			hr.Patch("/api/me", api.amendMe)
			hr.Delete("/api/me", api.deleteMe)

			// Host-app shell (D-32): server-rendered dashboard + stream CRUD via POST-redirect-GET.
			// Same RequireHost gate as the JSON API (EN-6); no JS (CONVENTIONS §3.1).
			hr.Get("/app", app.dashboard)
			hr.Get("/app/calendar", app.calendar)
			// Account settings + GDPR self-service (D-37 / AC-3..5): account card, an export
			// download link (→ GET /api/me/export), and amend/delete POST-redirect-GET forms.
			// Host-only; CSRF-safe via the SameSite=Lax session cookie.
			hr.Get("/app/settings", app.settings)
			hr.Post("/app/settings/amend", app.amendSettings)
			hr.Post("/app/settings/delete", app.deleteSettings)
			hr.Post("/app/streams", app.createStream)
			hr.Get("/app/streams/{id}", app.streamDetail)
			hr.Get("/app/streams/{id}/sources", app.sourcesTab) // read-only Sources tab (EN-26)
			// D-22 slot-token rotation ("my URLs leaked"): regenerate one slot or rotate all,
			// invalidating the old token(s) + tearing down any live /s/{slot} subscription.
			hr.Post("/app/streams/{id}/sources/slots/{slotId}/regenerate", app.regenerateSlot)
			hr.Post("/app/streams/{id}/sources/regenerate-all", app.regenerateAllSlots)
			hr.Get("/app/streams/{id}/edit", app.editStreamForm)
			hr.Post("/app/streams/{id}", app.updateStream)
			hr.Post("/app/streams/{id}/delete", app.deleteStream)
			// Go live / end session (EN-2/D-20): the host declares which stream is live, gating the
			// /ws join-replay to that stream's guests. One live session per host.
			hr.Post("/app/streams/{id}/session/start", app.goLive)
			hr.Post("/app/streams/{id}/session/end", app.endSession)
			// Invites tab (EN-23): guest list + invite form (name/email/role only) +
			// inline role edit + re-issue + revoke. No live production controls here.
			hr.Post("/app/streams/{id}/passes", app.createInvite)
			hr.Post("/app/streams/{id}/passes/{pid}/role", app.setInviteRole)
			hr.Post("/app/streams/{id}/passes/{pid}/reissue", app.reissueInvite)
			hr.Post("/app/streams/{id}/passes/{pid}/revoke", app.revokeInvite)
		})

		// Admin console (AC-9 / D-14): cross-host visibility + the AC-10 host-management actions, all
		// behind RequireAdmin (RequireHost + live is_admin, EN-6). The §7.7 privacy boundary is
		// structural — the read handlers see only host/session/stream metadata + in-memory participant
		// counts, never passes (guest PII) and never a foreign room's media/chat.
		admin := &adminServer{store: cfg.Store, hub: cfg.Hub, binds: binds, rd: rd}
		r.Group(func(adr chi.Router) {
			adr.Use(cfg.Auth.RequireAdmin)
			adr.Get("/admin", admin.adminConsole)
			adr.Get("/api/admin/stats", admin.statsJSON)
			adr.Get("/api/admin/sessions", admin.sessionsJSON)
			adr.Get("/api/admin/hosts", admin.hostsJSON)
			// Mutating actions (AC-10 / D-27 approve+suspend-cascade / D-28 / promote-demote):
			// server-rendered form POSTs that PRG back to /admin (CSRF-safe via the SameSite cookie).
			adr.Post("/api/admin/hosts/{id}/approve", admin.approveHost)
			adr.Post("/api/admin/hosts/{id}/suspend", admin.suspendHost)
			adr.Post("/api/admin/hosts/{id}/promote", admin.promoteHost)
			adr.Post("/api/admin/hosts/{id}/demote", admin.demoteHost)
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
