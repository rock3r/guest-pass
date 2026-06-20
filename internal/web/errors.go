package web

import (
	"net/http"
	"strings"

	"github.com/rock3r/guest-pass/internal/auth"
	"github.com/rock3r/guest-pass/internal/store"
)

// Error/denial screens (M5.5 "full error screens"). The authz DECISION stays in internal/auth
// (RequireHost/RequireAdmin, read live, EN-6); this file is purely its PRESENTATION. The web layer
// installs authDenied as the Authenticator's DeniedHandler (see NewRouter), so a denied navigation
// gets a rendered, explanatory page instead of a bare "forbidden"/"unauthorized" body — while the
// status (401 re-auth vs 403 suspended/forbidden) is unchanged.

// errorAction is the optional call-to-action link on an error screen.
type errorAction struct {
	Href, Label string
}

// errorPage is the page-specific payload for error.html.
type errorPage struct {
	Heading string
	Body    []string // one <p> per entry
	Action  *errorAction
}

// authDenied renders a denied request. It is installed as the auth.DeniedHandler so the auth
// middleware delegates presentation here. It content-negotiates: a top-level browser navigation
// (Accept: text/html) gets the rendered screen; a fetch()/XHR (the greenroom island → /api/*, which
// sends Accept: */*) keeps the terse plain-text body a JSON client expects. The status is fixed by
// the reason either way (reason.Status()).
func (rd *renderer) authDenied(w http.ResponseWriter, r *http.Request, reason auth.DenialReason, host *store.Host) {
	status := reason.Status()
	if !wantsHTML(r) {
		http.Error(w, denialPlainText(reason), status)
		return
	}
	title, page := errorContent(reason, host)
	rd.renderStatus(w, r, "error.html", status, pageData{Title: title, Data: page})
}

// wantsHTML reports whether the client is doing a document navigation that should get a rendered
// HTML screen, rather than a fetch/XHR that wants a terse body. Browsers send "text/html" in Accept
// on a top-level navigation; fetch() defaults to "*/*". This is the seam that lets one shared
// middleware serve HTML to /app, /greenroom, and /admin navigations while keeping /api/* plain.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// denialPlainText is the terse body for non-navigation (fetch/XHR) clients. It reproduces the exact
// strings the middleware used before this change, so API callers see no behavior change.
func denialPlainText(reason auth.DenialReason) string {
	switch reason {
	case auth.DenyUnauthorized:
		return "unauthorized"
	case auth.DenyError:
		return "internal error"
	default: // DenyInactive, DenyNotAdmin
		return "forbidden"
	}
}

// errorContent maps a denial reason (and the live host, when known) to the screen title + body.
// DenyInactive splits on the live status so a suspended host and a pending host get accurate copy.
// Copy is intentionally instance-neutral — this is self-hostable OSS, so there is no central
// support address to hard-code; the appeal path is "the administrator of this instance" (M6 owns
// final wording).
func errorContent(reason auth.DenialReason, host *store.Host) (title string, page errorPage) {
	switch reason {
	case auth.DenyUnauthorized:
		return "Sign in required", errorPage{
			Heading: "Please sign in",
			Body: []string{
				"You need to be signed in to view this page.",
				"Your session may have expired — sign in again to pick up where you left off.",
			},
			Action: &errorAction{Href: "/signin", Label: "Go to sign-in"},
		}
	case auth.DenyInactive:
		if host != nil && host.Status == store.HostPending {
			return "Account pending approval", errorPage{
				Heading: "Your host account is awaiting approval",
				Body: []string{
					"An administrator needs to approve your account before you can create or run streams.",
					"Check back later, or reach out to the administrator of this GuestPass instance.",
				},
				Action: &errorAction{Href: "/", Label: "Back to home"},
			}
		}
		// suspended (and any other non-active status) → the suspension screen.
		return "Account suspended", errorPage{
			Heading: "Your host account has been suspended",
			Body: []string{
				"While your account is suspended you can't start or run streams, and any live session you were hosting has been ended.",
				"If you think this is a mistake, contact the administrator of this GuestPass instance to appeal.",
			},
			Action: &errorAction{Href: "/", Label: "Back to home"},
		}
	case auth.DenyNotAdmin:
		return "No access", errorPage{
			Heading: "You don't have access to this page",
			Body: []string{
				"The admin console is limited to administrators of this GuestPass instance.",
				"If you reached this by mistake, head back to your dashboard.",
			},
			Action: &errorAction{Href: "/app", Label: "Back to your dashboard"},
		}
	default: // DenyError
		return "Something went wrong", errorPage{
			Heading: "Something went wrong",
			Body:    []string{"We couldn't complete that request. Please try again in a moment."},
			Action:  &errorAction{Href: "/", Label: "Back to home"},
		}
	}
}
