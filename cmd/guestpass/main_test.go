package main

import (
	"errors"
	"testing"

	"github.com/rock3r/guest-pass/internal/config"
)

// serve must fail closed (EN-14) before binding a socket: if config.Load rejects the
// environment — here, an empty JWT_SECRET — serve returns that error and never starts
// the server. Without the config wiring this test hangs in ListenAndServe (caught by
// the test timeout), which is the red state.
func TestServeFailsClosedWithoutJWTSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("AUTH_MODE", "")
	t.Setenv("TURN_URL", "")

	err := serve(":0")
	if !errors.Is(err, config.ErrSecretFailClosed) {
		t.Fatalf("expected serve to fail closed with ErrSecretFailClosed, got %v", err)
	}
}
