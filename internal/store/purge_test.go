package store

import (
	"context"
	"testing"
)

const (
	testRetention = int64(24 * 60 * 60) // 24h (D-37)
	testGrace     = int64(30 * 60)      // 30m scheduled-end grace (matches D-5 pass expiry)
)

// insertSession inserts a session row with an explicit ended_at so a test can place a
// stream's end an exact distance in the past (the repo's EndActiveSession always stamps
// "now"). A NULL ended_at + status=active models a live session.
func insertSession(t *testing.T, st *Store, id, streamID, hostID, status string, endedAt *int64) {
	t.Helper()
	_, err := st.writer.ExecContext(context.Background(),
		"INSERT INTO sessions (id, stream_id, host_id, started_at, ended_at, status) VALUES (?, ?, ?, ?, ?, ?)",
		id, streamID, hostID, int64(0), endedAt, status)
	if err != nil {
		t.Fatalf("insertSession: %v", err)
	}
}

// guestPass creates a pass carrying guest PII (name + email) so the purge has something to
// clear, and returns its id.
func guestPass(t *testing.T, st *Store, streamID, tokenHash string) string {
	t.Helper()
	name, email := "Guest", "guest@example.com"
	p, err := st.CreatePass(context.Background(), CreatePassParams{
		StreamID: streamID, Name: &name, Email: &email, TokenHash: tokenHash,
	})
	if err != nil {
		t.Fatalf("guestPass: %v", err)
	}
	return p.ID
}

// piiCleared reports whether a pass's name AND email are both NULL.
func piiCleared(t *testing.T, st *Store, passID string) bool {
	t.Helper()
	p, err := st.GetPass(context.Background(), passID)
	if err != nil {
		t.Fatalf("GetPass: %v", err)
	}
	return p.Name == nil && p.Email == nil
}

// TestPurgeGuestPII_EligibilityWindows covers the AC-1 retention rule: PII is cleared once a
// stream is 24h+ past its end (an ended session or, for a never-run stream, scheduled-end +
// grace), and never while the stream is live, recently ended, or has no end to key off.
func TestPurgeGuestPII_EligibilityWindows(t *testing.T) {
	ctx := context.Background()
	now := int64(2_000_000_000)
	cutoff := now - testRetention // a stream-end at or before this is purge-eligible

	type fixture struct {
		name        string
		wantCleared bool
		build       func(t *testing.T, st *Store, streamID, hostID string)
	}
	fixtures := []fixture{
		{
			name:        "ended >24h ago",
			wantCleared: true,
			build: func(t *testing.T, st *Store, streamID, hostID string) {
				ended := cutoff - 10
				insertSession(t, st, "sess-"+streamID, streamID, hostID, SessionEnded, &ended)
			},
		},
		{
			name:        "ended exactly at cutoff",
			wantCleared: true, // boundary is inclusive (<=)
			build: func(t *testing.T, st *Store, streamID, hostID string) {
				ended := cutoff
				insertSession(t, st, "sess-"+streamID, streamID, hostID, SessionEnded, &ended)
			},
		},
		{
			name:        "ended just under 24h ago",
			wantCleared: false,
			build: func(t *testing.T, st *Store, streamID, hostID string) {
				ended := cutoff + 1
				insertSession(t, st, "sess-"+streamID, streamID, hostID, SessionEnded, &ended)
			},
		},
		{
			name:        "live now (active session)",
			wantCleared: false,
			build: func(t *testing.T, st *Store, streamID, hostID string) {
				insertSession(t, st, "sess-"+streamID, streamID, hostID, SessionActive, nil)
			},
		},
		{
			name:        "old ended session but live again now",
			wantCleared: false, // an active session on the same stream protects it even with an old ended one
			build: func(t *testing.T, st *Store, streamID, hostID string) {
				ended := cutoff - 1000
				insertSession(t, st, "sess-old-"+streamID, streamID, hostID, SessionEnded, &ended)
				insertSession(t, st, "sess-live-"+streamID, streamID, hostID, SessionActive, nil)
			},
		},
		{
			name:        "never ran, scheduled-end+grace >24h ago",
			wantCleared: true,
			build: func(t *testing.T, st *Store, streamID, hostID string) {
				// scheduled-end (scheduled_at + duration*60) + grace must be <= cutoff.
			},
		},
		{
			name:        "never ran, scheduled recently",
			wantCleared: false,
			build: func(t *testing.T, st *Store, streamID, hostID string) {
				// scheduled near now → scheduled-end+grace is well after cutoff.
			},
		},
		{
			name:        "draft, never scheduled, never ran",
			wantCleared: false, // no stream-end to key the retention window off
			build:       func(t *testing.T, st *Store, streamID, hostID string) {},
		},
	}

	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			st := openTestStore(t)
			h := seedHost(t, st, "h-"+f.name)

			var sched, dur *int64
			switch f.name {
			case "never ran, scheduled-end+grace >24h ago":
				// scheduled_at + dur*60 + grace = cutoff - 100  ⇒ eligible
				d := int64(60)
				s := cutoff - d*60 - testGrace - 100
				sched, dur = &s, &d
			case "never ran, scheduled recently":
				d := int64(60)
				s := now - 100
				sched, dur = &s, &d
			}
			stream, err := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "S", ScheduledAt: sched, DurationMin: dur})
			if err != nil {
				t.Fatalf("CreateStream: %v", err)
			}
			passID := guestPass(t, st, stream.ID, "tok-"+f.name)
			f.build(t, st, stream.ID, h.ID)

			n, err := st.PurgeGuestPII(ctx, now, testRetention, testGrace)
			if err != nil {
				t.Fatalf("PurgeGuestPII: %v", err)
			}
			cleared := piiCleared(t, st, passID)
			if cleared != f.wantCleared {
				t.Fatalf("cleared = %v, want %v (rows affected: %d)", cleared, f.wantCleared, n)
			}
			wantN := int64(0)
			if f.wantCleared {
				wantN = 1
			}
			if n != wantN {
				t.Fatalf("rows affected = %d, want %d", n, wantN)
			}
		})
	}
}

// TestPurgeGuestPII_Idempotent re-runs the purge: the second pass clears nothing (already
// NULL is never re-matched), so the job is safe to run on every tick.
func TestPurgeGuestPII_Idempotent(t *testing.T) {
	ctx := context.Background()
	now := int64(2_000_000_000)
	st := openTestStore(t)
	h := seedHost(t, st, "h-idem")
	stream, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "S"})
	passID := guestPass(t, st, stream.ID, "tok-idem")
	ended := now - testRetention - 10
	insertSession(t, st, "sess-idem", stream.ID, h.ID, SessionEnded, &ended)

	n1, err := st.PurgeGuestPII(ctx, now, testRetention, testGrace)
	if err != nil || n1 != 1 {
		t.Fatalf("first purge = %d / %v, want 1", n1, err)
	}
	if !piiCleared(t, st, passID) {
		t.Fatal("first purge did not clear PII")
	}
	n2, err := st.PurgeGuestPII(ctx, now, testRetention, testGrace)
	if err != nil || n2 != 0 {
		t.Fatalf("second purge = %d / %v, want 0 (idempotent)", n2, err)
	}
}

// TestPurgeGuestPII_NoCollateral asserts the purge touches ONLY the eligible stream's
// passes.name/email — never another stream's PII, and never sessions/peers/streams rows
// (anonymous aggregates and operational rows are preserved, AC-1).
func TestPurgeGuestPII_NoCollateral(t *testing.T) {
	ctx := context.Background()
	now := int64(2_000_000_000)
	st := openTestStore(t)
	h := seedHost(t, st, "h-collat")
	other := seedHost(t, st, "h-collat-other")

	// Eligible stream (ended >24h ago) with a guest pass + a peer row.
	eligible, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "Eligible"})
	elPass := guestPass(t, st, eligible.ID, "tok-eligible")
	ended := now - testRetention - 10
	insertSession(t, st, "sess-eligible", eligible.ID, h.ID, SessionEnded, &ended)
	if _, err := st.writer.ExecContext(ctx,
		"INSERT INTO peers (id, session_id, role, connected_at) VALUES (?, ?, ?, ?)",
		"peer-1", "sess-eligible", RoleGuest, int64(0)); err != nil {
		t.Fatalf("insert peer: %v", err)
	}

	// Ineligible stream (another host, live now) whose PII must survive untouched.
	keep, _ := st.CreateStream(ctx, CreateStreamParams{HostID: other.ID, Title: "Keep"})
	keepPass := guestPass(t, st, keep.ID, "tok-keep")
	insertSession(t, st, "sess-keep", keep.ID, other.ID, SessionActive, nil)

	countRows := func(table string) int {
		var c int
		if err := st.reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&c); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		return c
	}
	beforeSessions, beforePeers, beforeStreams := countRows("sessions"), countRows("peers"), countRows("streams")

	n, err := st.PurgeGuestPII(ctx, now, testRetention, testGrace)
	if err != nil || n != 1 {
		t.Fatalf("purge = %d / %v, want exactly 1 cleared", n, err)
	}
	if !piiCleared(t, st, elPass) {
		t.Fatal("eligible pass PII not cleared")
	}
	if piiCleared(t, st, keepPass) {
		t.Fatal("ineligible (other host, live) pass PII was wrongly cleared")
	}
	if got := countRows("sessions"); got != beforeSessions {
		t.Fatalf("sessions row count changed %d → %d (purge touched sessions)", beforeSessions, got)
	}
	if got := countRows("peers"); got != beforePeers {
		t.Fatalf("peers row count changed %d → %d (purge touched peers)", beforePeers, got)
	}
	if got := countRows("streams"); got != beforeStreams {
		t.Fatalf("streams row count changed %d → %d (purge touched streams)", beforeStreams, got)
	}
}
