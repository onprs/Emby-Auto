//go:build integration

package database_test

// P4：完整应用+River DB 的旧 checker/dump-restore 控制。需要 P4_PG_CID、
// P4_OLD_CHECKER、P4_NEW_CHECKER；只操作维护者显式传入的 owned 容器/回环。

import (
	"context"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestZZR1P4FullRiverRestoreOldChecker(t *testing.T) {
	cid := strings.TrimSpace(os.Getenv("P4_PG_CID"))
	oldChecker := strings.TrimSpace(os.Getenv("P4_OLD_CHECKER"))
	newChecker := strings.TrimSpace(os.Getenv("P4_NEW_CHECKER"))
	if cid == "" || oldChecker == "" || newChecker == "" {
		t.Skip("P4_PG_CID/P4_OLD_CHECKER/P4_NEW_CHECKER required")
	}
	if decoded, err := hex.DecodeString(cid); err != nil || len(decoded) != 32 {
		t.Fatalf("P4_PG_CID must be a full 64-hex container id")
	}
	for _, path := range []string{oldChecker, newChecker} {
		if _, err := os.Stat(path); err != nil {
			t.Skipf("checker binary missing: %s", path)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	databaseURL, pool := testutil.NewMigratedPostgres(t)
	host := pool.Config().ConnConfig.Host
	if host != "127.0.0.1" && host != "::1" {
		t.Fatalf("test database host = %q, want loopback", host)
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	sourceDB := pool.Config().ConnConfig.Database
	targetDB := "p4_restore_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	targetURL := *parsed
	targetURL.Path = "/" + targetDB
	t.Logf("restore_control cid=%s host=%s port=%d source_db=%s target_db=%s", cid, host, pool.Config().ConnConfig.Port, sourceDB, targetDB)
	run := func(parent context.Context, timeout time.Duration, name string, args ...string) (string, error) {
		commandCtx, commandCancel := context.WithTimeout(parent, timeout)
		defer commandCancel()
		command := exec.CommandContext(commandCtx, name, args...)
		output, commandErr := command.CombinedOutput()
		return string(output), commandErr
	}
	if output, err := run(ctx, 8*time.Second, "docker", "exec", cid, "psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-c", "CREATE DATABASE "+targetDB); err != nil {
		t.Fatalf("create restore DB: %v: %s", err, output)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cleanupCancel()
		output, err := run(cleanupCtx, 3*time.Second, "docker", "exec", cid, "psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-c", "DROP DATABASE "+targetDB+" WITH (FORCE)")
		if err != nil {
			t.Errorf("drop restore DB %s: %v: %s", targetDB, err, output)
		}
	}()
	downgradeApplication(t, ctx, pool, 46)
	seriesID, operationID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO media_series (id, title) VALUES ($1, 'pre47 restore fixture')`, seriesID); err != nil {
		t.Fatalf("insert pre47 series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, attempt_count, timeout_seconds, payload)
VALUES ($1, 'subtitle.prepare', 'episode_task', $2, $3, 'running', 3, 1, 60, '{}'::jsonb)`,
		operationID, uuid.New(), "p4-restore-"+operationID.String()); err != nil {
		t.Fatalf("insert pre47 operation: %v", err)
	}
	var bindingAt46 bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.operation_subtitle_bindings') IS NOT NULL`).Scan(&bindingAt46); err != nil {
		t.Fatalf("check binding at 46: %v", err)
	}
	if bindingAt46 {
		t.Fatal("binding table exists at pre47 source")
	}
	runChecker := func(path, databaseURL string) (string, error) {
		commandCtx, commandCancel := context.WithTimeout(ctx, 15*time.Second)
		defer commandCancel()
		command := exec.CommandContext(commandCtx, path)
		command.Env = append(os.Environ(), "DATABASE_URL="+databaseURL)
		output, commandErr := command.CombinedOutput()
		return string(output), commandErr
	}
	oldSourceOutput, oldSourceErr := runChecker(oldChecker, databaseURL)
	if oldSourceErr != nil || !strings.Contains(oldSourceOutput, "application: 46/46") || !strings.Contains(oldSourceOutput, "river: 8/8") {
		t.Fatalf("old checker at source 46/River8 = err=%v output=%s", oldSourceErr, oldSourceOutput)
	}
	dumpPath := filepath.Join(t.TempDir(), "pre47.dump")
	dumpFile, err := os.Create(dumpPath)
	if err != nil {
		t.Fatalf("create dump file: %v", err)
	}
	dumpCtx, dumpCancel := context.WithTimeout(ctx, 15*time.Second)
	dumpCommand := exec.CommandContext(dumpCtx, "docker", "exec", cid, "pg_dump", "-U", "postgres", "-Fc", "-d", sourceDB)
	dumpCommand.Stdout = dumpFile
	dumpErr := dumpCommand.Run()
	dumpCancel()
	closeErr := dumpFile.Close()
	if dumpErr != nil || closeErr != nil {
		t.Fatalf("pg_dump: err=%v close=%v", dumpErr, closeErr)
	}
	info, err := os.Stat(dumpPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("dump artifact empty: info=%v err=%v", info, err)
	}
	dumpInput, err := os.Open(dumpPath)
	if err != nil {
		t.Fatalf("open dump artifact: %v", err)
	}
	restoreCtx, restoreCancel := context.WithTimeout(ctx, 15*time.Second)
	restoreCommand := exec.CommandContext(restoreCtx, "docker", "exec", "-i", cid, "pg_restore", "-U", "postgres", "-d", targetDB, "--no-owner", "--no-privileges", "--exit-on-error")
	restoreCommand.Stdin = dumpInput
	restoreOutput, restoreErr := restoreCommand.CombinedOutput()
	restoreCancel()
	_ = dumpInput.Close()
	if restoreErr != nil {
		t.Fatalf("pg_restore: %v: %s", restoreErr, restoreOutput)
	}
	restoredPool, err := database.Open(ctx, targetURL.String())
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	var restoredTitle string
	var restoredOperation bool
	var restoredBinding bool
	if err := restoredPool.QueryRow(ctx, `SELECT title FROM media_series WHERE id = $1`, seriesID).Scan(&restoredTitle); err != nil {
		restoredPool.Close()
		t.Fatalf("read restored series: %v", err)
	}
	if err := restoredPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM operations WHERE id = $1)`, operationID).Scan(&restoredOperation); err != nil {
		restoredPool.Close()
		t.Fatalf("read restored operation: %v", err)
	}
	if err := restoredPool.QueryRow(ctx, `SELECT to_regclass('public.operation_subtitle_bindings') IS NOT NULL`).Scan(&restoredBinding); err != nil {
		restoredPool.Close()
		t.Fatalf("read restored binding table: %v", err)
	}
	restoredPool.Close()
	if restoredTitle != "pre47 restore fixture" || !restoredOperation || restoredBinding {
		t.Fatalf("restored 46 data = title %q operation %t binding %t, want fixture/true/false", restoredTitle, restoredOperation, restoredBinding)
	}
	oldRestoredOutput, oldRestoredErr := runChecker(oldChecker, targetURL.String())
	if oldRestoredErr != nil || !strings.Contains(oldRestoredOutput, "application: 46/46") || !strings.Contains(oldRestoredOutput, "river: 8/8") {
		t.Fatalf("old checker at restored 46/River8 = err=%v output=%s", oldRestoredErr, oldRestoredOutput)
	}
	if err := database.NewMigrator().Migrate(ctx, targetURL.String()); err != nil {
		t.Fatalf("NewMigrator restore 46 to 47: %v", err)
	}
	migratedPool, err := database.Open(ctx, targetURL.String())
	if err != nil {
		t.Fatalf("open migrated restored database: %v", err)
	}
	var migratedTitle string
	var migratedOperation bool
	var migratedBinding bool
	if err := migratedPool.QueryRow(ctx, `SELECT title FROM media_series WHERE id = $1`, seriesID).Scan(&migratedTitle); err != nil {
		migratedPool.Close()
		t.Fatalf("read migrated series: %v", err)
	}
	if err := migratedPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM operations WHERE id = $1)`, operationID).Scan(&migratedOperation); err != nil {
		migratedPool.Close()
		t.Fatalf("read migrated operation: %v", err)
	}
	if err := migratedPool.QueryRow(ctx, `SELECT to_regclass('public.operation_subtitle_bindings') IS NOT NULL`).Scan(&migratedBinding); err != nil {
		migratedPool.Close()
		t.Fatalf("read migrated binding table: %v", err)
	}
	migratedPool.Close()
	if migratedTitle != "pre47 restore fixture" || !migratedOperation || !migratedBinding {
		t.Fatalf("migrated 47 data = title %q operation %t binding %t, want fixture/true/true", migratedTitle, migratedOperation, migratedBinding)
	}
	newOutput, newErr := runChecker(newChecker, targetURL.String())
	if newErr != nil || !strings.Contains(newOutput, "application: 50/50") || !strings.Contains(newOutput, "river: 8/8") {
		t.Fatalf("new checker at migrated binding migration/River8 = err=%v output=%s", newErr, newOutput)
	}
	oldRejectOutput, oldRejectErr := runChecker(oldChecker, targetURL.String())
	var exitErr *exec.ExitError
	if !errors.As(oldRejectErr, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("old checker after 47 = err=%v output=%s, want explicit ExitCode=1", oldRejectErr, oldRejectOutput)
	}
	if !strings.Contains(oldRejectOutput, "application: 50/46") || !strings.Contains(oldRejectOutput, "river: 8/8") || !strings.Contains(oldRejectOutput, "application migrations are behind") {
		t.Fatalf("old checker rejection output = %s, want app 50/46 + River 8/8 + real migration rejection", oldRejectOutput)
	}
	t.Logf("restore_control_ok source_db=%s target_db=%s dump_bytes=%d old46=%s new47=%s old47_reject=%s", sourceDB, targetDB, info.Size(), strings.ReplaceAll(strings.TrimSpace(oldRestoredOutput), "\n", ";"), strings.ReplaceAll(strings.TrimSpace(newOutput), "\n", ";"), strings.ReplaceAll(strings.TrimSpace(oldRejectOutput), "\n", ";"))
}
