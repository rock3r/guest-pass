package store

import (
	"context"
	"fmt"
)

// SaveLock upserts a suppression-lock row (AD-22): one per (pass_id, modality), so a force
// or a higher-rank re-force overwrites the applier/floor in place (the lock state machine
// keeps at most one lock per pair, D-13). A force-muted guest's row survives a restart and is
// re-applied on room respawn via LocksForHost.
func (s *Store) SaveLock(ctx context.Context, l PassLock) error {
	_, err := s.writer.ExecContext(ctx,
		`INSERT INTO pass_locks (pass_id, modality, applier_rank_floor, applier_pass_id, created_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(pass_id, modality) DO UPDATE SET
		   applier_rank_floor = excluded.applier_rank_floor,
		   applier_pass_id    = excluded.applier_pass_id,
		   created_at         = excluded.created_at`,
		l.PassID, l.Modality, l.ApplierRankFloor, l.ApplierPassID, l.CreatedAt)
	if err != nil {
		return fmt.Errorf("saving pass lock: %w", err)
	}
	return nil
}

// DeleteLock removes the suppression-lock row for (pass_id, modality) on release (D-13). It
// is idempotent — deleting a lock that isn't there is not an error (a release of an absent
// lock is a no-op upstream).
func (s *Store) DeleteLock(ctx context.Context, passID, modality string) error {
	if _, err := s.writer.ExecContext(ctx,
		`DELETE FROM pass_locks WHERE pass_id = ? AND modality = ?`, passID, modality); err != nil {
		return fmt.Errorf("deleting pass lock: %w", err)
	}
	return nil
}

// LocksForHost returns every active suppression lock whose target pass belongs to one of the
// host's streams — the set a room (re)loads on spawn to re-apply moderation (AD-22). Ordered
// for determinism.
func (s *Store) LocksForHost(ctx context.Context, hostID string) ([]PassLock, error) {
	rows, err := s.reader.QueryContext(ctx,
		`SELECT pl.pass_id, pl.modality, pl.applier_rank_floor, pl.applier_pass_id, pl.created_at
		   FROM pass_locks pl
		   JOIN passes p  ON p.id = pl.pass_id
		   JOIN streams s ON s.id = p.stream_id
		  WHERE s.host_id = ?
		  ORDER BY pl.pass_id, pl.modality`, hostID)
	if err != nil {
		return nil, fmt.Errorf("listing locks for host: %w", err)
	}
	defer rows.Close()
	var out []PassLock
	for rows.Next() {
		var l PassLock
		if err := rows.Scan(&l.PassID, &l.Modality, &l.ApplierRankFloor, &l.ApplierPassID, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning pass lock: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating pass locks: %w", err)
	}
	return out, nil
}
