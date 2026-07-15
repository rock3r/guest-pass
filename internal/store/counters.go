package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Counter keys are global anonymous aggregates. They intentionally carry no host,
// stream, guest, pass, or network dimension (D-37 / D-M6-6).
const (
	CounterStreamsRun            = "streams_run"
	CounterGuestConnectedSeconds = "guest_connected_seconds"
	CounterPeakConcurrent        = "peak_concurrent"
	CounterTotalHosts            = "total_hosts"
	CounterInvitesSent           = "invites_sent"
	CounterReportsFiled          = "reports_filed"
)

// DailyCounter is one UTC daily bucket for an anonymous aggregate.
type DailyCounter struct {
	Day   string
	Value int64
}

// AddCounter atomically adds delta to the global lifetime value and today's UTC
// daily bucket. Counters are increment-only, so negative deltas are rejected.
func (s *Store) AddCounter(ctx context.Context, key string, delta int64) error {
	if delta < 0 {
		return fmt.Errorf("adding counter %q: negative delta", key)
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("adding counter %q: %w", key, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := addCounterTx(ctx, tx, key, delta, time.Now().UTC().Format(time.DateOnly)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("adding counter %q: %w", key, err)
	}
	return nil
}

func addCounterTx(ctx context.Context, tx *sql.Tx, key string, delta int64, day string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO counters (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = counters.value + excluded.value`, key, delta); err != nil {
		return fmt.Errorf("adding counter %q: %w", key, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO counters_daily (key, day, value) VALUES (?, ?, ?)
		 ON CONFLICT(key, day) DO UPDATE SET value = counters_daily.value + excluded.value`, key, day, delta); err != nil {
		return fmt.Errorf("adding daily counter %q: %w", key, err)
	}
	return nil
}

// BumpMax stores value when it exceeds the existing global and daily peak. It is
// used for a session's observed maximum concurrent participants.
func (s *Store) BumpMax(ctx context.Context, key string, value int64) error {
	if value < 0 {
		return fmt.Errorf("bumping counter %q: negative value", key)
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("bumping counter %q: %w", key, err)
	}
	defer func() { _ = tx.Rollback() }()
	day := time.Now().UTC().Format(time.DateOnly)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO counters (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = MAX(counters.value, excluded.value)`, key, value); err != nil {
		return fmt.Errorf("bumping counter %q: %w", key, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO counters_daily (key, day, value) VALUES (?, ?, ?)
		 ON CONFLICT(key, day) DO UPDATE SET value = MAX(counters_daily.value, excluded.value)`, key, day, value); err != nil {
		return fmt.Errorf("bumping counter %q: %w", key, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("bumping counter %q: %w", key, err)
	}
	return nil
}

// Counter returns a lifetime aggregate, treating an unseen key as zero.
func (s *Store) Counter(ctx context.Context, key string) (int64, error) {
	var value int64
	err := s.reader.QueryRowContext(ctx, "SELECT value FROM counters WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading counter %q: %w", key, err)
	}
	return value, nil
}

// CounterSeries returns daily anonymous buckets from sinceDay (inclusive). An empty
// sinceDay returns every retained bucket, ordered for direct trend rendering.
func (s *Store) CounterSeries(ctx context.Context, key, sinceDay string) ([]DailyCounter, error) {
	query := "SELECT day, value FROM counters_daily WHERE key = ?"
	args := []any{key}
	if sinceDay != "" {
		query += " AND day >= ?"
		args = append(args, sinceDay)
	}
	query += " ORDER BY day"
	rows, err := s.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading counter series %q: %w", key, err)
	}
	defer rows.Close()
	var out []DailyCounter
	for rows.Next() {
		var point DailyCounter
		if err := rows.Scan(&point.Day, &point.Value); err != nil {
			return nil, fmt.Errorf("scanning counter series %q: %w", key, err)
		}
		out = append(out, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating counter series %q: %w", key, err)
	}
	return out, nil
}

// CountActiveHosts is a current-state gauge, deliberately not retained as a
// counter because it must reflect suspensions and account erasure immediately.
func (s *Store) CountActiveHosts(ctx context.Context) (int64, error) {
	var count int64
	if err := s.reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM hosts WHERE status = ?", HostActive).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting active hosts: %w", err)
	}
	return count, nil
}
