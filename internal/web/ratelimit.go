package web

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// RateLimiter is an in-memory, per-key token-bucket limiter. It is single-instance and
// NOT persisted (AA-1): its state resets on restart, which is acceptable for the v1
// single-process deployment and a documented limitation. Used to bound token-scanning
// and connection-storm abuse on sensitive routes (D-36 / §5).
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64 // tokens added per second
	burst   float64 // bucket capacity
	now     func() time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter builds a limiter allowing perSecond requests/key sustained, with a
// burst capacity.
func NewRateLimiter(perSecond float64, burst int) *RateLimiter {
	return &RateLimiter{
		buckets: map[string]*tokenBucket{},
		rate:    perSecond,
		burst:   float64(burst),
		now:     time.Now,
	}
}

// Allow reports whether a request for key may proceed, consuming one token if so.
func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Cleanup evicts buckets untouched for at least idleFor, bounding memory against a
// scan across many distinct keys. The server runs this on a ticker.
func (l *RateLimiter) Cleanup(idleFor time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, b := range l.buckets {
		if now.Sub(b.last) >= idleFor {
			delete(l.buckets, k)
		}
	}
}

// Middleware rejects over-limit requests with 429, keyed by keyFn (e.g. ClientIP).
func (l *RateLimiter) Middleware(keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(keyFn(r)) {
				http.Error(w, "too many requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP returns the request's client IP from RemoteAddr. Trusting a forwarded header
// is a per-deployment decision (reverse-proxy trust) deferred past the v1 default.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
