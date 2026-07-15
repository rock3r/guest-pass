// Package guide compiles the Markdown user guide (docs/user-guide/*.md) into HTML at build
// time and loads the compiled result for serving at /guide. Authoring is Markdown; the build
// step (cmd/build) renders each page to web/dist/guide/<slug>.html plus an index.json nav
// manifest, and the web layer wraps those fragments in the design chrome at request time.
package guide

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

// PageRef names one guide page in the nav manifest.
type PageRef struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

// Group is a titled section of the guide nav (e.g. "For hosts").
type Group struct {
	Title string    `json:"title"`
	Pages []PageRef `json:"pages"`
}

// Manifest is the authored nav structure (docs/user-guide/manifest.json), copied verbatim to
// web/dist/guide/index.json so the runtime renders the same nav without re-reading source.
type Manifest struct {
	Groups []Group `json:"groups"`
}

// Page is a compiled guide page held in memory at runtime.
type Page struct {
	Slug  string
	Title string
	Group string
	Body  template.HTML // trusted: rendered from our own Markdown, no raw HTML, no scripts
}

// Site is the loaded guide: the nav groups plus every rendered page keyed by slug.
type Site struct {
	Groups []Group
	Pages  map[string]*Page
	first  string
}

// First returns the slug of the first page — the target /guide redirects to.
func (s *Site) First() string { return s.first }

// Page returns the compiled page for slug, or false if there is none.
func (s *Site) Page(slug string) (*Page, bool) { p, ok := s.Pages[slug]; return p, ok }

func newMarkdown() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
}

func readManifest(path string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(path)
	if err != nil {
		return m, fmt.Errorf("guide manifest: %w", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("guide manifest: %w", err)
	}
	return m, nil
}

// Compile renders docs/user-guide/<slug>.md → dist/guide/<slug>.html for every page in the
// manifest and writes dist/guide/index.json (the nav). Run at build time (cmd/build).
func Compile(root, dist string) error {
	srcDir := filepath.Join(root, "docs", "user-guide")
	man, err := readManifest(filepath.Join(srcDir, "manifest.json"))
	if err != nil {
		return err
	}
	outDir := filepath.Join(dist, "guide")
	// Rebuild the output dir from scratch so a page removed from the manifest leaves no stale
	// compiled fragment behind (which would otherwise linger on disk and be reachable via /assets).
	if err := os.RemoveAll(outDir); err != nil {
		return fmt.Errorf("guide outdir: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("guide outdir: %w", err)
	}
	md := newMarkdown()
	for _, g := range man.Groups {
		for _, p := range g.Pages {
			src, err := os.ReadFile(filepath.Join(srcDir, p.Slug+".md"))
			if err != nil {
				return fmt.Errorf("guide page %q: %w", p.Slug, err)
			}
			var buf bytes.Buffer
			if err := md.Convert(src, &buf); err != nil {
				return fmt.Errorf("guide render %q: %w", p.Slug, err)
			}
			if err := os.WriteFile(filepath.Join(outDir, p.Slug+".html"), buf.Bytes(), 0o644); err != nil {
				return fmt.Errorf("guide write %q: %w", p.Slug, err)
			}
		}
	}
	idx, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return fmt.Errorf("guide index: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "index.json"), idx, 0o644); err != nil {
		return fmt.Errorf("guide index: %w", err)
	}
	return nil
}

// Load reads a compiled guide from dir (web/dist/guide). It returns an error if the guide was
// never compiled, so the caller can leave /guide unmounted rather than serving a broken page.
func Load(dir string) (*Site, error) {
	man, err := readManifest(filepath.Join(dir, "index.json"))
	if err != nil {
		return nil, err
	}
	s := &Site{Groups: man.Groups, Pages: make(map[string]*Page)}
	for _, g := range man.Groups {
		for _, p := range g.Pages {
			b, err := os.ReadFile(filepath.Join(dir, p.Slug+".html"))
			if err != nil {
				return nil, fmt.Errorf("guide load %q: %w", p.Slug, err)
			}
			s.Pages[p.Slug] = &Page{Slug: p.Slug, Title: p.Title, Group: g.Title, Body: template.HTML(b)}
			if s.first == "" {
				s.first = p.Slug
			}
		}
	}
	if s.first == "" {
		return nil, fmt.Errorf("guide: no pages in manifest")
	}
	return s, nil
}
