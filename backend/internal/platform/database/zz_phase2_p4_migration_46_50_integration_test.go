//go:build integration

package database_test

// P4：真实 46→47 迁移：先 down 到 46，保留 46 数据，再 Migrate 到 47；
// 验证 schema_migrations、绑定表、pre47 数据保留，以及 operation FK ON DELETE
// CASCADE 在真实库生效。

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestZZR1P4Migration46To50PreservesDataAndCascade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	databaseURL, pool := testutil.NewMigratedPostgres(t)
	downgradeApplication(t, ctx, pool, 46)
	var version int64
	if err := pool.QueryRow(ctx, `SELECT version FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read pre-47 migration version: %v", err)
	}
	if version != 46 {
		t.Fatalf("pre-47 migration version = %d, want 46", version)
	}
	var bindingExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.operation_subtitle_bindings') IS NOT NULL`).Scan(&bindingExists); err != nil {
		t.Fatalf("check pre-47 binding table: %v", err)
	}
	if bindingExists {
		t.Fatal("binding table exists before 47 migration")
	}
	seriesID, operationID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'pre-47 migration fixture')`, seriesID); err != nil {
		t.Fatalf("insert pre-47 series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, attempt_count, timeout_seconds, payload)
VALUES ($1, 'subtitle.prepare', 'episode_task', $2, $3, 'running', 3, 1, 60, '{}'::jsonb)`,
		operationID, uuid.New(), "p4-pre47-"+operationID.String()); err != nil {
		t.Fatalf("insert pre-47 operation: %v", err)
	}
	if err := database.NewMigrator().Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("migrate 46 to 47: %v", err)
	}
	if err := database.RequireCurrentMigrations(ctx, pool); err != nil {
		t.Fatalf("require current migrations after 46 to 47: %v", err)
	}
	var title string
	if err := pool.QueryRow(ctx, `SELECT title FROM media_series WHERE id = $1`, seriesID).Scan(&title); err != nil {
		t.Fatalf("read pre-47 series after migration: %v", err)
	}
	if title != "pre-47 migration fixture" {
		t.Fatalf("pre-47 series title = %q", title)
	}
	var operationStillThere bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM operations WHERE id = $1)`, operationID).Scan(&operationStillThere); err != nil {
		t.Fatalf("read pre-47 operation after migration: %v", err)
	}
	if !operationStillThere {
		t.Fatal("pre-47 operation was not preserved by 47 migration")
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.operation_subtitle_bindings') IS NOT NULL`).Scan(&bindingExists); err != nil {
		t.Fatalf("check post-47 binding table: %v", err)
	}
	if !bindingExists {
		t.Fatal("47 migration did not create binding table")
	}
	attemptID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO operation_subtitle_bindings (operation_id, binding_version, phase, attempt, attempt_id, operation_attempt_count, binding)
VALUES ($1, 1, 'started', 1, $2, 1, '{"version":1}'::jsonb)`, operationID, attemptID); err != nil {
		t.Fatalf("insert binding after migration: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM operations WHERE id = $1`, operationID); err != nil {
		t.Fatalf("delete operation for cascade: %v", err)
	}
	var bindings int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_subtitle_bindings WHERE operation_id = $1`, operationID).Scan(&bindings); err != nil {
		t.Fatalf("count binding after operation delete: %v", err)
	}
	if bindings != 0 {
		t.Fatalf("binding rows after operation delete = %d, want 0 (FK cascade)", bindings)
	}
}
