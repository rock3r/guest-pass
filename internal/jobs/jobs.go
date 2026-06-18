// Package jobs holds GuestPass's in-binary background tickers (DESIGN §9.7) — no external
// scheduler or cron. v1 owns the 24h guest-PII purge (D-37); the idle-session reaper (D-40)
// joins it in a later step. Each job runs on its own goroutine, sweeps once on start, then on
// a fixed interval, and stops when its context is cancelled (the server's drain cancels it).
//
// Jobs NEVER log PII or tokens (EN-16/EN-20): the purge logs only a count of cleared passes,
// never a name, email, or id of the data it cleared.
package jobs

import (
	"context"
	"log/slog"
	"time"
)

// Default policy numbers, used when a caller leaves a PurgeConfig field zero.
const (
	// DefaultPurgeInterval is how often the purge sweeps (DESIGN §9.7 "hourly sweep").
	DefaultPurgeInterval = time.Hour
	// DefaultPurgeRetention is how long guest PII is kept after stream end (D-37).
	DefaultPurgeRetention = 24 * time.Hour
	// DefaultPurgeGrace extends the never-run-stream window past its scheduled end before
	// the retention clock starts — matches the D-5 pass-expiry grace (scheduled end + 30m).
	DefaultPurgeGrace = 30 * time.Minute
)

// PurgeStore is the store subset the purge job needs (*store.Store satisfies it). Narrowing
// it to one method keeps the job unit-testable with a fake and off the rest of the store.
type PurgeStore interface {
	PurgeGuestPII(ctx context.Context, now, retentionSecs, graceSecs int64) (int64, error)
}

// PurgeConfig configures the 24h guest-PII purge (D-37). Zero fields fall back to the
// Default* values, so a caller can pass only what it overrides.
type PurgeConfig struct {
	Interval  time.Duration // how often to sweep
	Retention time.Duration // keep guest PII this long after stream end
	Grace     time.Duration // extra window after a never-run stream's scheduled end
}

func (c PurgeConfig) withDefaults() PurgeConfig {
	if c.Interval <= 0 {
		c.Interval = DefaultPurgeInterval
	}
	if c.Retention <= 0 {
		c.Retention = DefaultPurgeRetention
	}
	if c.Grace <= 0 {
		c.Grace = DefaultPurgeGrace
	}
	return c
}

// Purger runs the periodic guest-PII purge.
type Purger struct {
	store PurgeStore
	cfg   PurgeConfig
	log   *slog.Logger
	now   func() time.Time // seam for tests; defaults to time.Now
}

// NewPurger builds the purge job. A nil logger defaults to slog.Default.
func NewPurger(store PurgeStore, cfg PurgeConfig, log *slog.Logger) *Purger {
	if log == nil {
		log = slog.Default()
	}
	return &Purger{store: store, cfg: cfg.withDefaults(), log: log, now: time.Now}
}

// sweep runs one purge and returns the number of passes cleared.
func (p *Purger) sweep(ctx context.Context) (int64, error) {
	return p.store.PurgeGuestPII(ctx,
		p.now().Unix(),
		int64(p.cfg.Retention.Seconds()),
		int64(p.cfg.Grace.Seconds()))
}

// Run sweeps once immediately (so a restart cleans up promptly) and then every
// cfg.Interval, until ctx is cancelled. It logs the cleared count per sweep and a warning on
// error — never PII (EN-16/EN-20). Intended to be launched on its own goroutine.
func (p *Purger) Run(ctx context.Context) {
	p.runOnce(ctx)
	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.runOnce(ctx)
		}
	}
}

// runOnce performs a single sweep and logs the outcome (count only, never PII).
func (p *Purger) runOnce(ctx context.Context) {
	n, err := p.sweep(ctx)
	if err != nil {
		p.log.WarnContext(ctx, "guest-PII purge sweep failed", "error", err)
		return
	}
	if n > 0 {
		p.log.InfoContext(ctx, "guest-PII purge cleared expired passes", "count", n)
	}
}
