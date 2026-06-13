package web

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
)

//go:embed templates/*.html
var templateFS embed.FS

// pageData is passed to every server-rendered page. SourceURL is the AGPL §13 link to
// the corresponding source of the running build, surfaced on every page including
// guest-facing ones (EN-17). Nonce is the per-request CSP script nonce (§3.5).
type pageData struct {
	Title           string
	SourceURL       string
	StyleIntegrity  string
	ScriptIntegrity string // SRI for the app.js island bundle (pages that mount an island)
	DevLogin        bool
	Nonce           string
	StreamTitle     string // pass landing page only
	GuestName       string // pass landing page only
}

// renderer holds the parsed page templates and the per-build constants injected into
// every render.
type renderer struct {
	pages           map[string]*template.Template
	sourceURL       string
	styleIntegrity  string
	scriptIntegrity string
	devLogin        bool
}

// pageFiles are the server-rendered pages; each defines a "content" template composed
// into base.html.
var pageFiles = []string{"landing.html", "signin.html", "pass.html"}

// newRenderer parses the embedded templates. sourceURL is the AGPL §13 source link;
// styleIntegrity is the SRI hash for the app CSS bundle (empty when no build manifest
// is present, e.g. in tests); devLogin toggles the dev sign-in affordance.
func newRenderer(sourceURL, styleIntegrity, scriptIntegrity string, devLogin bool) (*renderer, error) {
	pages := make(map[string]*template.Template, len(pageFiles))
	for _, p := range pageFiles {
		t, err := template.New("base").ParseFS(templateFS, "templates/base.html", "templates/"+p)
		if err != nil {
			return nil, err
		}
		pages[p] = t
	}
	return &renderer{pages: pages, sourceURL: sourceURL, styleIntegrity: styleIntegrity, scriptIntegrity: scriptIntegrity, devLogin: devLogin}, nil
}

// render executes a page into a buffer first, so a template error never writes a partial
// response, then flushes it as HTML. The per-build fields (source link, SRI, dev flag,
// nonce) are filled here; callers supply page-specific fields in data.
func (rd *renderer) render(w http.ResponseWriter, r *http.Request, page string, data pageData) {
	t, ok := rd.pages[page]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data.SourceURL = rd.sourceURL
	data.StyleIntegrity = rd.styleIntegrity
	data.ScriptIntegrity = rd.scriptIntegrity
	data.DevLogin = rd.devLogin
	data.Nonce = NonceFromContext(r.Context())
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base", data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (rd *renderer) landing(w http.ResponseWriter, r *http.Request) {
	rd.render(w, r, "landing.html", pageData{Title: "Guest management for live streams"})
}

func (rd *renderer) signin(w http.ResponseWriter, r *http.Request) {
	rd.render(w, r, "signin.html", pageData{Title: "Host sign-in"})
}

// passLandingPage renders the guest's magic-link landing (side-effect-free, EN-10): it
// shows who invited them and to which stream, with an entry action that marks the pass
// opened only on an explicit client action (the device-check, in M2).
func (rd *renderer) passLandingPage(w http.ResponseWriter, r *http.Request, streamTitle, guestName string) {
	rd.render(w, r, "pass.html", pageData{Title: "Your guest pass", StreamTitle: streamTitle, GuestName: guestName})
}

// healthz is the readiness endpoint (RF-21). It is registered only after migrations have
// applied and the server is wired, so reaching it means the binary is ready to serve.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}
