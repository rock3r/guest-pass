package store

import (
	"context"
	"fmt"
	"time"
)

// Report categories (D-42). The DB CHECK constraint is the source of truth; the web layer validates
// the submitted value against this set before insert.
const (
	ReportSpam       = "spam"
	ReportDontKnow   = "dont-know"
	ReportPhishing   = "phishing"
	ReportHarassment = "harassment"
	ReportOther      = "other"
)

// ReportCategories is the allowed category set, in display order.
var ReportCategories = []string{ReportSpam, ReportDontKnow, ReportPhishing, ReportHarassment, ReportOther}

// ValidReportCategory reports whether c is an allowed report category.
func ValidReportCategory(c string) bool {
	for _, v := range ReportCategories {
		if v == c {
			return true
		}
	}
	return false
}

// CreateReportParams are the fields of a new abuse report. host_id / stream_id / reporter_email are
// resolved server-side from the reporter's magic-link token (EN-24) — never form input; category +
// message come from the public form (both required).
type CreateReportParams struct {
	HostID        string
	StreamID      *string
	ReporterEmail *string
	Category      string
	Message       string
}

// CreateReport records one individual abuse report against a host (D-42).
func (s *Store) CreateReport(ctx context.Context, p CreateReportParams) (*Report, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	msg := p.Message
	r := &Report{
		ID: id, HostID: p.HostID, StreamID: p.StreamID, ReporterEmail: p.ReporterEmail,
		Category: p.Category, Message: &msg, CreatedAt: time.Now().Unix(),
	}
	_, err = s.writer.ExecContext(ctx,
		`INSERT INTO reports (id, host_id, stream_id, reporter_email, category, message, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.HostID, r.StreamID, r.ReporterEmail, r.Category, r.Message, r.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("inserting report: %w", err)
	}
	return r, nil
}

// ReportRow is one report joined to its reported host + (possibly-deleted) stream, for the admin
// review surface (D-42). reporter_email / message are nil once anonymized.
type ReportRow struct {
	ID            string
	HostID        string
	HostName      string
	StreamID      *string
	StreamTitle   *string
	ReporterEmail *string
	Category      string
	Message       *string
	CreatedAt     int64
}

// ListReports returns every abuse report newest-first within each host, grouped-friendly (ordered by
// host then time) for the admin review surface (D-42 / AC-11). Admin-only data — the reporter
// identity it carries is never shown to the reported host.
func (s *Store) ListReports(ctx context.Context) ([]ReportRow, error) {
	rows, err := s.reader.QueryContext(ctx,
		`SELECT r.id, r.host_id, h.name, r.stream_id, st.title, r.reporter_email, r.category, r.message, r.created_at
		 FROM reports r
		 JOIN hosts h ON h.id = r.host_id
		 LEFT JOIN streams st ON st.id = r.stream_id
		 ORDER BY r.host_id, r.created_at DESC, r.id`)
	if err != nil {
		return nil, fmt.Errorf("listing reports: %w", err)
	}
	defer rows.Close()
	var out []ReportRow
	for rows.Next() {
		var r ReportRow
		if err := rows.Scan(&r.ID, &r.HostID, &r.HostName, &r.StreamID, &r.StreamTitle, &r.ReporterEmail, &r.Category, &r.Message, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning report: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating reports: %w", err)
	}
	return out, nil
}

// DeleteReportsByHost clears all of a host's reports — the admin "Dismiss all" action (D-42). Returns
// the number of reports removed.
func (s *Store) DeleteReportsByHost(ctx context.Context, hostID string) (int64, error) {
	res, err := s.writer.ExecContext(ctx, "DELETE FROM reports WHERE host_id = ?", hostID)
	if err != nil {
		return 0, fmt.Errorf("dismissing reports: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// AnonymizeExpiredReports nulls reporter_email + message on reports older than retentionSecs (D-37):
// after the review window the identifying content is dropped, but the row is kept as an anonymous
// signal (host_id + category + time). Returns the number of reports anonymized. Idempotent — already-
// anonymized rows are skipped by the NOT NULL guard.
func (s *Store) AnonymizeExpiredReports(ctx context.Context, now, retentionSecs int64) (int64, error) {
	cutoff := now - retentionSecs
	res, err := s.writer.ExecContext(ctx,
		`UPDATE reports SET reporter_email = NULL, message = NULL
		 WHERE created_at < ? AND (reporter_email IS NOT NULL OR message IS NOT NULL)`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("anonymizing reports: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
