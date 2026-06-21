// Package buildinfo exposes the running build's version and VCS revision so the
// in-app source link can resolve to the corresponding source of the running build
// (EN-17). The link is surfaced in the guest/greenroom UI for transparency.
package buildinfo

import "runtime/debug"

// repo is the canonical source repository.
//
// Operators who run a modified build are encouraged to publish their corresponding
// source. A future deploy-time override (e.g. a SOURCE_URL value) will let them
// set this; until then a modified build links to the repository root rather than
// falsely pinning a commit it does not match.
const repo = "https://github.com/rock3r/guest-pass"

// version/commit are overridden at build time via, e.g.:
//
//	go build -ldflags "-X github.com/rock3r/guest-pass/internal/buildinfo.version=v1.2.3"
var (
	version = "dev"
	commit  = ""
)

// Version returns the build version (a release tag, or "dev" for local builds).
func Version() string { return version }

// Commit returns the VCS revision the binary was built from, or "" if unknown.
func Commit() string {
	if commit != "" {
		return commit
	}
	return vcsSetting("vcs.revision")
}

// Modified reports whether the binary was built from a dirty working tree. A build
// injected with an ldflags commit is treated as a clean release build.
func Modified() bool {
	if commit != "" {
		return false
	}
	return vcsSetting("vcs.modified") == "true"
}

// SourceURL returns the source link for the running build. It pins the exact revision
// ONLY when that revision faithfully corresponds to the running code (a known commit
// from a clean tree); a modified or unknown build links to the repository root instead
// of misrepresenting the corresponding source (EN-17).
func SourceURL() string { return sourceURL(Commit(), Modified()) }

func sourceURL(commit string, modified bool) string {
	if commit != "" && !modified {
		return repo + "/tree/" + commit
	}
	return repo
}

func vcsSetting(key string) string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == key {
				return s.Value
			}
		}
	}
	return ""
}
