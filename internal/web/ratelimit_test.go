package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter_BurstThenRefill(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewRateLimiter(1, 3) // 1/s, burst 3
	l.now = func() time.Time { return now }

	// Burst of 3 allowed, 4th denied.
	for i := 0; i < 3; i++ {
		if !l.Allow("k") {
			t.Fatalf("request %d should be allowed within burst", i)
		}
	}
	if l.Allow("k") {
		t.Fatal("4th request should be denied (burst exhausted)")
	}
	// After 1s, one token refills.
	now = now.Add(time.Second)
	if !l.Allow("k") {
		t.Fatal("request after 1s refill should be allowed")
	}
	if l.Allow("k") {
		t.Fatal("only one token should have refilled")
	}
}

func TestRateLimiter_PerKeyIsolation(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewRateLimiter(1, 1)
	l.now = func() time.Time { return now }
	if !l.Allow("a") || !l.Allow("b") {
		t.Fatal("distinct keys have independent buckets")
	}
	if l.Allow("a") {
		t.Fatal("key a should be exhausted")
	}
}

func TestRateLimiter_Cleanup(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewRateLimiter(1, 1)
	l.now = func() time.Time { return now }
	l.Allow("k")
	now = now.Add(time.Hour)
	l.Cleanup(time.Minute)
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n != 0 {
		t.Fatalf("idle bucket should be evicted, got %d buckets", n)
	}
}

func TestRateLimiter_Middleware429(t *testing.T) {
	l := NewRateLimiter(0, 1) // 1 burst, no refill
	h := l.Middleware(func(*http.Request) string { return "fixed" })(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	codes := []int{}
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		codes = append(codes, rec.Code)
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v, want [200 429]", codes)
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:54321"
	if got := ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("ClientIP = %q, want 203.0.113.9", got)
	}
}
