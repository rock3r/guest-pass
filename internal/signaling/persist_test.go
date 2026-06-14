package signaling

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeLockStore is an in-memory LockPersistence for the Room persistence tests (AD-22). It is
// mutex-guarded because the Room writes from its goroutine while the test reads from its own.
type fakeLockStore struct {
	mu      sync.Mutex
	locks   map[string]PersistedLock // key: target|modality
	loadErr error
}

func newFakeLockStore() *fakeLockStore { return &fakeLockStore{locks: map[string]PersistedLock{}} }

func lockKey(target, modality string) string { return target + "|" + modality }

func (f *fakeLockStore) LoadLocks(_ context.Context, _ string) ([]PersistedLock, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	out := make([]PersistedLock, 0, len(f.locks))
	for _, l := range f.locks {
		out = append(out, l)
	}
	return out, nil
}

func (f *fakeLockStore) SaveLock(_ context.Context, l PersistedLock) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.locks[lockKey(l.Target, l.Modality)] = l
	return nil
}

func (f *fakeLockStore) DeleteLock(_ context.Context, target, modality string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.locks, lockKey(target, modality))
	return nil
}

func (f *fakeLockStore) get(target, modality string) (PersistedLock, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.locks[lockKey(target, modality)]
	return l, ok
}

// eventually polls cond until true or a deadline, for asserting an async persist landed.
func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

func entryInFrame(f Frame, id string) *RosterEntry {
	for i := range f.Peers {
		if f.Peers[i].ID == id {
			return &f.Peers[i]
		}
	}
	return nil
}

func frameHasLock(f Frame, id, kind string) bool {
	e := entryInFrame(f, id)
	if e == nil {
		return false
	}
	for _, l := range e.Locks {
		if l.Kind == kind {
			return true
		}
	}
	return false
}

// T-4 (pure): seedLocks re-applies persisted locks into a fresh room state, with the
// host-applied (empty applier → "host") vs cohost-applied distinction, and a seeded-locked
// guest joining stays suppressed and cannot self-enable (AD-22).
func TestSeedLocksReapplies(t *testing.T) {
	s := newRoomState()
	s.seedLocks([]seededLock{
		{Target: "g1", Modality: "mic", Floor: rankHost, Applier: ""},     // host-applied
		{Target: "g1", Modality: "cam", Floor: rankCohost, Applier: "co"}, // cohost-applied
	})
	if lk := s.lockOn("g1", "mic"); lk == nil || lk.floor != rankHost || lk.applier != "host" {
		t.Fatalf("host-applied seed → applier host, got %+v", lk)
	}
	if lk := s.lockOn("g1", "cam"); lk == nil || lk.floor != rankCohost || lk.applier != "co" {
		t.Fatalf("cohost-applied seed → applier co, got %+v", lk)
	}

	s.join("host", "host", "")
	s.join("g1", "guest", "")
	s.applyState("g1", bptr(true), bptr(true), nil, nil) // try cam + mic on
	if s.peers["g1"].mic || s.peers["g1"].cam {
		t.Fatalf("a seeded force-muted/hidden guest must not self-enable, got mic=%v cam=%v", s.peers["g1"].mic, s.peers["g1"].cam)
	}
}

// T-4 (INT): a force persists a lock row; a release deletes it (AD-22). The host-applied lock
// stores a NULL applier (empty string here).
func TestRoomPersistsForceAndRelease(t *testing.T) {
	fs := newFakeLockStore()
	r := newRoom("h", fs, discardLogger())
	go r.run()
	defer r.Close()

	hostOut := make(chan Frame, 32)
	if !r.Join("host", "host", "", "", hostOut) {
		t.Fatal("host join refused")
	}
	g1Out := make(chan Frame, 32)
	if !r.Join("g1", "guest", "", "", g1Out) {
		t.Fatal("g1 join refused")
	}
	recvFrameOfType(t, hostOut, "peer-joined") // sync: g1 is in the room

	r.Force("host", "g1", "mic")
	eventually(t, func() bool { _, ok := fs.get("g1", "mic"); return ok }, "a force must persist a lock row")
	if l, _ := fs.get("g1", "mic"); l.ApplierRankFloor != "host" || l.Applier != "" {
		t.Fatalf("persisted lock = %+v, want host-applied with empty (NULL) applier", l)
	}

	r.Release("host", "g1", "mic")
	eventually(t, func() bool { _, ok := fs.get("g1", "mic"); return !ok }, "a release must delete the lock row")
}

// T-4 (INT, the keystone): a force-muted guest stays muted across a simulated restart — a fresh
// room with the same store re-applies the persisted lock on spawn, so the guest's reconnect
// roster shows the lock and it cannot self-unmute (AD-22).
func TestRoomReappliesLocksOnRespawn(t *testing.T) {
	fs := newFakeLockStore()
	// A prior run persisted a host-applied mic lock on g1.
	_ = fs.SaveLock(context.Background(), PersistedLock{Target: "g1", Modality: "mic", ApplierRankFloor: "host", Applier: ""})

	// Restart: a fresh room with the same store loads the lock on spawn, before any join.
	r := newRoom("h", fs, discardLogger())
	go r.run()
	defer r.Close()

	g1Out := make(chan Frame, 32)
	if !r.Join("g1", "guest", "", "", g1Out) {
		t.Fatal("g1 join refused")
	}
	roster := recvFrameOfType(t, g1Out, "roster") // the reconnect roster
	if !frameHasLock(roster, "g1", "mic") {
		t.Fatalf("a respawned room must re-apply the persisted lock, got %+v", roster.Peers)
	}
	if e := entryInFrame(roster, "g1"); e == nil || e.Mic {
		t.Fatalf("the restored lock must keep the guest suppressed, got %+v", e)
	}

	// The guest cannot self-unmute after the restart: the rejection re-broadcasts an
	// authoritative roster that still shows mic suppressed.
	r.ApplyState("g1", nil, bptr(true), nil, nil)
	rejected := recvFrameOfType(t, g1Out, "roster")
	if e := entryInFrame(rejected, "g1"); e == nil || e.Mic {
		t.Fatalf("a restart-restored force-muted guest must not self-unmute, got %+v", e)
	}
}
