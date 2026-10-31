//go:build integration

package database_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestCoreSchemaConstraintsIntegration(t *testing.T) {
	_, pool := testutil.NewMigratedPostgres(t)
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "test", "core_schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	statements := strings.TrimPrefix(string(contents), "\\set ON_ERROR_STOP on")
	if _, err := pool.Exec(t.Context(), statements); err != nil {
		t.Fatalf("核心数据库约束不满足: %v", err)
	}
}
