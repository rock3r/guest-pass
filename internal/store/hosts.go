package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CreateHostParams are the fields a caller supplies to create a host; the repo
// generates the id and created_at. Status defaults to pending when empty (D-28).
type CreateHostParams struct {
	GoogleSub string
	Email     string
	Name      string
	Picture   *string
	IsAdmin   bool
	Status    string
}

// CreateHost inserts a new host and returns it with its generated id and created_at.
func (s *Store) CreateHost(ctx context.Context, p CreateHostParams) (*Host, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	status := p.Status
	if status == "" {
		status = HostPending
	}
	h := &Host{
		ID:        id,
		GoogleSub: p.GoogleSub,
		Email:     p.Email,
		Name:      p.Name,
		Picture:   p.Picture,
		IsAdmin:   p.IsAdmin,
		Status:    status,
		CreatedAt: time.Now().Unix(),
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("inserting host: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO hosts (id, google_sub, email, name, picture, is_admin, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		h.ID, h.GoogleSub, h.Email, h.Name, h.Picture, boolToInt(h.IsAdmin), h.Status, h.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("inserting host: %w", err)
	}
	if err := addCounterTx(ctx, tx, CounterTotalHosts, 1, time.Now().UTC().Format(time.DateOnly)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("inserting host: %w", err)
	}
	return h, nil
}

// GetHost returns the host with the given id, or ErrNotFound.
func (s *Store) GetHost(ctx context.Context, id string) (*Host, error) {
	return scanHost(s.reader.QueryRowContext(ctx, hostSelect+" WHERE id = ?", id))
}

// GetHostByGoogleSub returns the host with the given Google subject id, or ErrNotFound.
func (s *Store) GetHostByGoogleSub(ctx context.Context, sub string) (*Host, error) {
	return scanHost(s.reader.QueryRowContext(ctx, hostSelect+" WHERE google_sub = ?", sub))
}

// SetHostStatus updates a host's status (D-28; e.g. admin approve/suspend). The change
// is read live by the authz middleware on the next request (EN-6).
func (s *Store) SetHostStatus(ctx context.Context, id, status string) error {
	return s.updateHostColumn(ctx, id, "status", status)
}

// SetHostAdmin sets or clears a host's is_admin flag (D-14).
func (s *Store) SetHostAdmin(ctx context.Context, id string, admin bool) error {
	return s.updateHostColumn(ctx, id, "is_admin", boolToInt(admin))
}

// SetHostName updates a host's display name — the GDPR rectification surface (PATCH /api/me,
// AC-4/D-37). Only the name is host-editable in-app; email + google_sub are the Google identity
// and change only by re-authenticating. The caller validates/caps the value.
func (s *Store) SetHostName(ctx context.Context, id, name string) error {
	return s.updateHostColumn(ctx, id, "name", name)
}

// DeleteHost erases a host and, via ON DELETE CASCADE, ALL of the host's data — streams (→ their
// passes, sessions, host_source_tokens, and transitively peers + pass_locks) and slots. This is
// the GDPR erasure path (DELETE /api/me, AC-5/D-37/D-M5-3): a clean host-scoped wipe. Anonymous
// counters have no foreign keys and are deliberately outside this cascade: contributions remain
// after the account is erased. Returns ErrNotFound if no such host. (foreign_keys=ON is set
// per-conn, EN-11, so the cascade fires.)
func (s *Store) DeleteHost(ctx context.Context, id string) error {
	res, err := s.writer.ExecContext(ctx, "DELETE FROM hosts WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting host: %w", err)
	}
	return errIfNoRows(res)
}

func (s *Store) updateHostColumn(ctx context.Context, id, column string, value any) error {
	// column is a fixed internal literal (never user input), so interpolation is safe.
	res, err := s.writer.ExecContext(ctx, "UPDATE hosts SET "+column+" = ? WHERE id = ?", value, id)
	if err != nil {
		return fmt.Errorf("updating host %s: %w", column, err)
	}
	return errIfNoRows(res)
}

const hostSelect = `SELECT id, google_sub, email, name, picture, is_admin, status, created_at FROM hosts`

func scanHostFrom(sc interface{ Scan(...any) error }) (*Host, error) {
	var h Host
	var isAdmin int64
	if err := sc.Scan(&h.ID, &h.GoogleSub, &h.Email, &h.Name, &h.Picture, &isAdmin, &h.Status, &h.CreatedAt); err != nil {
		return nil, err
	}
	h.IsAdmin = isAdmin != 0
	return &h, nil
}

func scanHost(row *sql.Row) (*Host, error) {
	h, err := scanHostFrom(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning host: %w", err)
	}
	return h, nil
}

func scanHostRows(rows *sql.Rows) (*Host, error) {
	h, err := scanHostFrom(rows)
	if err != nil {
		return nil, fmt.Errorf("scanning host: %w", err)
	}
	return h, nil
}
