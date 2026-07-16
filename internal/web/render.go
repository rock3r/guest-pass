package web

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/rock3r/guest-pass/internal/guide"
	"github.com/rock3r/guest-pass/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

// pageData is passed to every server-rendered page. SourceURL is the link to the
// corresponding source of the running build, surfaced on every user-facing page
// (EN-17). Nonce is the per-request CSP script nonce (§3.5).
type pageData struct {
	Title           string
	SourceURL       string
	StyleIntegrity  string
	ScriptIntegrity string // SRI for the JS bundle the page loads (app.js, or obs.js for source pages)
	DevLogin        bool
	Nonce           string
	StreamTitle     string     // pass landing page only
	GuestName       string     // pass landing page only
	WatchURL        string     // pass landing page only — public "watch live" link if the stream linked a channel (D-29)
	ReportToken     string     // pass landing page only — raw magic-link token, for the "report this invite" link (D-42)
	Slot            string     // OBS source page only — the opaque slot label (EN-15)
	Host            *navHost   // host-app pages only — the signed-in host for the shell nav (nil elsewhere)
	Nav             string     // host-app pages only — the active nav item ("dashboard"/"calendar"/"settings")
	CurrentStream   *navStream // host-app stream-scoped pages only — the active stream for the nav breadcrumb (nil elsewhere)
	Data            any        // host-app pages only — the page-specific payload, rendered server-side (D-32)
	Theme           string     // explicit dark-mode choice ("light"/"dark") from the gp_theme cookie; "" = follow OS (D-9)
	Path            string     // the current request path — the theme toggle returns here after setting the cookie (PRG)
}

// themeCookie holds the host/guest's explicit dark-mode choice ("light"/"dark"); absent means follow
// the OS (prefers-color-scheme). Read on every render to stamp <html data-theme> before paint (D-9).
const themeCookie = "gp_theme"

// themeChoice extracts the explicit theme from the request, accepting ONLY the two known values so a
// tampered cookie can't inject an arbitrary data-theme attribute. Anything else → "" (follow OS).
func themeChoice(r *http.Request) string {
	if c, err := r.Cookie(themeCookie); err == nil && (c.Value == "light" || c.Value == "dark") {
		return c.Value
	}
	return ""
}

// navHost is the authenticated host's identity surfaced in the host-app shell nav. It
// carries the display name and avatar fields for the nav avatar chip, but never email or any
// token in nav-unsafe positions (EN-16/D-37). AvatarColor is a CSS color derived from the
// host's name via the avColors hash (matching the UI's avColor() function). AvatarInitials
// is up to 2 uppercase initials extracted from the name's space-separated words. Email is
// included for the "signed in as" tooltip (host's own page only — not a cross-host leak, EN-8).
type navHost struct {
	Name           string
	IsAdmin        bool // shows the admin-console nav link (D-14); admin is a host flag, not a separate identity
	Email          string
	AvatarColor    string // CSS color computed from name using avColors hash (for client-side parity)
	AvatarIdx      int    // index into avColors → the .av-N CSS class (SSR avatars can't use inline style; CSP style-src 'self')
	AvatarInitials string // up to 2 uppercase initials from the name
}

// avColors is the fixed palette used by the UI's avColor() function (ui.jsx). The Go
// implementation must stay in sync with the JS one so SSR and CSR produce the same avatar.
var avColors = []string{
	"#ff7a59", "#5b8def", "#34a26b", "#c061cb", "#e0a82e", "#3bb2c9", "#e0617a", "#7a6cf0", "#d77a32",
}

// hostNav builds a navHost from a store.Host record. It derives AvatarColor and
// AvatarInitials using the same algorithm as the UI's avColor()/initials() functions.
func hostNav(h *store.Host) *navHost {
	// avColor: h=0; for each char c: h = (h*31 + int(c)) % len(avColors)
	hv := 0
	for _, c := range h.Name {
		hv = (hv*31 + int(c)) % len(avColors)
	}
	color := avColors[hv]

	// Initials: first char of each space-separated word, up to 2, uppercase.
	var initials []byte
	for _, word := range strings.Fields(h.Name) {
		if len(initials) >= 2 {
			break
		}
		if len(word) > 0 {
			r := rune(word[0])
			if r >= 'a' && r <= 'z' {
				r -= 32
			}
			initials = append(initials, byte(r))
		}
	}

	return &navHost{
		Name:           h.Name,
		IsAdmin:        h.IsAdmin,
		Email:          h.Email,
		AvatarColor:    color,
		AvatarIdx:      hv,
		AvatarInitials: string(initials),
	}
}

// navStream carries the current stream's identity for stream-specific pages (e.g.
// streamdetail, sources). Templates can use it to render the stream title in the nav
// breadcrumb without re-passing it through detailData. Non-nil only on stream-scoped pages.
type navStream struct {
	ID     string
	Title  string
	IsLive bool
}

// renderer holds the parsed page templates and the per-build constants injected into
// every render. manifest maps each emitted bundle to its SRI hash (empty when no build
// manifest is present, e.g. in tests, so pages render without integrity attributes).
type renderer struct {
	pages       map[string]*template.Template
	obsTmpl     *template.Template
	landingTmpl *template.Template
	guideTmpl   *template.Template
	guideSite   *guide.Site // compiled user guide; nil when not built (then /guide 404s)
	sourceURL   string
	manifest    map[string]string
	devLogin    bool
}

// pageFiles are the public/guest server-rendered pages composed into base.html (each
// defines a "content" template). The OBS source page is NOT one of these — it is a
// standalone, chromeless, font-free page (EN-13) with its own template. The landing page is
// also NOT in this list — it is a standalone template parsed separately (like obs.html).
// error.html (the denial/error screens, M5.5) is composed into base.html — the PUBLIC shell, NOT
// appbase.html: a suspended/pending/non-admin host must not see a host nav whose every link would
// itself 403. It still carries the source-link footer (EN-17).
var pageFiles = []string{"signin.html", "pass.html", "report.html", "error.html", "stats.html"}

// appPageFiles are the host-app shell pages (D-32: server-rendered, no JS) composed into
// appbase.html — which adds the host nav + "signed in as" + sign-out chrome that the
// public pages do not carry. Each also defines a "content" template; parsed in their own
// template.Template instances so "base" can mean appbase.html here and base.html there
// with no name clash.
var appPageFiles = []string{"dashboard.html", "streamedit.html", "calendar.html", "streamdetail.html", "settings.html", "admin.html", "greenroom.html"}

// newRenderer parses the embedded templates. sourceURL is the build source link (EN-17);
// manifest carries the SRI hashes (nil/empty in tests); devLogin toggles dev sign-in.
func newRenderer(sourceURL string, manifest map[string]string, devLogin bool) (*renderer, error) {
	if manifest == nil {
		manifest = map[string]string{}
	}
	funcs := template.FuncMap{
		"assetURL": func(name string) string { return bundledAssetURL(name, manifest[name]) },
		// OBS source pages must load their runtime assets beneath /s/{slot}. Operators commonly
		// exempt that token-gated path from an edge-access policy; loading the general /_gp assets
		// instead turns a Cloudflare Access redirect into a CSP failure and a black source.
		"sourceAssetURL": func(slot, name string) string {
			return sourceAssetURL(slot, name, manifest[name])
		},
	}
	pages := make(map[string]*template.Template, len(pageFiles)+len(appPageFiles))
	for _, p := range pageFiles {
		t, err := template.New("base").Funcs(funcs).ParseFS(templateFS, "templates/base.html", "templates/"+p)
		if err != nil {
			return nil, err
		}
		pages[p] = t
	}
	for _, p := range appPageFiles {
		t, err := template.New("base").Funcs(funcs).ParseFS(templateFS, "templates/appbase.html", "templates/"+p)
		if err != nil {
			return nil, err
		}
		pages[p] = t
	}
	obs, err := template.New("obs.html").Funcs(funcs).ParseFS(templateFS, "templates/obs.html")
	if err != nil {
		return nil, err
	}
	landing, err := template.New("landing.html").Funcs(funcs).ParseFS(templateFS, "templates/landing.html")
	if err != nil {
		return nil, err
	}
	guideT, err := template.New("guide.html").Funcs(funcs).ParseFS(templateFS, "templates/guide.html")
	if err != nil {
		return nil, err
	}
	return &renderer{
		pages:       pages,
		obsTmpl:     obs,
		landingTmpl: landing,
		guideTmpl:   guideT,
		sourceURL:   sourceURL,
		manifest:    manifest,
		devLogin:    devLogin,
	}, nil
}

// bundledAssetURL versions a static asset by its SRI digest. Asset filenames are stable between
// deploys, so this prevents a browser or CDN from pairing freshly rendered HTML with an older,
// cached CSS or JavaScript response that SRI will correctly reject.
func bundledAssetURL(name, integrity string) string {
	path := "/_gp/" + name
	if integrity == "" {
		return path
	}
	return path + "?v=" + url.QueryEscape(integrity)
}

// sourceAssetURL mirrors bundledAssetURL for the tiny OBS runtime, but scopes the request to
// the public, token-gated source prefix. This avoids requiring an embedded OBS Chromium instance
// to complete an interactive edge-access login for its stylesheet or module bundle.
func sourceAssetURL(slot, name, integrity string) string {
	path := "/s/" + url.PathEscape(slot) + "/_gp/" + url.PathEscape(name)
	if integrity == "" {
		return path
	}
	return path + "?v=" + url.QueryEscape(integrity)
}

// loadGuide loads the compiled user guide from dir (web/dist/guide). A failure (guide not
// built) leaves guideSite nil, so the /guide routes answer 404 rather than erroring.
func (rd *renderer) loadGuide(dir string) {
	if s, err := guide.Load(dir); err == nil {
		rd.guideSite = s
	}
}

// guidePageData backs the standalone guide template (public, no auth). Body is the compiled
// Markdown HTML for the current page; Groups drives the section nav.
type guidePageData struct {
	Title          string
	Theme          string
	Path           string
	StyleIntegrity string
	SourceURL      string
	Groups         []guide.Group
	Current        string
	PageTitle      string
	Body           template.HTML
}

// guideIndex redirects /guide to the first page so every guide page lives under /guide/<slug>
// (keeps the Markdown's relative inter-page links resolving correctly).
func (rd *renderer) guideIndex(w http.ResponseWriter, r *http.Request) {
	if rd.guideSite == nil {
		rd.notFound(w, r)
		return
	}
	http.Redirect(w, r, "/guide/"+rd.guideSite.First(), http.StatusFound)
}

// guidePage renders one compiled guide page wrapped in the design chrome.
func (rd *renderer) guidePage(w http.ResponseWriter, r *http.Request) {
	if rd.guideSite == nil {
		rd.notFound(w, r)
		return
	}
	p, ok := rd.guideSite.Page(chi.URLParam(r, "slug"))
	if !ok {
		rd.notFound(w, r)
		return
	}
	data := guidePageData{
		Title:          p.Title + " · Guide",
		Theme:          themeChoice(r),
		Path:           r.URL.RequestURI(),
		StyleIntegrity: rd.manifest["app.css"],
		SourceURL:      rd.sourceURL,
		Groups:         rd.guideSite.Groups,
		Current:        p.Slug,
		PageTitle:      p.Title,
		Body:           p.Body,
	}
	var buf bytes.Buffer
	if err := rd.guideTmpl.Execute(&buf, data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// render executes a base-composed page with a 200 OK status. See renderStatus.
func (rd *renderer) render(w http.ResponseWriter, r *http.Request, page string, data pageData) {
	rd.renderStatus(w, r, page, http.StatusOK, data)
}

// renderStatus executes a base-composed page into a buffer first, so a template error never
// writes a partial response, then flushes it as HTML with the given status. The per-build fields
// (source link, SRI, dev flag, nonce) are filled here; callers supply page-specific fields in
// data. Error screens (M5.5) use this with a 401/403/500 status.
func (rd *renderer) renderStatus(w http.ResponseWriter, r *http.Request, page string, status int, data pageData) {
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
	data.Theme = themeChoice(r)    // stamp the explicit dark-mode choice (no-FOUC); "" = follow OS
	data.Path = r.URL.RequestURI() // path + query, so the toggle returns to the SAME view — a query-
	//                                driven page (e.g. ?tab=sources, ?error=…) keeps its state (codex).
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base", data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// landing renders the public marketing / product landing page as a standalone template
// (no base.html chrome — the landing has its own full-page layout).
func (rd *renderer) landing(w http.ResponseWriter, r *http.Request) {
	data := pageData{
		Title:           "Guest management for live streams",
		SourceURL:       rd.sourceURL,
		StyleIntegrity:  rd.manifest["app.css"],
		ScriptIntegrity: rd.manifest["app.js"],
		Nonce:           NonceFromContext(r.Context()),
		Theme:           themeChoice(r),
		Path:            r.URL.RequestURI(),
	}
	var buf bytes.Buffer
	if err := rd.landingTmpl.Execute(&buf, data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (rd *renderer) signin(w http.ResponseWriter, r *http.Request) {
	rd.render(w, r, "signin.html", pageData{Title: "Host sign-in"})
}

// passLandingPage renders the guest's magic-link landing (side-effect-free, EN-10): it
// shows who invited them and to which stream, with an entry action that marks the pass
// opened only on an explicit client action (the device-check, in M2).
func (rd *renderer) passLandingPage(w http.ResponseWriter, r *http.Request, streamTitle, guestName, watchURL, reportToken string) {
	rd.render(w, r, "pass.html", pageData{
		Title: "Your guest pass", StreamTitle: streamTitle, GuestName: guestName,
		WatchURL: watchURL, ReportToken: reportToken,
	})
}

// reportCategory is one option in the public abuse-report form.
type reportCategory struct{ Value, Label string }

// reportCategoryOptions maps each stored category to its human label (D-42).
var reportCategoryOptions = []reportCategory{
	{store.ReportSpam, "Spam"},
	{store.ReportDontKnow, "I don't know this person"},
	{store.ReportPhishing, "Phishing or a scam"},
	{store.ReportHarassment, "Harassment"},
	{store.ReportOther, "Something else"},
}

// reportData backs the public abuse-report form (D-42 / AC-11).
type reportData struct {
	Token      string
	Sent       bool // thank-you state after a successful submit
	Error      bool // both-fields-required validation error
	Categories []reportCategory
}

// reportPage renders the public "report this invite" form (D-42): category + message, both required.
// No auth — the magic-link token in the path is the only identifier, and it is resolved to the
// reporter/host server-side (EN-24), never trusted from the form.
func (rd *renderer) reportPage(w http.ResponseWriter, r *http.Request, token string, sent, hasErr bool) {
	rd.render(w, r, "report.html", pageData{
		Title: "Report this invite",
		Data:  reportData{Token: token, Sent: sent, Error: hasErr, Categories: reportCategoryOptions},
	})
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
