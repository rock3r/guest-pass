package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// mig is a test-only constructor for a migration with just the fields checkMigrationState reads.
func mig(version int, checksum string) migration {
	return migration{version: version, name: "test", checksum: checksum}
}

func TestCheckMigrationState(t *testing.T) {
	migs := []migration{mig(1, "cs1"), mig(2, "cs2"), mig(3, "cs3")}
	cases := []struct {
		name    string
		applied map[int]string
		wantErr error // nil => ok
	}{
		{"none applied", map[int]string{}, nil},
		{"contiguous prefix", map[int]string{1: "cs1"}, nil},
		{"all applied", map[int]string{1: "cs1", 2: "cs2", 3: "cs3"}, nil},
		{"checksum drift", map[int]string{1: "cs1", 2: "WRONG"}, ErrMigrationDrift},
		{"older binary (applied has no embedded file)", map[int]string{1: "cs1", 2: "cs2", 3: "cs3", 4: "cs4"}, ErrMigrationState},
		{"non-contiguous: missing v1", map[int]string{2: "cs2"}, ErrMigrationState},
		{"non-contiguous: gap at v2", map[int]string{1: "cs1", 3: "cs3"}, ErrMigrationState},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkMigrationState(migs, tc.applied)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected ok, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadMigrations_EmbeddedContiguous(t *testing.T) {
	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migs) == 0 {
		t.Fatal("no embedded migrations found")
	}
	for i, m := range migs {
		if m.version != i+1 {
			t.Fatalf("migration %d has version %d, want %d", i, m.version, i+1)
		}
		if m.checksum == "" || m.sql == "" {
			t.Fatalf("migration %d (%s) missing checksum/sql", m.version, m.name)
		}
	}
}

func TestRunMigrations_FreshAndIdempotent(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	// Fresh Open already ran migrations; schema_version should hold version 1.
	var maxVersion int
	if err := st.reader.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_version").Scan(&maxVersion); err != nil {
		t.Fatalf("reading schema_version: %v", err)
	}
	if maxVersion != 1 {
		t.Fatalf("schema_version max = %d, want 1", maxVersion)
	}
	// Core tables exist.
	for _, table := range []string{"hosts", "streams", "slots", "passes", "sessions", "peers", "pass_locks", "host_source_tokens"} {
		var name string
		err := st.reader.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
	// Re-running is a no-op (idempotent) and records no duplicate version row.
	if err := runMigrations(ctx, st.writer); err != nil {
		t.Fatalf("second runMigrations: %v", err)
	}
	var count int
	if err := st.reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_version").Scan(&count); err != nil {
		t.Fatalf("counting schema_version: %v", err)
	}
	if count != 1 {
		t.Fatalf("schema_version row count = %d, want 1", count)
	}
}

func TestRunMigrations_RefusesChecksumDrift(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if _, err := st.writer.ExecContext(ctx, "UPDATE schema_version SET checksum='tampered' WHERE version=1"); err != nil {
		t.Fatalf("tampering checksum: %v", err)
	}
	err := runMigrations(ctx, st.writer)
	if !errors.Is(err, ErrMigrationDrift) {
		t.Fatalf("expected ErrMigrationDrift, got %v", err)
	}
}

func TestRunMigrations_RefusesOlderBinary(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	// A version recorded that this binary has no embedded file for = binary older than DB.
	if _, err := st.writer.ExecContext(ctx, "INSERT INTO schema_version (version, checksum, applied_at) VALUES (99, 'x', 0)"); err != nil {
		t.Fatalf("inserting future version: %v", err)
	}
	err := runMigrations(ctx, st.writer)
	if !errors.Is(err, ErrMigrationState) {
		t.Fatalf("expected ErrMigrationState, got %v", err)
	}
}

func TestOpen_RefusesDriftOnReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "drift.db")

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := st.writer.ExecContext(ctx, "UPDATE schema_version SET checksum='tampered' WHERE version=1"); err != nil {
		t.Fatalf("tampering: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err = Open(ctx, path)
	if !errors.Is(err, ErrMigrationDrift) {
		t.Fatalf("expected Open to fail closed with ErrMigrationDrift, got %v", err)
	}
}
