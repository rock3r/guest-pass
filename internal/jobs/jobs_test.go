package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakePurgeStore records every PurgeGuestPII call so a test can assert the policy numbers
// the purger passes, and counts invocations for the ticker-loop test.
type fakePurgeStore struct {
	mu        sync.Mutex
	calls     []purgeCall
	anonCalls []anonCall
	ret       int64
	err       error
	anonRet   int64
	anonErr   error
	signal    chan struct{} // non-nil: receives once per PurgeGuestPII call (ticker-loop test)
}

type purgeCall struct{ now, retention, grace int64 }
type anonCall struct{ now, retention int64 }

func (f *fakePurgeStore) PurgeGuestPII(_ context.Context, now, retentionSecs, graceSecs int64) (int64, error) {
	f.mu.Lock()
	f.calls = append(f.calls, purgeCall{now, retentionSecs, graceSecs})
	f.mu.Unlock()
	if f.signal != nil {
		f.signal <- struct{}{}
	}
	return f.ret, f.err
}

// AnonymizeExpiredReports records its call (no signal — the ticker-loop test counts PurgeGuestPII).
func (f *fakePurgeStore) AnonymizeExpiredReports(_ context.Context, now, retentionSecs int64) (int64, error) {
	f.mu.Lock()
	f.anonCalls = append(f.anonCalls, anonCall{now, retentionSecs})
	f.mu.Unlock()
	return f.anonRet, f.anonErr
}

func (f *fakePurgeStore) anonCallsCopy() []anonCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]anonCall(nil), f.anonCalls...)
}

func (f *fakePurgeStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func discardLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// TestPurger_SweepTriggersOneIntervalEarly asserts the eligibility age handed to the store is
// retention MINUS the sweep interval, so the user-facing "deleted within RETENTION of stream
// end" promise holds (D-37): a pass that becomes eligible right after a tick waits up to one
// interval for the next sweep, so triggering one interval early bounds the worst case at
// (retention - interval) + interval = retention. Grace + clock pass through unchanged.
func TestPurger_SweepTriggersOneIntervalEarly(t *testing.T) {
	fixed := time.Unix(2_000_000_000, 0)
	store := &fakePurgeStore{ret: 3}
	p := NewPurger(store, PurgeConfig{
		Interval:  time.Hour,
		Retention: 24 * time.Hour,
		Grace:     30 * time.Minute,
	}, discardLogger())
	p.now = func() time.Time { return fixed }

	n, err := p.sweep(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("sweep = %d / %v, want 3 / nil", n, err)
	}
	if store.callCount() != 1 {
		t.Fatalf("store called %d times, want 1", store.callCount())
	}
	got := store.calls[0]
	if got.now != fixed.Unix() {
		t.Errorf("now = %d, want %d", got.now, fixed.Unix())
	}
	wantAge := int64((24*time.Hour - time.Hour).Seconds()) // retention - interval
	if got.retention != wantAge {
		t.Errorf("eligibility age = %d, want %d (retention - interval)", got.retention, wantAge)
	}
	if got.grace != int64((30 * time.Minute).Seconds()) {
		t.Errorf("grace = %d, want %d", got.grace, int64((30 * time.Minute).Seconds()))
	}
}

// TestPurger_SweepClampsAgeToZero: if the interval is >= retention (a misconfiguration where
// the cadence alone can't meet the SLA), the eligibility age clamps to 0 — purge anything past
// stream end on the next sweep — rather than going negative (which would clear PII still inside
// its window).
func TestPurger_SweepClampsAgeToZero(t *testing.T) {
	store := &fakePurgeStore{}
	p := NewPurger(store, PurgeConfig{Interval: 48 * time.Hour, Retention: 24 * time.Hour, Grace: 0}, discardLogger())
	if _, err := p.sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := store.calls[0].retention; got != 0 {
		t.Errorf("eligibility age = %d, want 0 (clamped)", got)
	}
}

// TestPurger_SweepSurfacesError: a store failure is returned (so Run can log it) and does not panic.
func TestPurger_SweepSurfacesError(t *testing.T) {
	store := &fakePurgeStore{err: errors.New("boom")}
	p := NewPurger(store, PurgeConfig{Interval: time.Hour, Retention: time.Hour, Grace: 0}, discardLogger())
	if _, err := p.sweep(context.Background()); err == nil {
		t.Fatal("sweep should surface the store error")
	}
}

// TestPurger_RunSweepsImmediatelyThenTicks: Run sweeps once right away, then on every
// interval, and stops cleanly when the context is cancelled.
func TestPurger_RunSweepsImmediatelyThenTicks(t *testing.T) {
	store := &fakePurgeStore{signal: make(chan struct{}, 16)}
	p := NewPurger(store, PurgeConfig{Interval: 5 * time.Millisecond, Retention: time.Hour, Grace: 0}, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	// Expect the immediate sweep plus at least two ticked sweeps.
	for i := 0; i < 3; i++ {
		select {
		case <-store.signal:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d sweeps within timeout, want >= 3", i)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

// TestPurger_AnonymizesReports asserts each sweep also anonymizes expired reports, using the FULL
// report-retention age (no sub-interval early-trigger — anonymization has no SLA), and that a purge
// error does not skip the anonymize sweep (the two are independent). (D-42/D-37)
func TestPurger_AnonymizesReports(t *testing.T) {
	fixed := time.Unix(2_000_000_000, 0)
	store := &fakePurgeStore{err: errors.New("purge boom"), anonRet: 4}
	p := NewPurger(store, PurgeConfig{
		Interval:        time.Hour,
		Retention:       24 * time.Hour,
		ReportRetention: 30 * 24 * time.Hour,
	}, discardLogger())
	p.now = func() time.Time { return fixed }

	p.runOnce(context.Background())

	calls := store.anonCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("anonymize calls = %d, want 1 (runs even though the purge errored)", len(calls))
	}
	if calls[0].now != fixed.Unix() {
		t.Fatalf("anonymize now = %d, want %d", calls[0].now, fixed.Unix())
	}
	if want := int64((30 * 24 * time.Hour).Seconds()); calls[0].retention != want {
		t.Fatalf("anonymize retention = %d, want the full report retention %d (no interval subtraction)", calls[0].retention, want)
	}
}
