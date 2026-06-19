package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CreateStreamParams are the fields a caller supplies to create a stream; the repo
// generates id and created_at. Status defaults to draft when empty.
type CreateStreamParams struct {
	HostID           string
	Title            string
	ScheduledAt      *int64
	DurationMin      *int64
	Status           string
	MaxRes           *int64
	MaxFPS           *int64
	MaxBitrateKbps   *int64
	TwitchYTChannel  *string
	TwitchYTPlatform *string
}

// CreateStream inserts a new stream owned by the given host.
func (s *Store) CreateStream(ctx context.Context, p CreateStreamParams) (*Stream, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	status := p.Status
	if status == "" {
		status = StreamDraft
	}
	st := &Stream{
		ID:          id,
		HostID:      p.HostID,
		Title:       p.Title,
		ScheduledAt: p.ScheduledAt,
		DurationMin: p.DurationMin,
		Status:      status,
		// A new stream ships with the default program quality ceiling (D-19/AC-8) unless the caller
		// set one, so the program encoder is always bounded and the degradation ladder has a recovery
		// ceiling. The host adjusts it live via the greenroom People tab.
		MaxRes:           defaultInt64(p.MaxRes, DefaultMaxRes),
		MaxFPS:           defaultInt64(p.MaxFPS, DefaultMaxFPS),
		MaxBitrateKbps:   defaultInt64(p.MaxBitrateKbps, DefaultMaxBitrateKbps),
		TwitchYTChannel:  p.TwitchYTChannel,
		TwitchYTPlatform: p.TwitchYTPlatform,
		CreatedAt:        time.Now().Unix(),
	}
	_, err = s.writer.ExecContext(ctx,
		`INSERT INTO streams (id, host_id, title, scheduled_at, duration_min, status,
		 max_res, max_fps, max_bitrate_kbps, twitch_yt_channel, twitch_yt_platform, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		st.ID, st.HostID, st.Title, st.ScheduledAt, st.DurationMin, st.Status,
		st.MaxRes, st.MaxFPS, st.MaxBitrateKbps, st.TwitchYTChannel, st.TwitchYTPlatform, st.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("inserting stream: %w", err)
	}
	return st, nil
}

// defaultInt64 returns v when non-nil, else a pointer to def — so a caller that leaves a nullable
// ceiling column unset gets the product default while an explicit value (incl. a deliberate one) is
// preserved.
func defaultInt64(v *int64, def int64) *int64 {
	if v != nil {
		return v
	}
	return &def
}

// GetStream returns the stream with the given id, or ErrNotFound.
func (s *Store) GetStream(ctx context.Context, id string) (*Stream, error) {
	return scanStream(s.reader.QueryRowContext(ctx, streamSelect+" WHERE id = ?", id))
}

// ListStreamsByHost returns a host's streams, newest first.
func (s *Store) ListStreamsByHost(ctx context.Context, hostID string) ([]*Stream, error) {
	rows, err := s.reader.QueryContext(ctx, streamSelect+" WHERE host_id = ? ORDER BY created_at DESC, id", hostID)
	if err != nil {
		return nil, fmt.Errorf("listing streams: %w", err)
	}
	defer rows.Close()
	var out []*Stream
	for rows.Next() {
		st, err := scanStreamRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating streams: %w", err)
	}
	return out, nil
}

// UpdateStream writes the editable columns of st (identified by st.ID). host_id and
// created_at are immutable and not updated.
func (s *Store) UpdateStream(ctx context.Context, st *Stream) error {
	res, err := s.writer.ExecContext(ctx,
		`UPDATE streams SET title = ?, scheduled_at = ?, duration_min = ?, status = ?,
		 max_res = ?, max_fps = ?, max_bitrate_kbps = ?, twitch_yt_channel = ?, twitch_yt_platform = ?
		 WHERE id = ?`,
		st.Title, st.ScheduledAt, st.DurationMin, st.Status,
		st.MaxRes, st.MaxFPS, st.MaxBitrateKbps, st.TwitchYTChannel, st.TwitchYTPlatform, st.ID)
	if err != nil {
		return fmt.Errorf("updating stream: %w", err)
	}
	return errIfNoRows(res)
}

// SetStreamChannel links (or, with nil args, unlinks) a stream's optional live-verify channel
// (D-29): the platform + channel id used by internal/livecheck to verify live status and build the
// guest watch-live link. Both must be set together or both nil; the caller validates them against
// the livecheck platform/channel rules first. A targeted UPDATE (not a read-modify-write of the
// whole row) so it can't clobber a concurrent edit of other columns.
func (s *Store) SetStreamChannel(ctx context.Context, id string, platform, channel *string) error {
	res, err := s.writer.ExecContext(ctx,
		"UPDATE streams SET twitch_yt_platform = ?, twitch_yt_channel = ? WHERE id = ?",
		platform, channel, id)
	if err != nil {
		return fmt.Errorf("setting stream channel: %w", err)
	}
	return errIfNoRows(res)
}

// DeleteStream removes a stream (cascading to its passes/sessions via FK).
func (s *Store) DeleteStream(ctx context.Context, id string) error {
	res, err := s.writer.ExecContext(ctx, "DELETE FROM streams WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting stream: %w", err)
	}
	return errIfNoRows(res)
}

const streamSelect = `SELECT id, host_id, title, scheduled_at, duration_min, status,
	max_res, max_fps, max_bitrate_kbps, twitch_yt_channel, twitch_yt_platform, created_at FROM streams`

type streamScanner interface {
	Scan(dest ...any) error
}

func scanStreamFrom(sc streamScanner) (*Stream, error) {
	var st Stream
	err := sc.Scan(&st.ID, &st.HostID, &st.Title, &st.ScheduledAt, &st.DurationMin, &st.Status,
		&st.MaxRes, &st.MaxFPS, &st.MaxBitrateKbps, &st.TwitchYTChannel, &st.TwitchYTPlatform, &st.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func scanStream(row *sql.Row) (*Stream, error) {
	st, err := scanStreamFrom(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning stream: %w", err)
	}
	return st, nil
}

func scanStreamRows(rows *sql.Rows) (*Stream, error) {
	st, err := scanStreamFrom(rows)
	if err != nil {
		return nil, fmt.Errorf("scanning stream: %w", err)
	}
	return st, nil
}
