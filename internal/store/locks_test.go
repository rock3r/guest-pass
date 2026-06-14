package store

import (
	"context"
	"testing"
)

func strptr(s string) *string { return &s }

// SaveLock + LocksForHost + DeleteLock round-trip, with the host-applied (NULL applier) vs
// cohost-applied distinction and the upsert-in-place semantics (AD-22 / D-13).
func TestLockRepo_SaveLoadDelete(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	h := seedHost(t, st, "host-lock")
	stream, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "Show"})
	target, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "tok-target"})
	cohost, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "tok-cohost", Role: RoleCohost})

	if err := st.SaveLock(ctx, PassLock{PassID: target.ID, Modality: "mic", ApplierRankFloor: "host", ApplierPassID: nil, CreatedAt: 100}); err != nil {
		t.Fatalf("SaveLock host: %v", err)
	}
	if err := st.SaveLock(ctx, PassLock{PassID: target.ID, Modality: "cam", ApplierRankFloor: "cohost", ApplierPassID: strptr(cohost.ID), CreatedAt: 101}); err != nil {
		t.Fatalf("SaveLock cohost: %v", err)
	}

	locks, err := st.LocksForHost(ctx, h.ID)
	if err != nil {
		t.Fatalf("LocksForHost: %v", err)
	}
	if len(locks) != 2 {
		t.Fatalf("want 2 locks, got %d: %+v", len(locks), locks)
	}
	byMod := map[string]PassLock{}
	for _, l := range locks {
		byMod[l.Modality] = l
	}
	if m := byMod["mic"]; m.ApplierRankFloor != "host" || m.ApplierPassID != nil {
		t.Fatalf("mic lock = %+v, want host-applied with NULL applier", m)
	}
	if c := byMod["cam"]; c.ApplierRankFloor != "cohost" || c.ApplierPassID == nil || *c.ApplierPassID != cohost.ID {
		t.Fatalf("cam lock = %+v, want cohost-applied by %s", c, cohost.ID)
	}

	// Upsert: a higher-rank re-force overwrites in place — still one row per (pass, modality).
	if err := st.SaveLock(ctx, PassLock{PassID: target.ID, Modality: "cam", ApplierRankFloor: "host", ApplierPassID: nil, CreatedAt: 102}); err != nil {
		t.Fatalf("SaveLock upsert: %v", err)
	}
	locks, _ = st.LocksForHost(ctx, h.ID)
	if len(locks) != 2 {
		t.Fatalf("an upsert must not add a row, got %d", len(locks))
	}

	// Release deletes the row; deleting again is idempotent (no error).
	if err := st.DeleteLock(ctx, target.ID, "mic"); err != nil {
		t.Fatalf("DeleteLock: %v", err)
	}
	if err := st.DeleteLock(ctx, target.ID, "mic"); err != nil {
		t.Fatalf("DeleteLock idempotent: %v", err)
	}
	if locks, _ = st.LocksForHost(ctx, h.ID); len(locks) != 1 {
		t.Fatalf("after delete want 1 lock, got %d", len(locks))
	}
}

// LocksForHost is host-scoped: one host never sees another host's locks (AD-22 join).
func TestLockRepo_HostScoped(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	h1 := seedHost(t, st, "h1-lock")
	h2 := seedHost(t, st, "h2-lock")
	s1, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h1.ID, Title: "S1"})
	s2, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h2.ID, Title: "S2"})
	p1, _ := st.CreatePass(ctx, CreatePassParams{StreamID: s1.ID, TokenHash: "p1"})
	p2, _ := st.CreatePass(ctx, CreatePassParams{StreamID: s2.ID, TokenHash: "p2"})
	_ = st.SaveLock(ctx, PassLock{PassID: p1.ID, Modality: "mic", ApplierRankFloor: "host", CreatedAt: 1})
	_ = st.SaveLock(ctx, PassLock{PassID: p2.ID, Modality: "mic", ApplierRankFloor: "host", CreatedAt: 1})

	l1, _ := st.LocksForHost(ctx, h1.ID)
	if len(l1) != 1 || l1[0].PassID != p1.ID {
		t.Fatalf("host h1 should see only its own lock, got %+v", l1)
	}
}

// Schema durability (AD-22): deleting the TARGET pass CASCADE-deletes its locks (the 24h
// purge path); deleting a COHOST APPLIER's pass nulls applier_pass_id but KEEPS the lock (the
// rank floor preserves authority — host can still release; no orphaned moderation).
func TestLockRepo_CascadeAndApplierSetNull(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	h := seedHost(t, st, "h-cascade")
	stream, _ := st.CreateStream(ctx, CreateStreamParams{HostID: h.ID, Title: "S"})
	target, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "tok-t"})
	cohost, _ := st.CreatePass(ctx, CreatePassParams{StreamID: stream.ID, TokenHash: "tok-c", Role: RoleCohost})
	_ = st.SaveLock(ctx, PassLock{PassID: target.ID, Modality: "mic", ApplierRankFloor: "cohost", ApplierPassID: strptr(cohost.ID), CreatedAt: 1})

	// Cohost applier's pass deleted → applier_pass_id SET NULL, lock + floor preserved.
	if err := st.DeletePass(ctx, cohost.ID); err != nil {
		t.Fatalf("DeletePass cohost: %v", err)
	}
	locks, _ := st.LocksForHost(ctx, h.ID)
	if len(locks) != 1 || locks[0].ApplierPassID != nil || locks[0].ApplierRankFloor != "cohost" {
		t.Fatalf("a deleted cohost applier must null applier_pass_id but keep the lock + floor, got %+v", locks)
	}

	// Target pass deleted → its locks CASCADE away.
	if err := st.DeletePass(ctx, target.ID); err != nil {
		t.Fatalf("DeletePass target: %v", err)
	}
	if locks, _ = st.LocksForHost(ctx, h.ID); len(locks) != 0 {
		t.Fatalf("deleting the target pass must cascade-delete its locks, got %+v", locks)
	}
}
