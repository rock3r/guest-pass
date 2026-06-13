package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// GetSlot returns the slot with the given id, or ErrNotFound.
func (s *Store) GetSlot(ctx context.Context, id string) (*Slot, error) {
	return scanSlot(s.reader.QueryRowContext(ctx, slotSelect+" WHERE id = ?", id))
}

// GetSlotBySourceTokenHash resolves a slot from its source-token hash — the lookup the
// OBS source-page WS handshake (/ws?src=) performs (EN-5/EN-15).
func (s *Store) GetSlotBySourceTokenHash(ctx context.Context, tokenHash string) (*Slot, error) {
	return scanSlot(s.reader.QueryRowContext(ctx, slotSelect+" WHERE source_token_hash = ?", tokenHash))
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
