package store

import (
	"context"
	"fmt"
)

// PurgeGuestPII clears guest PII (passes.name + passes.email) for passes whose stream is
// done with, enforcing the "deleted within 24h of stream end" retention guarantee (D-37 /
// DESIGN §8). It is the data-layer half of the in-binary purge job (internal/jobs); the
// caller supplies the policy numbers (retentionSecs + graceSecs) so the store holds no policy.
//
// A pass is eligible when its PII is not already cleared (so an already-NULL pass is never
// re-matched — the job is idempotent) AND its stream has NO currently-active session AND
// either:
//
//   - the stream has an ended session whose ended_at is at or before now-retentionSecs, OR
//   - the stream never ran (no session row at all) and its scheduled end
//     (scheduled_at + duration_min*60) plus graceSecs is at or before now-retentionSecs.
//
// It touches ONLY passes.name/email — never sessions, peers, streams, or any other row, and
// never a stream that is not yet eligible — so operational rows and any (future) anonymous
// aggregates are preserved (AC-1). A stream that carries PII but has neither an ended session
// nor a computable scheduled end (a pure draft, or a scheduled stream with no duration) is
// left alone: there is no stream-end to key the retention window off. Host account deletion
// (PR-3, DELETE /api/me) is the path that wipes a draft's PII. Returns the number of passes
// cleared, for logging.
func (s *Store) PurgeGuestPII(ctx context.Context, now, retentionSecs, graceSecs int64) (int64, error) {
	cutoff := now - retentionSecs // a stream-end at or before this is past the retention window
	res, err := s.writer.ExecContext(ctx, `
UPDATE passes
SET name = NULL, email = NULL
WHERE (name IS NOT NULL OR email IS NOT NULL)
  AND NOT EXISTS (
        SELECT 1 FROM sessions sa
        WHERE sa.stream_id = passes.stream_id AND sa.status = 'active')
  AND (
        EXISTS (
          SELECT 1 FROM sessions se
          WHERE se.stream_id = passes.stream_id
            AND se.status = 'ended' AND se.ended_at IS NOT NULL AND se.ended_at <= ?)
        OR (
          NOT EXISTS (SELECT 1 FROM sessions sn WHERE sn.stream_id = passes.stream_id)
          AND EXISTS (
            SELECT 1 FROM streams st
            WHERE st.id = passes.stream_id
              AND st.scheduled_at IS NOT NULL AND st.duration_min IS NOT NULL
              AND (st.scheduled_at + st.duration_min * 60 + ?) <= ?)
        )
  )`, cutoff, graceSecs, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purging guest PII: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purging guest PII: %w", err)
	}
	return n, nil
}
