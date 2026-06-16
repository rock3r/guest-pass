package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CreateSlotParams are the fields a caller supplies to create a slot; the repo
// generates the id. Epoch starts at 0 (RF-6: authoritative in-memory, persisted at
// lifecycle edges).
type CreateSlotParams struct {
	HostID          string
	Kind            string // cam | host | screenshare
	Idx             *int64 // cam slots 1..8; nil for host/screenshare
	SourceTokenHash string // HMAC(secret, token) (EN-5)
}

// CreateSlot inserts a new slot in a host's global pool (D-20).
func (s *Store) CreateSlot(ctx context.Context, p CreateSlotParams) (*Slot, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	sl := &Slot{
		ID:              id,
		HostID:          p.HostID,
		Kind:            p.Kind,
		Idx:             p.Idx,
		SourceTokenHash: p.SourceTokenHash,
		Epoch:           0,
	}
	_, err = s.writer.ExecContext(ctx,
		`INSERT INTO slots (id, host_id, kind, idx, source_token_hash, epoch)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sl.ID, sl.HostID, sl.Kind, sl.Idx, sl.SourceTokenHash, sl.Epoch)
	if err != nil {
		return nil, fmt.Errorf("inserting slot: %w", err)
	}
	return sl, nil
}

// SlotSpec is one desired slot in the host-global pool, with a caller-minted hashed token
// (the store never does crypto, EN-5). Used by EnsureSlotPool.
type SlotSpec struct {
	Kind            string
	Idx             *int64
	SourceTokenHash string
}

// EnsureSlotPool idempotently provisions a host's slot pool (D-20): in a SINGLE transaction
// it inserts every spec that does not already exist for the host and returns a parallel
// slice marking which specs it actually inserted. Because the whole pool is decided and
// written in one transaction on the single-writer connection (EN-11), two concurrent first
// opens can't each create a different subset — the first opener inserts the full missing set
// (and so reveals all those URLs) while the second sees them already present and inserts
// none. Existing slots are never overwritten (the insert is ignored on the partial-unique
// cam/singleton indexes). (SQLite `INSERT OR IGNORE`; the Postgres-portable form is
// `INSERT ... ON CONFLICT DO NOTHING`.)
func (s *Store) EnsureSlotPool(ctx context.Context, hostID string, specs []SlotSpec) ([]bool, error) {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("ensuring slot pool: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful commit
	inserted := make([]bool, len(specs))
	for i, sp := range specs {
		id, err := newID()
		if err != nil {
			return nil, err
		}
		res, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO slots (id, host_id, kind, idx, source_token_hash, epoch)
			 VALUES (?, ?, ?, ?, ?, 0)`,
			id, hostID, sp.Kind, sp.Idx, sp.SourceTokenHash)
		if err != nil {
			return nil, fmt.Errorf("ensuring slot pool: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("ensuring slot pool: %w", err)
		}
		inserted[i] = n > 0
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("ensuring slot pool: %w", err)
	}
	return inserted, nil
}

// RotateSlotToken replaces a slot's source-token hash (D-22 "my URLs leaked"): the old hash
// is overwritten so the previous OBS URL stops authenticating (one active token per slot,
// EN-5), and the leak-detection metadata (last_used_at/last_source_ip) is cleared since it
// described the now-dead token. The caller mints the new token and tears down any live
// /s/{slot} subscription with a token-rotated terminate.
func (s *Store) RotateSlotToken(ctx context.Context, slotID, newTokenHash string) error {
	res, err := s.writer.ExecContext(ctx,
		`UPDATE slots SET source_token_hash = ?, source_token_last_used_at = NULL,
		 source_token_last_source_ip = NULL WHERE id = ?`,
		newTokenHash, slotID)
	if err != nil {
		return fmt.Errorf("rotating slot token: %w", err)
	}
	return errIfNoRows(res)
}

// RotateSlotTokens rotates several slots' source tokens in ONE transaction — the rotate-all
// "my URLs leaked" panic (D-22). Either every hash is overwritten (all old URLs dead) or none
// is, so a mid-batch failure never leaves some slots on fresh, un-revealed tokens. Each entry
// must name an existing slot (ErrNotFound otherwise rolls the whole batch back).
func (s *Store) RotateSlotTokens(ctx context.Context, newHashByID map[string]string) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("rotating slot tokens: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful commit
	for id, hash := range newHashByID {
		res, err := tx.ExecContext(ctx,
			`UPDATE slots SET source_token_hash = ?, source_token_last_used_at = NULL,
			 source_token_last_source_ip = NULL WHERE id = ?`,
			hash, id)
		if err != nil {
			return fmt.Errorf("rotating slot tokens: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("rotating slot tokens: %w", err)
		} else if n == 0 {
			return ErrNotFound
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rotating slot tokens: %w", err)
	}
	return nil
}

// GetSlot returns the slot with the given id, or ErrNotFound.
func (s *Store) GetSlot(ctx context.Context, id string) (*Slot, error) {
	return scanSlot(s.reader.QueryRowContext(ctx, slotSelect+" WHERE id = ?", id))
}

// GetSlotBySourceTokenHash resolves a slot from its source-token hash — the lookup the
// OBS source-page WS handshake (/ws?src=) performs (EN-5/EN-15).
func (s *Store) GetSlotBySourceTokenHash(ctx context.Context, tokenHash string) (*Slot, error) {
	return scanSlot(s.reader.QueryRowContext(ctx, slotSelect+" WHERE source_token_hash = ?", tokenHash))
}

// GetHostCamSlot resolves a host's cam slot by index (1..8), or ErrNotFound if it isn't
// provisioned. Backs the greenroom slot picker, which names a slot by its cam index.
func (s *Store) GetHostCamSlot(ctx context.Context, hostID string, idx int64) (*Slot, error) {
	return scanSlot(s.reader.QueryRowContext(ctx,
		slotSelect+" WHERE host_id = ? AND kind = ? AND idx = ?", hostID, SlotCam, idx))
}

// RecordSlotTokenUse stamps a slot's source-token leak-detection metadata
// (source_token_last_used_at = now, source_token_last_source_ip = sourceIP). The
// /ws?src= source-page handshake calls this after resolving a slot via
// GetSlotBySourceTokenHash, so a host can spot an unexpected live subscription
// (AD-23 / EN-5). The lookup stays a read (reader pool); this is the paired write.
func (s *Store) RecordSlotTokenUse(ctx context.Context, slotID, sourceIP string) error {
	res, err := s.writer.ExecContext(ctx,
		"UPDATE slots SET source_token_last_used_at = ?, source_token_last_source_ip = ? WHERE id = ?",
		time.Now().Unix(), sourceIP, slotID)
	if err != nil {
		return fmt.Errorf("recording slot token use: %w", err)
	}
	return errIfNoRows(res)
}

// ListSlotsByHost returns a host's slot pool ordered by kind then idx.
func (s *Store) ListSlotsByHost(ctx context.Context, hostID string) ([]*Slot, error) {
	rows, err := s.reader.QueryContext(ctx, slotSelect+" WHERE host_id = ? ORDER BY kind, idx, id", hostID)
	if err != nil {
		return nil, fmt.Errorf("listing slots: %w", err)
	}
	defer rows.Close()
	var out []*Slot
	for rows.Next() {
		sl, err := scanSlotFrom(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning slot: %w", err)
		}
		out = append(out, sl)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating slots: %w", err)
	}
	return out, nil
}

const slotSelect = `SELECT id, host_id, kind, idx, source_token_hash,
	source_token_last_used_at, source_token_last_source_ip, epoch FROM slots`

func scanSlotFrom(sc streamScanner) (*Slot, error) {
	var sl Slot
	err := sc.Scan(&sl.ID, &sl.HostID, &sl.Kind, &sl.Idx, &sl.SourceTokenHash,
		&sl.SourceTokenLastUsedAt, &sl.SourceTokenLastSourceIP, &sl.Epoch)
	if err != nil {
		return nil, err
	}
	return &sl, nil
}

func scanSlot(row *sql.Row) (*Slot, error) {
	sl, err := scanSlotFrom(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning slot: %w", err)
	}
	return sl, nil
}
