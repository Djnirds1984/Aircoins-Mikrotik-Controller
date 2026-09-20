package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/storage"
)

// testEnv is a migrated database plus the repositories under test.
type testEnv struct {
	routers *Routers
	admins  *Admins
	db      *sql.DB
	ctx     context.Context
}

// newTestEnv opens a migrated database in a temporary directory.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := storage.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	return &testEnv{
		routers: NewRouters(db, key),
		admins:  NewAdmins(db),
		db:      db,
		ctx:     ctx,
	}
}
