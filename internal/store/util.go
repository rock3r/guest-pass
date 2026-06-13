package store

import (
	"database/sql"
	"fmt"
)

// boolToInt maps a Go bool to the 0/1 INTEGER SQLite stores (no native bool).
func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// errIfNoRows translates an UPDATE/DELETE that matched no rows into ErrNotFound, so
// callers can distinguish "no such id" from a successful change.
func errIfNoRows(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
