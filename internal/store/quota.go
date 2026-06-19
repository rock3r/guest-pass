package store

import (
	"context"
	"fmt"
)

// CountInvitesSentByHost counts the host's invites whose email was sent within the window (sent_at >
// since) across all the host's streams — the input to the progressive-trust invite cap (D-36). Only
// delivered invites count (sent_at is stamped on send), so a created-but-undelivered pass doesn't
// consume the email quota.
func (s *Store) CountInvitesSentByHost(ctx context.Context, hostID string, sinceUnix int64) (int, error) {
	var n int
	err := s.reader.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM passes p JOIN streams st ON st.id = p.stream_id
		 WHERE st.host_id = ? AND p.sent_at IS NOT NULL AND p.sent_at > ?`, hostID, sinceUnix).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting recent invites: %w", err)
	}
	return n, nil
}

// CountStreamsByHost counts the host's existing streams — the input to the progressive-trust
// concurrent-stream cap (D-36). Deleting a stream frees a slot.
func (s *Store) CountStreamsByHost(ctx context.Context, hostID string) (int, error) {
	var n int
	err := s.reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM streams WHERE host_id = ?", hostID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting host streams: %w", err)
	}
	return n, nil
}
