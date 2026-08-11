package testutil

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

func AdminDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests")
	}
	return databaseURL
}

func NewMigratedPostgres(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	adminDatabaseURL := AdminDatabaseURL(t)
	parsed, err := url.Parse(adminDatabaseURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	databaseName := "emby_auto_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{databaseName}.Sanitize()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	adminPool, err := database.Open(ctx, adminDatabaseURL)
	if err != nil {
		t.Fatalf("open integration database server: %v", err)
	}
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		adminPool.Close()
		t.Fatalf("create integration database: %v", err)
	}
	targetURL := *parsed
	targetURL.Path = "/" + databaseName
	if err := database.NewMigrator().Migrate(ctx, targetURL.String()); err != nil {
		_, _ = adminPool.Exec(ctx, "DROP DATABASE IF EXISTS "+identifier+" WITH (FORCE)")
		adminPool.Close()
		t.Fatalf("migrate integration database: %v", err)
	}
	pool, err := database.Open(ctx, targetURL.String())
	if err != nil {
		_, _ = adminPool.Exec(ctx, "DROP DATABASE IF EXISTS "+identifier+" WITH (FORCE)")
		adminPool.Close()
		t.Fatalf("open migrated integration database: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		_, _ = adminPool.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+identifier+" WITH (FORCE)")
		adminPool.Close()
	})
	return targetURL.String(), pool
}
