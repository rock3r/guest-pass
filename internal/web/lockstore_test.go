package web

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// The store↔signaling adapter maps a host-applied lock (empty applier) to a NULL
// applier_pass_id and back, and a cohost-applied lock to the cohost's pass id (AD-22).
func TestLockPersistenceAdapter(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "lp.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	host, _ := st.CreateHost(ctx, store.CreateHostParams{GoogleSub: "lp", Email: "lp@example.com", Name: "H", Status: store.HostActive})
	stream, _ := st.CreateStream(ctx, store.CreateStreamParams{HostID: host.ID, Title: "S"})
	target, _ := st.CreatePass(ctx, store.CreatePassParams{StreamID: stream.ID, TokenHash: "lp-t"})
	cohost, _ := st.CreatePass(ctx, store.CreatePassParams{StreamID: stream.ID, TokenHash: "lp-c", Role: store.RoleCohost})

	a := NewLockPersistence(st)
	if err := a.SaveLock(ctx, signaling.PersistedLock{Target: target.ID, Modality: "mic", ApplierRankFloor: "host", Applier: ""}); err != nil {
		t.Fatalf("SaveLock host: %v", err)
	}
	if err := a.SaveLock(ctx, signaling.PersistedLock{Target: target.ID, Modality: "cam", ApplierRankFloor: "cohost", Applier: cohost.ID}); err != nil {
		t.Fatalf("SaveLock cohost: %v", err)
	}

	locks, err := a.LoadLocks(ctx, host.ID)
	if err != nil {
		t.Fatalf("LoadLocks: %v", err)
	}
	if len(locks) != 2 {
		t.Fatalf("want 2 locks, got %d: %+v", len(locks), locks)
	}
	byMod := map[string]signaling.PersistedLock{}
	for _, l := range locks {
		byMod[l.Modality] = l
	}
	if m := byMod["mic"]; m.Applier != "" || m.ApplierRankFloor != "host" || m.Target != target.ID {
		t.Fatalf("a host-applied lock must map to an empty applier, got %+v", m)
	}
	if c := byMod["cam"]; c.Applier != cohost.ID || c.ApplierRankFloor != "cohost" {
		t.Fatalf("a cohost-applied lock must map to the cohost pass id, got %+v", c)
	}

	if err := a.DeleteLock(ctx, target.ID, "mic"); err != nil {
		t.Fatalf("DeleteLock: %v", err)
	}
	if locks, _ = a.LoadLocks(ctx, host.ID); len(locks) != 1 {
		t.Fatalf("after delete want 1 lock, got %d", len(locks))
	}
}
