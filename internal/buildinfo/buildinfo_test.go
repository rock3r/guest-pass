package buildinfo

import (
	"strings"
	"testing"
)

func TestVersionNeverEmpty(t *testing.T) {
	if Version() == "" {
		t.Fatal("Version() must never be empty (defaults to \"dev\")")
	}
}

func TestSourceURLAlwaysPointsAtRepo(t *testing.T) {
	if got := SourceURL(); !strings.HasPrefix(got, repo) {
		t.Fatalf("SourceURL() = %q, want it to start with %q", got, repo)
	}
}

// EN-17: the §13 source link must resolve to the EXACT running revision, not just
// the repository root, whenever the commit is known.
func TestSourceURLPinsCommitWhenKnown(t *testing.T) {
	old := commit
	t.Cleanup(func() { commit = old })

	commit = "abc123"
	if got := SourceURL(); !strings.HasSuffix(got, "/tree/abc123") {
		t.Fatalf("SourceURL() = %q, want it to pin /tree/abc123", got)
	}
}

// EN-17 / AGPL §13: a MODIFIED build must NOT pin a commit, because the running
// code does not correspond to that revision — doing so points network users at the
// wrong "corresponding source".
func TestSourceURLByState(t *testing.T) {
	cases := []struct {
		name     string
		commit   string
		modified bool
		want     string
	}{
		{"clean known commit pins it", "abc123", false, repo + "/tree/abc123"},
		{"modified build does NOT pin (would mislead)", "abc123", true, repo},
		{"unknown commit falls back to repo root", "", false, repo},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sourceURL(c.commit, c.modified); got != c.want {
				t.Fatalf("sourceURL(%q, %v) = %q, want %q", c.commit, c.modified, got, c.want)
			}
		})
	}
}

func TestCommitOverrideWins(t *testing.T) {
	old := commit
	t.Cleanup(func() { commit = old })

	commit = "deadbeef"
	if got := Commit(); got != "deadbeef" {
		t.Fatalf("Commit() = %q, want the ldflags override %q", got, "deadbeef")
	}
}
