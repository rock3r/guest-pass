package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrSlotHostMismatch means a pass was assigned a slot that does not belong to the
// pass's stream's host. SQLite cannot express this cross-table CHECK, so the store
// enforces it in the app layer (RF-2 / CONVENTIONS §2.5).
var ErrSlotHostMismatch = errors.New("store: slot does not belong to the stream's host")

// CreatePassParams are the fields a caller supplies to create a pass; the repo
// generates the id. The token hash is HMAC(secret, token) computed by the caller (EN-5).
// Slot binding is assigned separately via AssignPassSlot (D-20).
type CreatePassParams struct {
	StreamID  string
	Name      *string // guest PII (D-37)
	Email     *string // guest PII (D-37)
	Role      string  // guest | cohost
	TokenHash string
	CanScreen bool
	Status    string
	ExpiresAt *int64
}

// CreatePass inserts a new pass for a stream.
func (s *Store) CreatePass(ctx context.Context, p CreatePassParams) (*Pass, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	role := p.Role
	if role == "" {
		role = RoleGuest
	}
	status := p.Status
	if status == "" {
		status = PassCreated
	}
	pass := &Pass{
		ID:        id,
		StreamID:  p.StreamID,
		Name:      p.Name,
		Email:     p.Email,
		Role:      role,
		TokenHash: p.TokenHash,
		CanScreen: p.CanScreen,
		Status:    status,
		ExpiresAt: p.ExpiresAt,
	}
	_, err = s.writer.ExecContext(ctx,
		`INSERT INTO passes (id, stream_id, slot_id, name, email, role, token_hash, can_screen, status, expires_at)
		 VALUES (?, ?, NULL, ?, ?, ?, ?, ?, ?, ?)`,
		pass.ID, pass.StreamID, pass.Name, pass.Email, pass.Role, pass.TokenHash, boolToInt(pass.CanScreen), pass.Status, pass.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("inserting pass: %w", err)
	}
	return pass, nil
}

// GetPass returns the pass with the given id, or ErrNotFound.
func (s *Store) GetPass(ctx context.Context, id string) (*Pass, error) {
	return scanPass(s.reader.QueryRowContext(ctx, passSelect+" WHERE id = ?", id))
}

// GetPassByTokenHash resolves a pass from its magic-link token hash — the lookup
// GET /p/{token} performs (EN-5). The route itself is side-effect-free (EN-10): this
// read does not transition the pass status.
func (s *Store) GetPassByTokenHash(ctx context.Context, tokenHash string) (*Pass, error) {
	return scanPass(s.reader.QueryRowContext(ctx, passSelect+" WHERE token_hash = ?", tokenHash))
}

// AssignPassSlot binds a pass to a cam slot (D-20), enforcing the RF-2 same-host
// invariant: the slot must belong to the pass's stream's host. The DB additionally
// enforces at-most-one active occupant per (stream, slot) via a partial unique index.
func (s *Store) AssignPassSlot(ctx context.Context, passID, slotID string) error {
	pass, err := s.GetPass(ctx, passID)
	if err != nil {
		return err
	}
	stream, err := s.GetStream(ctx, pass.StreamID)
	if err != nil {
		return err
	}
	slot, err := s.GetSlot(ctx, slotID)
	if err != nil {
		return err
	}
	if slot.HostID != stream.HostID {
		return ErrSlotHostMismatch
	}
	res, err := s.writer.ExecContext(ctx, "UPDATE passes SET slot_id = ? WHERE id = ?", slotID, passID)
	if err != nil {
		return fmt.Errorf("assigning slot: %w", err)
	}
	return errIfNoRows(res)
}

// SetPassStatus transitions a pass to a new status and stamps the corresponding
// timestamp column (sent_at/opened_at/accepted_at/revoked_at) when applicable.
func (s *Store) SetPassStatus(ctx context.Context, id, status string) error {
	now := time.Now().Unix()
	var tsColumn string
	switch status {
	case PassSent:
		tsColumn = "sent_at"
	case PassOpened:
		tsColumn = "opened_at"
	case PassAccepted:
		tsColumn = "accepted_at"
	case PassRevoked:
		tsColumn = "revoked_at"
	}
	query := "UPDATE passes SET status = ?"
	args := []any{status}
	if tsColumn != "" {
		query += ", " + tsColumn + " = ?" // tsColumn is a fixed internal literal
		args = append(args, now)
	}
	query += " WHERE id = ?"
	args = append(args, id)
	res, err := s.writer.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("setting pass status: %w", err)
	}
	return errIfNoRows(res)
}

// DeletePass removes a pass.
func (s *Store) DeletePass(ctx context.Context, id string) error {
	res, err := s.writer.ExecContext(ctx, "DELETE FROM passes WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting pass: %w", err)
	}
	return errIfNoRows(res)
}

const passSelect = `SELECT id, stream_id, slot_id, name, email, role, token_hash, can_screen,
	status, sent_at, expires_at, opened_at, accepted_at, revoked_at FROM passes`

func scanPass(row *sql.Row) (*Pass, error) {
	var p Pass
	var canScreen int64
	err := row.Scan(&p.ID, &p.StreamID, &p.SlotID, &p.Name, &p.Email, &p.Role, &p.TokenHash, &canScreen,
		&p.Status, &p.SentAt, &p.ExpiresAt, &p.OpenedAt, &p.AcceptedAt, &p.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning pass: %w", err)
	}
	p.CanScreen = canScreen != 0
	return &p, nil
}
