// Package storage opens and migrates the SQLite database.
//
// SQLite is opened through the pure-Go modernc.org/sqlite driver so the whole
// controller cross-compiles to linux/arm64, linux/arm/v7 and linux/amd64 with
// CGO_ENABLED=0. The pool is deliberately limited to a single connection:
// SQLite allows one writer, and serialising access avoids "database is locked"
// errors on slow SBC storage.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open opens (creating if needed) the SQLite database at path.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create data directory %s: %w", dir, err)
		}
	}

	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}

	// Single writer: SQLite permits one writer at a time and the panel is
	// low-throughput, so serialising here is both correct and cheap.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database %s: %w", path, err)
	}

	// Belt and braces: re-assert the per-connection pragmas so behaviour does
	// not depend on DSN parsing by the driver.
	for _, pragma := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply %s: %w", pragma, err)
		}
	}

	return db, nil
}
