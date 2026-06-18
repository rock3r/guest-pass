package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeReaperDeps drives the reaper deterministically: a fixed active-host list, a per-host
// participant count, and a recorded Reap that returns a configurable (reaped, err).
type fakeReaperDeps struct {
	mu        sync.Mutex
	active    []string
	parts     map[string]int
	reaped    []string
	reapOK    bool
	reapErr   error
	activeErr error
}

func (f *fakeReaperDeps) deps() ReaperDeps {
	return ReaperDeps{
		ActiveHosts: func(context.Context) ([]string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.activeErr != nil {
				return nil, f.activeErr
			}
			return append([]string(nil), f.active...), nil
		},
		Participants: func(h string) int {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.parts[h]
		},
		Reap: func(_ context.Context, h string) (bool, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.reapErr != nil {
				return false, f.reapErr
			}
			if f.reapOK {
				f.reaped = append(f.reaped, h)
			}
			return f.reapOK, f.reapErr
		},
	}
}

func (f *fakeReaperDeps) reapedHosts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reaped...)
}

// newTestReaper builds a reaper with a controllable clock.
func newTestReaper(deps ReaperDeps, idleAfter time.Duration, clock *time.Time) *Reaper {
	r := NewReaper(deps, ReaperConfig{Interval: time.Minute, IdleAfter: idleAfter}, discardLogger())
	r.now = func() time.Time { return *clock }
	return r
}

// TestReaper_ReapsAfterThreshold: a session idle (0 participants) for >= IdleAfter is reaped; not
// before. The clock advances between sweeps.
func TestReaper_ReapsAfterThreshold(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	f := &fakeReaperDeps{active: []string{"h1"}, parts: map[string]int{"h1": 0}, reapOK: true}
	r := newTestReaper(f.deps(), 15*time.Minute, &now)
	ctx := context.Background()

	r.sweep(ctx) // t0: seed idle clock, no reap
	if len(f.reapedHosts()) != 0 {
		t.Fatal("must not reap on the first idle observation")
	}
	now = now.Add(14 * time.Minute)
	r.sweep(ctx) // still under threshold
	if len(f.reapedHosts()) != 0 {
		t.Fatal("must not reap before IdleAfter elapses")
	}
	now = now.Add(2 * time.Minute) // total 16m > 15m
	r.sweep(ctx)
	if got := f.reapedHosts(); len(got) != 1 || got[0] != "h1" {
		t.Fatalf("reaped = %v, want [h1] after the threshold", got)
	}
}

// TestReaper_ResetsOnReconnect: a participant connecting resets the idle clock, so the threshold
// is measured from the LAST time the session went idle, not the first.
func TestReaper_ResetsOnReconnect(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	f := &fakeReaperDeps{active: []string{"h1"}, parts: map[string]int{"h1": 0}, reapOK: true}
	r := newTestReaper(f.deps(), 15*time.Minute, &now)
	ctx := context.Background()

	r.sweep(ctx) // t0 idle: clock starts
	now = now.Add(10 * time.Minute)
	f.mu.Lock()
	f.parts["h1"] = 1 // a participant reconnected
	f.mu.Unlock()
	r.sweep(ctx) // resets the clock
	now = now.Add(10 * time.Minute)
	f.mu.Lock()
	f.parts["h1"] = 0 // idle again
	f.mu.Unlock()
	r.sweep(ctx) // re-seeds the clock (now)
	now = now.Add(14 * time.Minute)
	r.sweep(ctx) // 14m since the re-seed < 15m
	if len(f.reapedHosts()) != 0 {
		t.Fatalf("reconnect must reset the idle clock; reaped too early: %v", f.reapedHosts())
	}
	now = now.Add(2 * time.Minute)
	r.sweep(ctx)
	if got := f.reapedHosts(); len(got) != 1 {
		t.Fatalf("should reap 15m after the LAST idle transition, got %v", got)
	}
}

// TestReaper_AbortedReapReTracks: when Reap returns reaped=false (a participant won the
// poll→reap race), the host is dropped from tracking and re-tracked from scratch — so it isn't
// reaped again until idle for another full window.
func TestReaper_AbortedReapReTracks(t *testing.T) {
	now := time.Unix(3_000_000, 0)
	f := &fakeReaperDeps{active: []string{"h1"}, parts: map[string]int{"h1": 0}, reapOK: false} // race: never actually reaps
	r := newTestReaper(f.deps(), 15*time.Minute, &now)
	ctx := context.Background()

	r.sweep(ctx)
	now = now.Add(16 * time.Minute)
	r.sweep(ctx) // crosses threshold → calls Reap, which reports reaped=false
	if _, tracked := r.idleSince["h1"]; tracked {
		t.Fatal("an aborted reap must clear tracking (re-track from scratch next sweep)")
	}
	// Next sweep re-seeds; it must not immediately reap again at the same instant.
	r.sweep(ctx)
	if _, tracked := r.idleSince["h1"]; !tracked {
		t.Fatal("after an aborted reap, the host should be re-tracked on the next idle sweep")
	}
}

// TestReaper_DropsInactiveHosts: a host that is no longer in the active set is dropped from
// tracking, so the idleSince map doesn't leak.
func TestReaper_DropsInactiveHosts(t *testing.T) {
	now := time.Unix(4_000_000, 0)
	f := &fakeReaperDeps{active: []string{"h1"}, parts: map[string]int{"h1": 0}, reapOK: true}
	r := newTestReaper(f.deps(), 15*time.Minute, &now)
	ctx := context.Background()

	r.sweep(ctx)
	if _, tracked := r.idleSince["h1"]; !tracked {
		t.Fatal("h1 should be tracked while idle + active")
	}
	f.mu.Lock()
	f.active = nil // h1's session ended elsewhere
	f.mu.Unlock()
	r.sweep(ctx)
	if _, tracked := r.idleSince["h1"]; tracked {
		t.Fatal("a host no longer active must be dropped from tracking")
	}
}

// TestReaper_ListErrorIsNonFatal: an ActiveHosts error skips the sweep without panicking or
// clobbering existing tracking.
func TestReaper_ListErrorIsNonFatal(t *testing.T) {
	now := time.Unix(5_000_000, 0)
	f := &fakeReaperDeps{activeErr: errors.New("db down")}
	r := newTestReaper(f.deps(), 15*time.Minute, &now)
	r.sweep(context.Background()) // must not panic
}

// TestReaper_RunStops: Run returns promptly when the context is cancelled.
func TestReaper_RunStops(t *testing.T) {
	f := &fakeReaperDeps{}
	r := NewReaper(f.deps(), ReaperConfig{Interval: 5 * time.Millisecond, IdleAfter: time.Hour}, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
