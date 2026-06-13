//go:build !dev

package config

// devBuild reports whether the dev-auth seam (AUTH_MODE=dev) is compiled into this
// binary. In a release build (the default — no `dev` build tag) it is false, so the
// dev-auth code path does not exist at all (AD-8 / RF-4 / CONVENTIONS §1.5). This is
// strictly stronger than a runtime "refuse if set in prod" check: there is nothing
// to disable, because nothing was compiled in.
const devBuild = false
