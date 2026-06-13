// Package store is GuestPass's SQLite persistence layer (modernc.org/sqlite, pure Go,
// no CGO). It owns the connection contract (EN-11), the embedded forward-only migration
// runner (AD-6), and the durable-entity repositories (hosts, streams, passes, slots).
//
// Connection contract (EN-11): every pooled connection is opened with journal_mode=WAL,
// busy_timeout>=5000ms, and foreign_keys=ON via a connection hook (so it runs on every
// conn, not just the first). Writes go through a single-connection writer pool
// (SetMaxOpenConns(1)); reads use a separate pool (WAL permits concurrent readers), so
// reads never contend with the single writer.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"sync"

	"modernc.org/sqlite"
)

// ErrNotFound is returned by repository reads when no row matches. Callers branch on it
// with errors.Is and never see the underlying SQLite/driver error (CONVENTIONS §1.2).
var ErrNotFound = errors.New("store: not found")

// busyTimeoutMS is the per-connection SQLite busy timeout (EN-11 requires >= 5000).
const busyTimeoutMS = 5000

// connContract holds the PRAGMAs applied to every pooled connection (EN-11).
var connContract = []string{
	"PRAGMA journal_mode=WAL",
	fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMS),
	"PRAGMA foreign_keys=ON",
}

// registerHookOnce guards the process-global modernc connection-hook registration.
var registerHookOnce sync.Once

// registerConnHook installs a connection hook (once) that applies the EN-11 connection
// contract to every connection modernc opens, for both the writer and reader pools.
func registerConnHook() {
	registerHookOnce.Do(func() {
		sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
			for _, pragma := range connContract {
				if _, err := conn.ExecContext(context.Background(), pragma, nil); err != nil {
					return fmt.Errorf("applying %q: %w", pragma, err)
				}
			}
			return nil
		})
	})
}

// Store holds the writer and reader connection pools over one SQLite file (EN-11).
type Store struct {
	writer *sql.DB // single connection (SetMaxOpenConns(1)); all writes serialize here
	reader *sql.DB // concurrent readers (WAL)
	path   string
}

// Open opens (creating if absent) the SQLite database at path, applies the EN-11
// connection contract to every pooled connection, and runs embedded migrations forward
// to the latest version. It fails closed on migration checksum drift, a dirty/partial
// migration state, or a binary older than the DB (AD-6).
func Open(ctx context.Context, path string) (*Store, error) {
	registerConnHook()

	writer, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening writer pool: %w", err)
	}
	// Single writer: WAL serializes writers anyway, and one connection makes that
	// explicit and avoids SQLITE_BUSY churn between competing writers (EN-11 / RF-11).
	writer.SetMaxOpenConns(1)
	if err := writer.PingContext(ctx); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("connecting writer pool: %w", err)
	}

	if err := runMigrations(ctx, writer); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	reader, err := sql.Open("sqlite", path)
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("opening reader pool: %w", err)
	}
	reader.SetMaxOpenConns(max(4, runtime.NumCPU()))
	if err := reader.PingContext(ctx); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		return nil, fmt.Errorf("connecting reader pool: %w", err)
	}

	return &Store{writer: writer, reader: reader, path: path}, nil
}

// Close closes both connection pools.
func (s *Store) Close() error {
	return errors.Join(s.writer.Close(), s.reader.Close())
}
