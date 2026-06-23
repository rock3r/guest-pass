package guide

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// Compile renders each manifest page's Markdown to an HTML fragment + an index, and Load reads
// them back into a Site. This exercises the full build→serve roundtrip on a temp fixture.
func TestCompileAndLoad(t *testing.T) {
	root := t.TempDir()
	dist := t.TempDir()
	srcDir := filepath.Join(root, "docs", "user-guide")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(srcDir, "manifest.json"),
		`{"groups":[{"title":"For hosts","pages":[`+
			`{"slug":"quickstart","title":"Quickstart"},{"slug":"obs","title":"OBS"}]}]}`)
	writeFile(t, filepath.Join(srcDir, "quickstart.md"), "# Quickstart\n\nGet going. See [OBS](obs).\n")
	writeFile(t, filepath.Join(srcDir, "obs.md"), "# OBS\n\nWire it once.\n")

	if err := Compile(root, dist); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	for _, f := range []string{"quickstart.html", "obs.html", "index.json"} {
		if _, err := os.Stat(filepath.Join(dist, "guide", f)); err != nil {
			t.Fatalf("compiled output missing %s: %v", f, err)
		}
	}

	site, err := Load(filepath.Join(dist, "guide"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if site.First() != "quickstart" {
		t.Fatalf("First() = %q, want quickstart (the first manifest page)", site.First())
	}
	if len(site.Groups) != 1 || site.Groups[0].Title != "For hosts" || len(site.Groups[0].Pages) != 2 {
		t.Fatalf("nav groups not preserved: %+v", site.Groups)
	}
	p, ok := site.Page("quickstart")
	if !ok {
		t.Fatal("quickstart page not loaded")
	}
	if p.Title != "Quickstart" || p.Group != "For hosts" {
		t.Fatalf("page metadata = %q/%q, want Quickstart/For hosts", p.Title, p.Group)
	}
	body := string(p.Body)
	if !strings.Contains(body, "<h1") || !strings.Contains(body, "Quickstart") {
		t.Fatalf("Markdown not rendered to HTML: %q", body)
	}
	// A relative inter-page Markdown link stays relative so it resolves under /guide/<slug>.
	if !strings.Contains(body, `href="obs"`) {
		t.Fatalf("relative inter-page link not rendered: %q", body)
	}
	if _, ok := site.Page("nope"); ok {
		t.Fatal("Page() resolved an unknown slug")
	}
}

// Load on a directory that was never compiled (no index.json) must error, so the caller can
// leave /guide unmounted instead of serving a broken page.
func TestLoadUncompiled(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("Load of an uncompiled dir should error")
	}
}

// Compile must fail loudly when the manifest references a page whose Markdown file is missing,
// rather than silently emitting a guide with a dead nav entry.
func TestCompileMissingPage(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "docs", "user-guide")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(srcDir, "manifest.json"),
		`{"groups":[{"title":"G","pages":[{"slug":"ghost","title":"Ghost"}]}]}`)
	if err := Compile(root, t.TempDir()); err == nil {
		t.Fatal("Compile with a manifest page that has no .md should error")
	}
}
