package auth

import (
	"net/http"
	"time"
)

// SessionTTL is how long a host session cookie stays valid before re-auth is required.
const SessionTTL = 7 * 24 * time.Hour

// SetSession issues a session JWT for hostID and writes it as the session cookie:
// httpOnly + SameSite=Lax + Secure (in production). SameSite=Lax (not Strict) so the
// cookie survives the top-level redirect back from Google's consent screen.
func (a *Authenticator) SetSession(w http.ResponseWriter, hostID string) error {
	tok, err := a.ring.Issue(hostID, SessionTTL)
	if err != nil {
		return err
	}
	http.SetCookie(w, a.sessionCookie(tok, int(SessionTTL/time.Second)))
	return nil
}

// ClearSession expires the session cookie (logout).
func (a *Authenticator) ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, a.sessionCookie("", -1))
}

func (a *Authenticator) sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
}
