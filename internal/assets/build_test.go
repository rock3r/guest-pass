package assets

import (
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.css"), []byte("body{color:red}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	// obs.js intentionally absent — it must be skipped, not error.

	if err := writeManifest(dir); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest json: %v", err)
	}

	sum := sha512.Sum384([]byte("body{color:red}"))
	want := "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
	if m["app.css"] != want {
		t.Errorf("app.css = %q, want %q", m["app.css"], want)
	}
	if !strings.HasPrefix(m["app.js"], "sha384-") {
		t.Errorf("app.js = %q, want sha384- prefix", m["app.js"])
	}
	if _, ok := m["obs.js"]; ok {
		t.Error("absent obs.js should be skipped, not present in manifest")
	}
}
