//go:build dev

package config

// devBuild is true only in a `-tags dev` build, which compiles in the AUTH_MODE=dev
// seam (a fake host session without Google, for local dev + hermetic tests, AD-8).
// A dev build additionally refuses a non-loopback BASE_URL (RF-4), so it can never be
// pointed at a real origin.
const devBuild = true
