package store

import (
	"context"
	"fmt"
)

// ListHosts returns every host, newest first — the admin console's hosts list (AC-9 / D-14).
// Metadata only: the admin manages hosts (approve/suspend/promote, PR-8), so it sees host identity
// (email/name/status), never any guest's PII. The caller's DTO drops internal fields (google_sub).
func (s *Store) ListHosts(ctx context.Context) ([]*Host, error) {
	rows, err := s.reader.QueryContext(ctx, hostSelect+" ORDER BY created_at DESC, id")
	if err != nil {
		return nil, fmt.Errorf("listing hosts: %w", err)
	}
	defer rows.Close()
	var out []*Host
	for rows.Next() {
		h, err := scanHostRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating hosts: %w", err)
	}
	return out, nil
}

// CountActiveAdmins returns the number of hosts that are both admin and active
// (is_admin = 1 AND status = 'active') — the input to the last-admin lockout guard (D-M5.5-5 /
// AC-9). Suspended or demoted admins are excluded, since only an active admin can administer the
// instance. The guard refuses a demote/suspend that would drop this count to zero.
func (s *Store) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.reader.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM hosts WHERE is_admin = 1 AND status = ?", HostActive).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting active admins: %w", err)
	}
	return n, nil
}

// ActiveSessionInfo is one cross-host live session as the admin console sees it (AC-9 / EN-2):
// session + owning-host + stream metadata only. It deliberately carries NO guest PII, no tokens, and
// nothing from the backstage media/chat path — the §7.7 privacy boundary. Live participant counts
// come from the hub (in-memory), not this row.
type ActiveSessionInfo struct {
	SessionID   string
	HostID      string
	HostName    string
	HostEmail   string
	StreamID    string
	StreamTitle string
	StartedAt   int64
}

// ListActiveSessions returns all currently-live sessions across every host, newest first — the
// admin console's cross-host session view (AC-9 / EN-2). Metadata only (see ActiveSessionInfo): it
// joins sessions→hosts→streams for identifying metadata and never reads passes/peers, so no guest
// PII or media/chat can leak through this path (§7.7).
func (s *Store) ListActiveSessions(ctx context.Context) ([]ActiveSessionInfo, error) {
	rows, err := s.reader.QueryContext(ctx,
		`SELECT se.id, se.host_id, h.name, h.email, se.stream_id, st.title, se.started_at
		 FROM sessions se
		 JOIN hosts h ON h.id = se.host_id
		 JOIN streams st ON st.id = se.stream_id
		 WHERE se.status = ?
		 ORDER BY se.started_at DESC, se.id`, SessionActive)
	if err != nil {
		return nil, fmt.Errorf("listing active sessions: %w", err)
	}
	defer rows.Close()
	var out []ActiveSessionInfo
	for rows.Next() {
		var a ActiveSessionInfo
		if err := rows.Scan(&a.SessionID, &a.HostID, &a.HostName, &a.HostEmail, &a.StreamID, &a.StreamTitle, &a.StartedAt); err != nil {
			return nil, fmt.Errorf("scanning active session: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating active sessions: %w", err)
	}
	return out, nil
}

// TurnRelayStats returns anonymous browser-link samples and the number whose selected ICE candidate
// pair used TURN. The aggregate counters carry no connection, peer, session, or host identifier;
// total is zero until a browser has reported a completed media link, in which case the caller renders
// the percentage as unavailable rather than dividing by zero.
func (s *Store) TurnRelayStats(ctx context.Context) (total, relayed int64, err error) {
	total, err = s.Counter(ctx, CounterConnectionsTotal)
	if err != nil {
		return 0, 0, fmt.Errorf("reading total connection samples: %w", err)
	}
	relayed, err = s.Counter(ctx, CounterConnectionsRelayed)
	if err != nil {
		return 0, 0, fmt.Errorf("reading relayed connection samples: %w", err)
	}
	return total, relayed, nil
}
