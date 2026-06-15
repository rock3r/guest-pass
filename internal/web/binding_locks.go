package web

import "sync"

// bindingLocks serializes a host's slot-binding operations — the greenroom PUT
// /api/passes/{id}/slot AND the /ws join-replay — so each operation's persistent write and its
// live room command (Room.Rebind/RebindOrVacate/VacateOccupant) are issued ATOMICALLY per host
// (D-20). The room goroutine applies its commands in enqueue order, so holding this lock across
// "(read/write passes.slot_id) → (enqueue the room command)" guarantees the room sees them in
// DB-commit order. Without it, two concurrent operations on the same pass could commit the DB in
// one order but enqueue their room commands in the other, leaving the live slot diverged from the
// persisted binding (e.g. an unassign and a bind racing, or a join-replay racing a host move).
//
// One mutex per host id; the map is bounded by the distinct host count (small for a self-host),
// and each entry is a bare *sync.Mutex — a negligible, never-shrinking footprint we accept over
// the complexity of refcounted eviction.
type bindingLocks struct {
	mu   sync.Mutex
	keys map[string]*sync.Mutex
}

func newBindingLocks() *bindingLocks {
	return &bindingLocks{keys: make(map[string]*sync.Mutex)}
}

// lock acquires the per-host binding mutex and returns its unlock function.
func (b *bindingLocks) lock(host string) func() {
	b.mu.Lock()
	m := b.keys[host]
	if m == nil {
		m = &sync.Mutex{}
		b.keys[host] = m
	}
	b.mu.Unlock()
	m.Lock()
	return m.Unlock
}
