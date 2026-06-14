package signaling

import "context"

// LockPersistence backs the suppression-lock state machine with durable storage (AD-22), so a
// force-muted guest stays muted across a server restart. The Room loads a session's locks on
// (re)spawn and writes each force/release through. *store.Store is adapted to this interface
// where the Hub is constructed, keeping the signaling package free of the DB schema. A nil
// LockPersistence disables persistence (used by the pure reducer/transport tests and dev).
type LockPersistence interface {
	// LoadLocks returns every persisted suppression lock for a session (the host id), to
	// re-apply on room spawn.
	LoadLocks(ctx context.Context, session string) ([]PersistedLock, error)
	// SaveLock upserts one lock — one row per (target, modality).
	SaveLock(ctx context.Context, l PersistedLock) error
	// DeleteLock removes a lock on release.
	DeleteLock(ctx context.Context, target, modality string) error
}

// PersistedLock is the durable shape of a suppression lock. Applier is the applier's peer id,
// or "" when the HOST applied it (the host has no pass). ApplierRankFloor is "host" | "cohost".
type PersistedLock struct {
	Target           string
	Modality         string
	ApplierRankFloor string
	Applier          string
}

// toSeeded maps loaded persistence rows into the reducer's seed shape.
func toSeeded(locks []PersistedLock) []seededLock {
	out := make([]seededLock, 0, len(locks))
	for _, l := range locks {
		out = append(out, seededLock{
			Target:   PeerID(l.Target),
			Modality: l.Modality,
			Floor:    floorRank(l.ApplierRankFloor),
			Applier:  PeerID(l.Applier),
		})
	}
	return out
}
