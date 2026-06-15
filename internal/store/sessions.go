package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrSessionAlreadyLive means a host tried to start a second live session while one is already
// active. v1 allows exactly one live session per host (EN-2/D-20) so the host-global slot pool
// resolves to one show; concurrent shows are v1.1. The partial unique index idx_sessions_one_live
// backstops this at the DB even if two StartSession calls race.
var ErrSessionAlreadyLive = errors.New("store: host already has a live session")

const sessionSelect = `SELECT id, stream_id, host_id, started_at, ended_at, status FROM sessions`

// StartSession opens the host's one live session for streamID (EN-2/D-20). It validates that the
// stream belongs to the host (RF-2; a foreign or missing stream answers ErrNotFound so ids can't
// be probed) and refuses a second concurrent session (ErrSessionAlreadyLive). The check + insert
// run in one writer transaction so they are atomic against a racing StartSession; the partial
// unique index is the final backstop.
func (s *Store) StartSession(ctx context.Context, streamID, hostID string) (*Session, error) {
	stream, err := s.GetStream(ctx, streamID)
	if errors.Is(err, ErrNotFound) || (err == nil && stream.HostID != hostID) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("starting session: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful commit

	// Read the live state inside the same writer tx (serialized against other writers) so the
	// at-most-one-active invariant can't be straddled by a concurrent start.
	var existing string
	switch err := tx.QueryRowContext(ctx,
		"SELECT id FROM sessions WHERE host_id = ? AND status = ?", hostID, SessionActive).Scan(&existing); {
	case err == nil:
		return nil, ErrSessionAlreadyLive
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("starting session: %w", err)
	}

	sess := &Session{ID: id, StreamID: streamID, HostID: hostID, StartedAt: time.Now().Unix(), Status: SessionActive}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO sessions (id, stream_id, host_id, started_at, status) VALUES (?, ?, ?, ?, ?)",
		sess.ID, sess.StreamID, sess.HostID, sess.StartedAt, sess.Status); err != nil {
		return nil, fmt.Errorf("starting session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("starting session: %w", err)
	}
	return sess, nil
}

// EndActiveSession closes the host's active session, stamping ended_at. It is idempotent: ending
// when nothing is live is a no-op (no error), so a double "end" or an end after an idle reap is safe.
func (s *Store) EndActiveSession(ctx context.Context, hostID string) error {
	_, err := s.writer.ExecContext(ctx,
		"UPDATE sessions SET status = ?, ended_at = ? WHERE host_id = ? AND status = ?",
		SessionEnded, time.Now().Unix(), hostID, SessionActive)
	if err != nil {
		return fmt.Errorf("ending session: %w", err)
	}
	return nil
}

// ActiveSession returns the host's live session, or ErrNotFound when the host is not live. The
// active session's stream_id is the runtime "which stream is live" used to gate the join-replay.
func (s *Store) ActiveSession(ctx context.Context, hostID string) (*Session, error) {
	return scanSession(s.reader.QueryRowContext(ctx,
		sessionSelect+" WHERE host_id = ? AND status = ?", hostID, SessionActive))
}

func scanSession(row *sql.Row) (*Session, error) {
	var sess Session
	err := row.Scan(&sess.ID, &sess.StreamID, &sess.HostID, &sess.StartedAt, &sess.EndedAt, &sess.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning session: %w", err)
	}
	return &sess, nil
}
