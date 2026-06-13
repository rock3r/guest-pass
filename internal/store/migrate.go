package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Migration-runner contract (AD-6 / CONVENTIONS §2.3 / RF-12). The runner is
// hand-rolled and embedded — zero external migration deps. Numbered *.sql files are
// applied forward-only, each in its own all-or-nothing transaction; a schema_version
// table records each applied version + a per-file checksum + applied_at. The runner
// refuses to start on checksum drift of an already-applied file, a dirty/partial
// (non-contiguous) state, or a binary older than the DB (an applied version with no
// embedded file).

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration-runner sentinel errors (callers branch with errors.Is).
var (
	// ErrMigrationDrift means an already-applied file's checksum no longer matches the
	// embedded file (a migration was edited after being applied).
	ErrMigrationDrift = errors.New("store: migration checksum drift")
	// ErrMigrationState means the recorded migration state is dirty/partial
	// (non-contiguous versions) or the binary is older than the DB (an applied version
	// has no embedded file).
	ErrMigrationState = errors.New("store: invalid migration state")
)

const createSchemaVersionTable = `CREATE TABLE IF NOT EXISTS schema_version (
    version    INTEGER PRIMARY KEY,
    checksum   TEXT    NOT NULL,
    applied_at INTEGER NOT NULL
) STRICT;`

type migration struct {
	version  int
	name     string // file name, e.g. 0001_init.sql
	sql      string
	checksum string // sha256 hex of the file bytes
}

// loadMigrations reads the embedded migrations, sorted ascending by version, and
// verifies they form a contiguous sequence 1..N (a gap is a packaging bug).
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("reading embedded migrations: %w", err)
	}
	var migs []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := parseVersion(e.Name())
		if err != nil {
			return nil, err
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("reading migration %s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(body)
		migs = append(migs, migration{
			version:  version,
			name:     e.Name(),
			sql:      string(body),
			checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	for i, m := range migs {
		if m.version != i+1 {
			return nil, fmt.Errorf("embedded migrations are not contiguous at %s (expected version %d): %w", m.name, i+1, ErrMigrationState)
		}
	}
	return migs, nil
}

// parseVersion extracts the leading integer from a migration file name (e.g. 0001 from
// "0001_init.sql").
func parseVersion(name string) (int, error) {
	base, _, found := strings.Cut(name, "_")
	if !found {
		return 0, fmt.Errorf("migration %q is not in NNNN_name.sql form: %w", name, ErrMigrationState)
	}
	v, err := strconv.Atoi(base)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("migration %q has a non-positive-integer version: %w", name, ErrMigrationState)
	}
	return v, nil
}

// runMigrations brings the database forward to the latest embedded migration, applying
// each pending file in its own transaction. It is idempotent: an up-to-date DB is a
// no-op. It fails closed on drift, dirty state, or a binary older than the DB (AD-6).
func runMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, createSchemaVersionTable); err != nil {
		return fmt.Errorf("creating schema_version: %w", err)
	}
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	applied, err := loadApplied(ctx, db)
	if err != nil {
		return err
	}
	if err := checkMigrationState(migs, applied); err != nil {
		return err
	}

	// Apply pending migrations in order, each in its own transaction.
	for _, m := range migs {
		if _, ok := applied[m.version]; ok {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return fmt.Errorf("applying migration %d (%s): %w", m.version, m.name, err)
		}
	}
	return nil
}

// checkMigrationState validates the recorded migration state against the embedded
// migrations (a pure function, unit-testable without a DB). It enforces the three
// fail-closed guards (RF-12 / AD-6): drift (an applied checksum no longer matches),
// older-binary (an applied version has no embedded file), and dirty/partial
// (applied versions are not a contiguous prefix 1..k). migs must already be the
// contiguous, ascending list returned by loadMigrations.
func checkMigrationState(migs []migration, applied map[int]string) error {
	byVersion := make(map[int]migration, len(migs))
	for _, m := range migs {
		byVersion[m.version] = m
	}
	for v, cs := range applied {
		m, ok := byVersion[v]
		if !ok {
			return fmt.Errorf("applied migration %d has no embedded file (binary older than DB): %w", v, ErrMigrationState)
		}
		if m.checksum != cs {
			return fmt.Errorf("migration %d (%s) was modified after being applied: %w", v, m.name, ErrMigrationDrift)
		}
	}
	versions := make([]int, 0, len(applied))
	for v := range applied {
		versions = append(versions, v)
	}
	sort.Ints(versions)
	for i, v := range versions {
		if v != i+1 {
			return fmt.Errorf("recorded migrations are non-contiguous (dirty/partial state) at version %d: %w", v, ErrMigrationState)
		}
	}
	return nil
}

// loadApplied returns the recorded version→checksum map from schema_version.
func loadApplied(ctx context.Context, db *sql.DB) (map[int]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT version, checksum FROM schema_version")
	if err != nil {
		return nil, fmt.Errorf("reading schema_version: %w", err)
	}
	defer rows.Close()
	applied := map[int]string{}
	for rows.Next() {
		var v int
		var cs string
		if err := rows.Scan(&v, &cs); err != nil {
			return nil, fmt.Errorf("scanning schema_version: %w", err)
		}
		applied[v] = cs
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating schema_version: %w", err)
	}
	return applied, nil
}

// applyMigration runs one migration file and records its version+checksum in the SAME
// transaction, so a crash leaves the DB at the last fully-applied migration — never a
// half-applied file (AD-6).
func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_version (version, checksum, applied_at) VALUES (?, ?, ?)",
		m.version, m.checksum, time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("recording version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
