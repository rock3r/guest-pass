package web

import (
	"net/http"
	"strings"
)

// themeCookieMaxAge keeps an explicit dark-mode choice for a year — long enough to feel persistent,
// not permanent.
const themeCookieMaxAge = 365 * 24 * 60 * 60

// themeHandler handles POST /theme: it records the user's explicit dark-mode choice (D-9) in the
// gp_theme cookie and PRG-redirects back to the page they were on. "light"/"dark" set the cookie;
// anything else ("system") clears it, so the page follows the OS preference. The cookie is purely
// cosmetic — no PII, no token — so it is intentionally JS-readable (HttpOnly omitted) to allow a
// future live toggle; SameSite=Lax keeps it same-site, and Secure follows the deployment.
func themeHandler(secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch theme := r.FormValue("theme"); theme {
		case "light", "dark":
			http.SetCookie(w, &http.Cookie{
				Name: themeCookie, Value: theme, Path: "/",
				MaxAge: themeCookieMaxAge, SameSite: http.SameSiteLaxMode, Secure: secure,
			})
		default: // "system" / unknown → clear the choice, follow the OS preference
			http.SetCookie(w, &http.Cookie{
				Name: themeCookie, Value: "", Path: "/",
				MaxAge: -1, SameSite: http.SameSiteLaxMode, Secure: secure,
			})
		}
		http.Redirect(w, r, safeLocalNext(r.FormValue("next")), http.StatusSeeOther)
	}
}

// safeLocalNext clamps the toggle's return path to a same-origin absolute path, so a tampered "next"
// can never turn the toggle into an open redirect or a header-injection: it must start with "/" but
// not "//" or "/\" (protocol-relative authority), and carry no CR/LF. Anything else falls back to "/".
func safeLocalNext(next string) string {
	if next == "" || next[0] != '/' {
		return "/"
	}
	if len(next) >= 2 && (next[1] == '/' || next[1] == '\\') {
		return "/"
	}
	if strings.ContainsAny(next, "\r\n") {
		return "/"
	}
	return next
}
