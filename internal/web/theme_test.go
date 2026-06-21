package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// M5.5/D-9: the server stamps <html data-theme> from the gp_theme cookie BEFORE paint (no FOUC), and
// only for the two known values — a tampered cookie can never inject an arbitrary attribute.
func TestTheme_StampFromCookie(t *testing.T) {
	a := newAPIHarness(t)

	get := func(cookie *http.Cookie) string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, req)
		return rec.Body.String()
	}

	// No cookie → follow the OS → no data-theme attr stamped.
	if body := get(nil); strings.Contains(body, "data-theme") {
		t.Fatalf("no theme cookie must stamp no data-theme attr")
	}
	// gp_theme=dark → <html data-theme="dark">.
	if body := get(&http.Cookie{Name: "gp_theme", Value: "dark"}); !strings.Contains(body, `data-theme="dark"`) {
		t.Fatalf("gp_theme=dark must stamp data-theme=\"dark\"")
	}
	if body := get(&http.Cookie{Name: "gp_theme", Value: "light"}); !strings.Contains(body, `data-theme="light"`) {
		t.Fatalf("gp_theme=light must stamp data-theme=\"light\"")
	}
	// A tampered value is ignored (only light/dark accepted) — no attribute injection.
	if body := get(&http.Cookie{Name: "gp_theme", Value: `evil"><script>`}); strings.Contains(body, "data-theme") {
		t.Fatalf("a tampered theme cookie must be ignored, not stamped")
	}
}

// The toggle's return path preserves the query string, so toggling on a query-driven page (a tab,
// a flash, a filter) returns to the SAME view rather than dropping the query (codex).
func TestTheme_TogglePreservesQuery(t *testing.T) {
	a := newAPIHarness(t)
	req := httptest.NewRequest(http.MethodGet, "/?foo=bar", nil)
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `name="next" value="/?foo=bar"`) {
		t.Fatalf("toggle next must carry the query string (/?foo=bar), got:\n%s", rec.Body.String())
	}
}

// The no-JS toggle (POST /theme) sets/clears the cookie and PRG-redirects back; the return path is
// clamped to a same-origin path so it can't become an open redirect.
func TestTheme_HandlerSetsClearsAndClampsRedirect(t *testing.T) {
	a := newAPIHarness(t)

	rec := a.formPost(t, "/theme", "theme=dark&next=/app", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/app" {
		t.Fatalf("set theme = %d loc=%q, want 303 → /app", rec.Code, rec.Header().Get("Location"))
	}
	if !cookieIs(rec, "gp_theme", "dark", true) {
		t.Fatalf("theme=dark must set gp_theme=dark with a positive max-age; cookies=%v", rec.Result().Cookies())
	}

	rec = a.formPost(t, "/theme", "theme=system&next=/", nil)
	if !cookieIs(rec, "gp_theme", "", false) {
		t.Fatalf("theme=system must CLEAR gp_theme (max-age<0); cookies=%v", rec.Result().Cookies())
	}

	for _, bad := range []string{"https://evil.com", "//evil.com", "/\\evil.com"} {
		rec = a.formPost(t, "/theme", "theme=light&next="+bad, nil)
		if loc := rec.Header().Get("Location"); loc != "/" {
			t.Fatalf("off-site next %q → redirect %q, want clamped to /", bad, loc)
		}
	}
}

func TestSafeLocalNext(t *testing.T) {
	for _, ok := range []string{"/", "/app", "/app/streams/x?q=1", "/p/abc-123"} {
		if got := safeLocalNext(ok); got != ok {
			t.Errorf("safeLocalNext(%q) = %q, want unchanged", ok, got)
		}
	}
	bad := map[string]string{
		"":                   "/",
		"app":                "/", // relative
		"//evil.com":         "/", // protocol-relative authority
		"/\\evil.com":        "/", // backslash authority trick
		"https://evil.com":   "/", // absolute off-site
		"/ok\r\nSet-Cookie:": "/", // header injection
	}
	for in, want := range bad {
		if got := safeLocalNext(in); got != want {
			t.Errorf("safeLocalNext(%q) = %q, want %q", in, got, want)
		}
	}
}

// cookieIs reports whether rec set cookie name to value; wantPersist asserts a positive max-age (set)
// vs a negative one (cleared).
func cookieIs(rec *httptest.ResponseRecorder, name, value string, wantPersist bool) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name != name {
			continue
		}
		if wantPersist {
			return c.Value == value && c.MaxAge > 0
		}
		return c.MaxAge < 0
	}
	return false
}
