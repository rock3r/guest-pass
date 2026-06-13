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
	_, err = s.writer.ExecContext(ctx,
		`INSERT INTO hosts (id, google_sub, email, name, picture, is_admin, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		h.ID, h.GoogleSub, h.Email, h.Name, h.Picture, boolToInt(h.IsAdmin), h.Status, h.CreatedAt)
	if err != nil {
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

func (s *Store) updateHostColumn(ctx context.Context, id, column string, value any) error {
	// column is a fixed internal literal (never user input), so interpolation is safe.
	res, err := s.writer.ExecContext(ctx, "UPDATE hosts SET "+column+" = ? WHERE id = ?", value, id)
	if err != nil {
		return fmt.Errorf("updating host %s: %w", column, err)
	}
	return errIfNoRows(res)
}

const hostSelect = `SELECT id, google_sub, email, name, picture, is_admin, status, created_at FROM hosts`

func scanHost(row *sql.Row) (*Host, error) {
	var h Host
	var isAdmin int64
	err := row.Scan(&h.ID, &h.GoogleSub, &h.Email, &h.Name, &h.Picture, &isAdmin, &h.Status, &h.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning host: %w", err)
	}
	h.IsAdmin = isAdmin != 0
	return &h, nil
}
