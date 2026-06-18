package jobs

import (
	"context"
	"log/slog"
	"time"
)

// Default idle-session-reaper dials (D-40). DEPLOYMENT §8 documents REAP_INTERVAL /
// REAP_IDLE_AFTER as the overrides.
const (
	// DefaultReapInterval is how often the reaper polls active sessions for idleness.
	DefaultReapInterval = time.Minute
	// DefaultReapIdleAfter is how long a session may have zero connected participants before it
	// is auto-ended. Generous so a host's transient drop / reconnect (D-40) is never reaped; the
	// purpose is to free the one-live-session-per-host slot from a genuinely abandoned show.
	DefaultReapIdleAfter = 15 * time.Minute
)

// ReaperConfig configures the idle-session reaper (D-40). Zero fields fall back to the Default*
// values.
type ReaperConfig struct {
	Interval  time.Duration // how often to poll
	IdleAfter time.Duration // end a session whose room has had no participants for this long
}

func (c ReaperConfig) withDefaults() ReaperConfig {
	if c.Interval <= 0 {
		c.Interval = DefaultReapInterval
	}
	if c.IdleAfter <= 0 {
		c.IdleAfter = DefaultReapIdleAfter
	}
	return c
}

// ReaperDeps are the reaper's collaborators, passed as functions so the job stays decoupled from
// the signaling hub and the store (and unit-testable with fakes). main wires them to
// store.ActiveSessionHostIDs, hub.ParticipantCount, and a reap closure (hub.ReapIfIdle +
// store.EndActiveSession).
type ReaperDeps struct {
	// ActiveHosts returns the host ids that currently have an active DB session.
	ActiveHosts func(ctx context.Context) ([]string, error)
	// Participants returns the number of connected greenroom participants in the host's live room
	// (0 when no room is live, or only OBS source pages remain — those don't keep a session alive).
	Participants func(hostID string) int
	// Reap atomically ends an idle host's session: it tears down the (participant-empty) room and
	// stamps ended_at, returning reaped=true. It returns reaped=false (no error) when a participant
	// reconnected in the poll→reap race, so the session was NOT idle and was left alone.
	Reap func(ctx context.Context, hostID string) (reaped bool, err error)
}

// Reaper ends abandoned live sessions (D-40 / AC-2): a session whose room has had zero connected
// participants for IdleAfter is auto-ended (ended_at stamped, room torn down), which both frees
// the one-live-session-per-host slot and makes its guest PII purge-eligible (the 24h purge keys
// off ended_at). Idleness is tracked across polls in idleSince; a reconnect resets the clock.
type Reaper struct {
	deps      ReaperDeps
	cfg       ReaperConfig
	log       *slog.Logger
	now       func() time.Time     // seam for tests; defaults to time.Now
	idleSince map[string]time.Time // host id → when its session was first observed idle
}

// NewReaper builds the reaper. A nil logger defaults to slog.Default.
func NewReaper(deps ReaperDeps, cfg ReaperConfig, log *slog.Logger) *Reaper {
	if log == nil {
		log = slog.Default()
	}
	return &Reaper{deps: deps, cfg: cfg.withDefaults(), log: log, now: time.Now, idleSince: map[string]time.Time{}}
}

// Run polls immediately (to seed idleness, never reaping on the first tick) then every
// cfg.Interval, until ctx is cancelled. Intended to run on its own goroutine.
func (r *Reaper) Run(ctx context.Context) {
	r.sweep(ctx)
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sweep(ctx)
		}
	}
}

// sweep runs one reap pass: refresh the active-session set, reset the idle clock for any host with
// connected participants, and end the sessions idle past the threshold.
func (r *Reaper) sweep(ctx context.Context) {
	hosts, err := r.deps.ActiveHosts(ctx)
	if err != nil {
		r.log.WarnContext(ctx, "idle-session reaper: listing active sessions failed", "error", err)
		return
	}
	active := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		active[h] = true
	}
	// Stop tracking hosts that are no longer live (ended elsewhere), so the map can't grow unbounded.
	for h := range r.idleSince {
		if !active[h] {
			delete(r.idleSince, h)
		}
	}
	now := r.now()
	for _, h := range hosts {
		if r.deps.Participants(h) > 0 {
			delete(r.idleSince, h) // a participant is connected → reset the idle clock
			continue
		}
		since, tracked := r.idleSince[h]
		if !tracked {
			r.idleSince[h] = now // first time seen idle — start the clock, don't reap yet
			continue
		}
		if now.Sub(since) < r.cfg.IdleAfter {
			continue // still within the grace window (allow reconnects, D-40)
		}
		reaped, err := r.deps.Reap(ctx, h)
		if err != nil {
			r.log.WarnContext(ctx, "idle-session reap failed", "host", h, "error", err)
			continue // keep tracking; retry next tick
		}
		// Clear tracking whether or not it reaped: if a participant reconnected in the race
		// (reaped=false) the session is no longer idle and re-tracks from scratch next sweep.
		delete(r.idleSince, h)
		if reaped {
			r.log.InfoContext(ctx, "reaped idle session", "host", h)
		}
	}
}
