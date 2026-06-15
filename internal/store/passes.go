package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// ErrSlotHostMismatch means a pass was assigned a slot that does not belong to the
// pass's stream's host. SQLite cannot express this cross-table CHECK, so the store
// enforces it in the app layer (RF-2 / CONVENTIONS §2.5).
var ErrSlotHostMismatch = errors.New("store: slot does not belong to the stream's host")

// ErrSlotNotCam means a pass was assigned a non-cam slot. Pass occupants bind only to
// cam slots (D-20); host slots (D-18) and the shared screenshare slot (D-21) are not
// pass-bound, so binding one would falsely show a guest occupying that source and
// wrongly consume the active-occupant unique index.
var ErrSlotNotCam = errors.New("store: passes can only be assigned to cam slots")

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

// ListPassesByStream returns a stream's passes in a stable, deterministic order by id.
// Pass ids are random (UUIDv4), so this is NOT chronological; the passes table has no
// created_at column, and chronological ordering would need a schema change (a later
// milestone if the host UI calls for it).
func (s *Store) ListPassesByStream(ctx context.Context, streamID string) ([]*Pass, error) {
	rows, err := s.reader.QueryContext(ctx, passSelect+" WHERE stream_id = ? ORDER BY id", streamID)
	if err != nil {
		return nil, fmt.Errorf("listing passes: %w", err)
	}
	defer rows.Close()
	var out []*Pass
	for rows.Next() {
		p, err := scanPassRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating passes: %w", err)
	}
	return out, nil
}

// AssignPassSlot binds a pass to a cam slot (D-20), enforcing the RF-2 same-host invariant
// (the slot must belong to the pass's stream's host) and cam-only. Binding onto a slot another
// active guest holds DISPLACES them — the prior occupant's binding is cleared and the new one
// assigned in ONE transaction (the DoD "swap a slot occupant"), so a failure can never leave
// the displaced guest unbound while the new bind is lost. The partial unique index on
// (stream, slot) backstops the at-most-one-occupant invariant.
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
	if slot.Kind != SlotCam {
		return ErrSlotNotCam
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("assigning slot: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful commit
	// Clear the slot from ANY other pass still pointing at this (stream, slot): the active occupant
	// being displaced (the DoD "swap"), AND any retired row that kept a stale slot_id when it was
	// revoked/expired. Leaving a retired row's slot_id set would let a later Re-issue re-activate it
	// back into the partial unique index and collide with the new occupant (codex). Retired rows
	// aren't live-connected, so this never diverges from the room.
	if _, err := tx.ExecContext(ctx,
		`UPDATE passes SET slot_id = NULL WHERE stream_id = ? AND slot_id = ? AND id != ?`,
		stream.ID, slotID, passID); err != nil {
		return fmt.Errorf("assigning slot: %w", err)
	}
	res, err := tx.ExecContext(ctx, "UPDATE passes SET slot_id = ? WHERE id = ?", slotID, passID)
	if err != nil {
		return fmt.Errorf("assigning slot: %w", err)
	}
	if err := errIfNoRows(res); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("assigning slot: %w", err)
	}
	return nil
}

// BoundCamPass is a pass currently bound to a cam slot, paired with that slot's label — used to
// reconcile a freshly-live room with persisted bindings on Go live (D-40 replay).
type BoundCamPass struct {
	PassID    string
	SlotLabel string // "cam-1".."cam-8"
}

// BoundCamPassesForStream lists the stream's still-JOINABLE passes bound to a cam slot, each with
// its "cam-N" label. On Go live the handler replays these into the now-live room so a guest who
// connected BEFORE the session started (its join-replay was gated off, any pre-live bind DB-only)
// gets bound without the host re-picking. It mirrors passJoinable: revoked/expired-by-status AND
// passes past their expires_at DEADLINE are excluded — a guest can connect pre-live and then cross
// its deadline before Go live (status still "sent"), and such a no-longer-joinable invite must not
// be replayed onto an OBS slot (codex).
func (s *Store) BoundCamPassesForStream(ctx context.Context, streamID string) ([]BoundCamPass, error) {
	rows, err := s.reader.QueryContext(ctx,
		`SELECT p.id, sl.idx FROM passes p JOIN slots sl ON p.slot_id = sl.id
		 WHERE p.stream_id = ? AND sl.kind = ? AND p.status NOT IN (?, ?)
		   AND (p.expires_at IS NULL OR p.expires_at > ?)`,
		streamID, SlotCam, PassRevoked, PassExpired, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("listing bound cam passes: %w", err)
	}
	defer rows.Close()
	var out []BoundCamPass
	for rows.Next() {
		var id string
		var idx int64
		if err := rows.Scan(&id, &idx); err != nil {
			return nil, fmt.Errorf("scanning bound cam pass: %w", err)
		}
		out = append(out, BoundCamPass{PassID: id, SlotLabel: "cam-" + strconv.FormatInt(idx, 10)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating bound cam passes: %w", err)
	}
	return out, nil
}

// OtherStreamPassIDs returns the ids of all passes belonging to the host's streams OTHER than
// exceptStreamID (their room peer ids). On Go live for one stream they are evicted from the
// host-scoped room, so a guest of a NON-live stream that connected pre-live can't linger in (and
// mesh with) the now-live session — the admission gate refuses NEW such joins, this clears the
// pre-live stragglers (codex). EvictPeers no-ops the ids that aren't actually connected.
func (s *Store) OtherStreamPassIDs(ctx context.Context, hostID, exceptStreamID string) ([]string, error) {
	rows, err := s.reader.QueryContext(ctx,
		`SELECT p.id FROM passes p JOIN streams st ON p.stream_id = st.id
		 WHERE st.host_id = ? AND p.stream_id != ?`,
		hostID, exceptStreamID)
	if err != nil {
		return nil, fmt.Errorf("listing other-stream passes: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning other-stream pass: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating other-stream passes: %w", err)
	}
	return out, nil
}

// ClearPassSlot unbinds a pass from any slot (slot_id → NULL) — the persistent half of a
// greenroom "unassign" (the live half is Room.Unbind).
func (s *Store) ClearPassSlot(ctx context.Context, passID string) error {
	res, err := s.writer.ExecContext(ctx, "UPDATE passes SET slot_id = NULL WHERE id = ?", passID)
	if err != nil {
		return fmt.Errorf("clearing pass slot: %w", err)
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

// MarkPassOpened atomically transitions a pass to "opened" ONLY from a pre-opened state
// (created/sent) AND only while it is not past its expiry deadline, stamping opened_at. It
// returns true if it performed the transition and false otherwise — so concurrent or
// repeated device-check entries mark a pass opened exactly once with no read-then-write
// race, and a pass that expires in the gap after the caller's pre-check still can't be
// opened (EN-10). The caller still rejects revoked/expired/past-deadline passes up front so
// it can return the right status to the guest.
func (s *Store) MarkPassOpened(ctx context.Context, id string) (bool, error) {
	now := time.Now().Unix()
	res, err := s.writer.ExecContext(ctx,
		`UPDATE passes SET status = ?, opened_at = ?
		 WHERE id = ? AND status IN (?, ?) AND (expires_at IS NULL OR expires_at > ?)`,
		PassOpened, now, id, PassCreated, PassSent, now)
	if err != nil {
		return false, fmt.Errorf("marking pass opened: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("marking pass opened: %w", err)
	}
	return n > 0, nil
}

// SetPassRole updates a pass's role (guest|cohost). Role is an invitation attribute,
// editable from the host's invites tab and also live in the greenroom via promote/demote
// (D-15). The caller validates the role value against the allowed set.
func (s *Store) SetPassRole(ctx context.Context, id, role string) error {
	res, err := s.writer.ExecContext(ctx, "UPDATE passes SET role = ? WHERE id = ?", role, id)
	if err != nil {
		return fmt.Errorf("setting pass role: %w", err)
	}
	return errIfNoRows(res)
}

// ReissuePass rotates a pass's magic-link token to newTokenHash and returns it to the
// "sent" state, stamping sent_at (PD-2). The previous hash is overwritten, so the old link
// stops resolving — one active token per pass (EN-5). It also CLEARS expires_at so the
// fresh link can't be born already-expired (D-5: re-issuing an expired pass mints a fresh,
// usable token); a later expiry-derivation pass re-stamps a deadline. It deliberately leaves
// slot_id ALONE: re-issuing a currently-bound, connected guest must NOT silently unbind them
// (that would diverge from the live room, which keeps rendering them on /s/cam-* — codex). A
// stale binding on a retired-then-reissued pass can't collide because AssignPassSlot already
// clears any other row on a slot when it's (re)assigned. The rest of the row's history
// (opened_at/accepted_at) is kept (same row).
func (s *Store) ReissuePass(ctx context.Context, id, newTokenHash string) error {
	res, err := s.writer.ExecContext(ctx,
		"UPDATE passes SET token_hash = ?, status = ?, sent_at = ?, expires_at = NULL WHERE id = ?",
		newTokenHash, PassSent, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("reissuing pass: %w", err)
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

func scanPassFrom(sc streamScanner) (*Pass, error) {
	var p Pass
	var canScreen int64
	if err := sc.Scan(&p.ID, &p.StreamID, &p.SlotID, &p.Name, &p.Email, &p.Role, &p.TokenHash, &canScreen,
		&p.Status, &p.SentAt, &p.ExpiresAt, &p.OpenedAt, &p.AcceptedAt, &p.RevokedAt); err != nil {
		return nil, err
	}
	p.CanScreen = canScreen != 0
	return &p, nil
}

func scanPassRows(rows *sql.Rows) (*Pass, error) {
	p, err := scanPassFrom(rows)
	if err != nil {
		return nil, fmt.Errorf("scanning pass: %w", err)
	}
	return p, nil
}

func scanPass(row *sql.Row) (*Pass, error) {
	p, err := scanPassFrom(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning pass: %w", err)
	}
	return p, nil
}
