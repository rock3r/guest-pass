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
// the corresponding source of the running build, surfaced on every user-facing page
// (EN-17). Nonce is the per-request CSP script nonce (§3.5).
type pageData struct {
	Title           string
	SourceURL       string
	StyleIntegrity  string
	ScriptIntegrity string // SRI for the JS bundle the page loads (app.js, or obs.js for source pages)
	DevLogin        bool
	Nonce           string
	StreamTitle     string // pass landing page only
	GuestName       string // pass landing page only
	Slot            string // OBS source page only — the opaque slot label (EN-15)
}

// renderer holds the parsed page templates and the per-build constants injected into
// every render. manifest maps each emitted bundle to its SRI hash (empty when no build
// manifest is present, e.g. in tests, so pages render without integrity attributes).
type renderer struct {
	pages     map[string]*template.Template
	obsTmpl   *template.Template
	sourceURL string
	manifest  map[string]string
	devLogin  bool
}

// pageFiles are the server-rendered pages composed into base.html (each defines a
// "content" template). The OBS source page is NOT one of these — it is a standalone,
// chromeless, font-free page (EN-13) with its own template.
var pageFiles = []string{"landing.html", "signin.html", "pass.html", "greenroom.html"}

// newRenderer parses the embedded templates. sourceURL is the AGPL §13 source link;
// manifest carries the SRI hashes (nil/empty in tests); devLogin toggles dev sign-in.
func newRenderer(sourceURL string, manifest map[string]string, devLogin bool) (*renderer, error) {
	pages := make(map[string]*template.Template, len(pageFiles))
	for _, p := range pageFiles {
		t, err := template.New("base").ParseFS(templateFS, "templates/base.html", "templates/"+p)
		if err != nil {
			return nil, err
		}
		pages[p] = t
	}
	obs, err := template.New("obs.html").ParseFS(templateFS, "templates/obs.html")
	if err != nil {
		return nil, err
	}
	if manifest == nil {
		manifest = map[string]string{}
	}
	return &renderer{pages: pages, obsTmpl: obs, sourceURL: sourceURL, manifest: manifest, devLogin: devLogin}, nil
}

// render executes a base-composed page into a buffer first, so a template error never
// writes a partial response, then flushes it as HTML. The per-build fields (source link,
// SRI, dev flag, nonce) are filled here; callers supply page-specific fields in data.
func (rd *renderer) render(w http.ResponseWriter, r *http.Request, page string, data pageData) {
	t, ok := rd.pages[page]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data.SourceURL = rd.sourceURL
	data.StyleIntegrity = rd.manifest["app.css"]
	data.ScriptIntegrity = rd.manifest["app.js"]
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

// greenroom renders the host's thin monitor page (host-authenticated upstream): it mounts
// the host-monitor island, which consumes each guest's camera over P2P (PR-7).
func (rd *renderer) greenroom(w http.ResponseWriter, r *http.Request) {
	rd.render(w, r, "greenroom.html", pageData{Title: "Greenroom"})
}

// passLandingPage renders the guest's magic-link landing (side-effect-free, EN-10): it
// shows who invited them and to which stream, with an entry action that marks the pass
// opened only on an explicit client action (the device-check, in M2).
func (rd *renderer) passLandingPage(w http.ResponseWriter, r *http.Request, streamTitle, guestName string) {
	rd.render(w, r, "pass.html", pageData{Title: "Your guest pass", StreamTitle: streamTitle, GuestName: guestName})
}

// sourcePage renders the chromeless OBS cam source page (EN-13): a standalone, font-free,
// transparent page that loads the minimal obs.js bundle. The slot label is opaque (EN-15);
// the source token is NOT embedded — obs.js reads it from the URL query. It does not use
// base.html (no footer / §13 chrome — it is a render surface, not user-facing UI).
func (rd *renderer) sourcePage(w http.ResponseWriter, r *http.Request, slot string) {
	data := pageData{
		Slot:            slot,
		StyleIntegrity:  rd.manifest["obs.css"],
		ScriptIntegrity: rd.manifest["obs.js"],
		Nonce:           NonceFromContext(r.Context()),
	}
	var buf bytes.Buffer
	if err := rd.obsTmpl.Execute(&buf, data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// healthz is the readiness endpoint (RF-21). It is registered only after migrations have
// applied and the server is wired, so reaching it means the binary is ready to serve.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}
