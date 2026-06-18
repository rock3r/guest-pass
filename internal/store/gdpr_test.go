package store

import (
	"context"
	"errors"
	"testing"
)

// SetHostName is the GDPR rectification write (PATCH /api/me, AC-4): it updates the host's
// display name and errors on an unknown id.
func TestHostRepo_SetHostName(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-amend")

	if err := st.SetHostName(ctx, h.ID, "Renamed Host"); err != nil {
		t.Fatalf("SetHostName: %v", err)
	}
	if got, _ := st.GetHost(ctx, h.ID); got.Name != "Renamed Host" {
		t.Fatalf("name = %q, want Renamed Host", got.Name)
	}
	if err := st.SetHostName(ctx, "nope", "X"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetHostName(missing) = %v, want ErrNotFound", err)
	}
}

// DeleteHost is the GDPR erasure (DELETE /api/me, AC-5): it removes the host AND, via ON DELETE
// CASCADE, ALL of the host's data — streams, passes, slots, sessions, host_source_tokens, peers,
// pass_locks — while leaving OTHER hosts' data completely untouched (host-scoped wipe, D-M5-3).
func TestHostRepo_DeleteHostCascades(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	victim := seedHost(t, st, "host-delete")
	other := seedHost(t, st, "host-keep")

	// Victim's data across every table that references it.
	vs, _ := st.CreateStream(ctx, CreateStreamParams{HostID: victim.ID, Title: "V"})
	vslot, _ := st.CreateSlot(ctx, CreateSlotParams{HostID: victim.ID, Kind: SlotCam, Idx: i64(1), SourceTokenHash: "v-src"})
	vname, vemail := "Guest", "g@example.com"
	vpass, _ := st.CreatePass(ctx, CreatePassParams{StreamID: vs.ID, Name: &vname, Email: &vemail, TokenHash: "v-tok"})
	_ = st.AssignPassSlot(ctx, vpass.ID, vslot.ID)
	vsess, _ := st.StartSession(ctx, vs.ID, victim.ID)
	if _, err := st.writer.ExecContext(ctx,
		"INSERT INTO peers (id, session_id, role, connected_at) VALUES (?, ?, ?, ?)", "v-peer", vsess.ID, RoleGuest, int64(0)); err != nil {
		t.Fatalf("insert peer: %v", err)
	}
	if _, err := st.writer.ExecContext(ctx,
		"INSERT INTO host_source_tokens (id, stream_id, role, token_hash) VALUES (?, ?, ?, ?)", "v-hst", vs.ID, "host", "v-hst-tok"); err != nil {
		t.Fatalf("insert host_source_token: %v", err)
	}
	if _, err := st.writer.ExecContext(ctx,
		"INSERT INTO pass_locks (pass_id, modality, applier_rank_floor, created_at) VALUES (?, ?, ?, ?)", vpass.ID, "mic", "host", int64(0)); err != nil {
		t.Fatalf("insert pass_lock: %v", err)
	}

	// Another host's data, which must survive the wipe.
	os, _ := st.CreateStream(ctx, CreateStreamParams{HostID: other.ID, Title: "O"})
	oname := "Keep"
	opass, _ := st.CreatePass(ctx, CreatePassParams{StreamID: os.ID, Name: &oname, TokenHash: "o-tok"})

	if err := st.DeleteHost(ctx, victim.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}

	// Victim is gone everywhere (cascade).
	if _, err := st.GetHost(ctx, victim.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("victim host still present: %v", err)
	}
	count := func(table, where string, args ...any) int {
		var c int
		if err := st.reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" "+where, args...).Scan(&c); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return c
	}
	if n := count("streams", "WHERE host_id = ?", victim.ID); n != 0 {
		t.Fatalf("victim streams not cascaded: %d", n)
	}
	if n := count("slots", "WHERE host_id = ?", victim.ID); n != 0 {
		t.Fatalf("victim slots not cascaded: %d", n)
	}
	if n := count("passes", "WHERE id = ?", vpass.ID); n != 0 {
		t.Fatalf("victim pass not cascaded: %d", n)
	}
	if n := count("sessions", "WHERE id = ?", vsess.ID); n != 0 {
		t.Fatalf("victim session not cascaded: %d", n)
	}
	if n := count("peers", "WHERE id = ?", "v-peer"); n != 0 {
		t.Fatalf("victim peer not cascaded: %d", n)
	}
	if n := count("host_source_tokens", "WHERE id = ?", "v-hst"); n != 0 {
		t.Fatalf("victim host_source_token not cascaded: %d", n)
	}
	if n := count("pass_locks", "WHERE pass_id = ?", vpass.ID); n != 0 {
		t.Fatalf("victim pass_lock not cascaded: %d", n)
	}

	// The other host's data is untouched.
	if _, err := st.GetHost(ctx, other.ID); err != nil {
		t.Fatalf("other host wrongly affected: %v", err)
	}
	if n := count("streams", "WHERE id = ?", os.ID); n != 1 {
		t.Fatalf("other host's stream wrongly deleted: %d", n)
	}
	if n := count("passes", "WHERE id = ?", opass.ID); n != 1 {
		t.Fatalf("other host's pass wrongly deleted: %d", n)
	}

	if err := st.DeleteHost(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteHost(missing) = %v, want ErrNotFound", err)
	}
}

// ListPassesByHost returns the host's invited-guest passes across all their streams (the export
// surface, AC-3), scoped to the host — never another host's (EN-8).
func TestPassRepo_ListPassesByHost(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	h := seedHost(t, st, "host-export")
	other := seedHost(t, st, "host-export-other")
	s1, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "S1"})
	s2, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "S2"})
	os, _ := st.CreateStream(ctx, CreateStreamParams{HostID: other.ID, Title: "O"})

	n1 := "A"
	_, _ = st.CreatePass(ctx, CreatePassParams{StreamID: s1.ID, Name: &n1, TokenHash: "p1"})
	_, _ = st.CreatePass(ctx, CreatePassParams{StreamID: s2.ID, TokenHash: "p2"})
	_, _ = st.CreatePass(ctx, CreatePassParams{StreamID: os.ID, TokenHash: "op"}) // other host's — must not appear

	got, err := st.ListPassesByHost(ctx, h.ID)
	if err != nil {
		t.Fatalf("ListPassesByHost: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d passes, want 2 (host-scoped)", len(got))
	}
	for _, p := range got {
		if p.StreamID == os.ID {
			t.Fatal("another host's pass leaked into the export")
		}
	}
}
