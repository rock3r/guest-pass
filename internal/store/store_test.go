package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// openTestStore opens a Store backed by a fresh temp-file database (not in-memory, so
// WAL applies) and registers cleanup.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestOpen_AppliesConnectionContract asserts the EN-11 PRAGMAs are present on conns
// from BOTH pools (the connection hook runs on every pooled conn, not just the first).
func TestOpen_AppliesConnectionContract(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	want := map[string]string{
		"journal_mode": "wal",
		"foreign_keys": "1",
		"busy_timeout": "5000",
	}
	check := func(label string, q func(string) (string, error)) {
		for pragma, exp := range want {
			got, err := q("PRAGMA " + pragma)
			if err != nil {
				t.Fatalf("%s PRAGMA %s: %v", label, pragma, err)
			}
			if got != exp {
				t.Errorf("%s PRAGMA %s = %q, want %q", label, pragma, got, exp)
			}
		}
	}
	check("writer", func(q string) (string, error) {
		var v string
		err := st.writer.QueryRowContext(ctx, q).Scan(&v)
		return v, err
	})
	check("reader", func(q string) (string, error) {
		var v string
		err := st.reader.QueryRowContext(ctx, q).Scan(&v)
		return v, err
	})
}

// TestOpen_WriterIsSingleConnection asserts the writer pool caps at one connection (EN-11).
func TestOpen_WriterIsSingleConnection(t *testing.T) {
	st := openTestStore(t)
	if got := st.writer.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer MaxOpenConnections = %d, want 1", got)
	}
}

// TestWriterSerializesConcurrentWrites is the connect-storm test (RF-11): many
// goroutines write concurrently through the single-connection writer pool; every write
// must land (none lost or dropped to SQLITE_BUSY), proving the single writer serializes
// rather than contends.
func TestWriterSerializesConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.writer.ExecContext(ctx,
				"INSERT INTO hosts (id, google_sub, email, name, created_at) VALUES (?, ?, ?, ?, ?)",
				fmt.Sprintf("host-%d", i), fmt.Sprintf("sub-%d", i), "e@example.com", "n", 0)
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}
	var count int
	if err := st.reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM hosts").Scan(&count); err != nil {
		t.Fatalf("counting hosts: %v", err)
	}
	if count != n {
		t.Fatalf("host count = %d, want %d (writes were lost)", count, n)
	}
}
