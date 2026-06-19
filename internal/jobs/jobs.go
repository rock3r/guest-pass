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
	// DefaultReportRetention is how long an abuse report's identifying content (reporter email +
	// message) is kept before it is anonymized to NULL — the D-42 "review window" (D-37).
	DefaultReportRetention = 30 * 24 * time.Hour
)

// PurgeStore is the store subset the purge job needs (*store.Store satisfies it). Narrowing it to
// these methods keeps the job unit-testable with a fake and off the rest of the store.
type PurgeStore interface {
	PurgeGuestPII(ctx context.Context, now, retentionSecs, graceSecs int64) (int64, error)
	// AnonymizeExpiredReports nulls reporter email + message on reports past the review window
	// (D-42/D-37), returning the count anonymized.
	AnonymizeExpiredReports(ctx context.Context, now, retentionSecs int64) (int64, error)
}

// PurgeConfig configures the periodic retention sweeps (D-37): the 24h guest-PII purge and the
// abuse-report anonymization. Zero fields fall back to the Default* values, so a caller can pass
// only what it overrides.
type PurgeConfig struct {
	Interval        time.Duration // how often to sweep
	Retention       time.Duration // keep guest PII this long after stream end
	Grace           time.Duration // extra window after a never-run stream's scheduled end
	ReportRetention time.Duration // keep an abuse report's identifying content this long (D-42)
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
	if c.ReportRetention <= 0 {
		c.ReportRetention = DefaultReportRetention
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
//
// It triggers eligibility one interval EARLY so the user-facing "deleted within RETENTION of
// stream end" promise holds (D-37). With discrete sweeps, a pass that crosses the retention
// cutoff just after a tick would otherwise wait nearly a full interval for the next sweep —
// up to retention+interval after stream end. Using (retention - interval) as the eligibility
// age bounds the worst case at (retention - interval) + interval = retention. Clamped to 0
// when the interval is misconfigured >= retention (the cadence alone then can't meet the SLA;
// 0 means "purge anything past stream end on the next sweep", the best the cadence allows).
func (p *Purger) sweep(ctx context.Context) (int64, error) {
	age := p.cfg.Retention - p.cfg.Interval
	if age < 0 {
		age = 0
	}
	return p.store.PurgeGuestPII(ctx,
		p.now().Unix(),
		int64(age.Seconds()),
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

// anonymizeReports nulls the identifying content of abuse reports past the review window (D-42).
// Unlike the guest-PII purge it has no sub-interval SLA, so it uses the full retention age.
func (p *Purger) anonymizeReports(ctx context.Context) (int64, error) {
	return p.store.AnonymizeExpiredReports(ctx, p.now().Unix(), int64(p.cfg.ReportRetention.Seconds()))
}

// runOnce performs a single retention sweep — the guest-PII purge plus the abuse-report
// anonymization — and logs each outcome (count only, never PII; EN-16/EN-20). The two are
// independent: one failing does not skip the other.
func (p *Purger) runOnce(ctx context.Context) {
	if n, err := p.sweep(ctx); err != nil {
		p.log.WarnContext(ctx, "guest-PII purge sweep failed", "error", err)
	} else if n > 0 {
		p.log.InfoContext(ctx, "guest-PII purge cleared expired passes", "count", n)
	}
	if n, err := p.anonymizeReports(ctx); err != nil {
		p.log.WarnContext(ctx, "abuse-report anonymize sweep failed", "error", err)
	} else if n > 0 {
		p.log.InfoContext(ctx, "anonymized expired abuse reports", "count", n)
	}
}
