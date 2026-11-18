//go:build integration

package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestZZR1OperationSubtitleBindingMigrationDownUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	up, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", "000050_operation_subtitle_bindings.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", "000050_operation_subtitle_bindings.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	tableExists := func() bool {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.operation_subtitle_bindings') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		return exists
	}
	if !tableExists() {
		t.Fatal("migrated schema is missing operation_subtitle_bindings")
	}
	if _, err := testutil.ExecFixture(ctx, pool, string(down)); err != nil {
		t.Fatalf("apply official down: %v", err)
	}
	if tableExists() {
		t.Fatal("official down did not remove operation_subtitle_bindings")
	}
	if _, err := testutil.ExecFixture(ctx, pool, string(up)); err != nil {
		t.Fatalf("apply official up after down: %v", err)
	}
	if !tableExists() {
		t.Fatal("official up did not recreate operation_subtitle_bindings")
	}
	var primaryKey bool
	if err := pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'operation_subtitle_bindings'::regclass AND contype = 'p'
)`).Scan(&primaryKey); err != nil {
		t.Fatal(err)
	}
	if !primaryKey {
		t.Fatal("recreated binding table has no primary key")
	}
}
