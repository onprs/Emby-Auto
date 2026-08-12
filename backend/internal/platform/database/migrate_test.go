package database

import (
	"strings"
	"testing"
)

func TestBundledApplicationMigrationsContainScrapingProxyRemoval(t *testing.T) {
	migrations, err := loadApplicationMigrations()
	if err != nil {
		t.Fatalf("loadApplicationMigrations() error = %v", err)
	}
	if len(migrations) < 37 {
		t.Fatalf("migration count = %d, want at least 37", len(migrations))
	}
	removal := migrations[36]
	if removal.version != 37 || removal.name != "000037_remove_emby_scraping_proxy.up.sql" {
		t.Fatalf("migration 37 = %d %q", removal.version, removal.name)
	}
	if !strings.Contains(removal.sql, "state.active") || !strings.Contains(removal.sql, "operation.status IN ('queued', 'running')") {
		t.Fatalf("migration 37 does not guard active retired commands: %q", removal.sql)
	}
	if !strings.Contains(removal.sql, "DROP TABLE IF EXISTS emby_scraping_proxy_state") {
		t.Fatalf("migration 37 does not remove the retired state table: %q", removal.sql)
	}
}
