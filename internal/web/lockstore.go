package web

import (
	"context"
	"time"

	"github.com/rock3r/guest-pass/internal/signaling"
	"github.com/rock3r/guest-pass/internal/store"
)

// lockPersistence adapts *store.Store to signaling.LockPersistence (AD-22), mapping the
// signaling-level lock shape to and from the pass_locks rows so the signaling package stays
// free of the DB schema. The session is the host id; the target/applier are pass ids (an empty
// applier means the host applied the lock — stored as a NULL applier_pass_id).
type lockPersistence struct{ store *store.Store }

// NewLockPersistence wraps a store as the signaling Hub's suppression-lock backing (AD-22).
func NewLockPersistence(st *store.Store) signaling.LockPersistence {
	return &lockPersistence{store: st}
}

func (a *lockPersistence) LoadLocks(ctx context.Context, session string) ([]signaling.PersistedLock, error) {
	rows, err := a.store.LocksForHost(ctx, session)
	if err != nil {
		return nil, err
	}
	out := make([]signaling.PersistedLock, 0, len(rows))
	for _, r := range rows {
		applier := ""
		if r.ApplierPassID != nil {
			applier = *r.ApplierPassID
		}
		out = append(out, signaling.PersistedLock{
			Target: r.PassID, Modality: r.Modality, ApplierRankFloor: r.ApplierRankFloor, Applier: applier,
		})
	}
	return out, nil
}

func (a *lockPersistence) SaveLock(ctx context.Context, l signaling.PersistedLock) error {
	var applier *string
	if l.Applier != "" {
		applier = &l.Applier
	}
	return a.store.SaveLock(ctx, store.PassLock{
		PassID: l.Target, Modality: l.Modality, ApplierRankFloor: l.ApplierRankFloor,
		ApplierPassID: applier, CreatedAt: time.Now().Unix(),
	})
}

func (a *lockPersistence) DeleteLock(ctx context.Context, target, modality string) error {
	return a.store.DeleteLock(ctx, target, modality)
}
