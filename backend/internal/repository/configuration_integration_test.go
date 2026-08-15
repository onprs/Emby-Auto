//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestSaveAppSettingCreatesUpdatesAndRejectsStaleVersionIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)

	created, err := queries.SaveAppSetting(ctx, db.SaveAppSettingParams{
		Name: "runtime", Value: []byte(`{"value":"first"}`), ExpectedVersion: 0,
	})
	if err != nil {
		t.Fatalf("create SaveAppSetting() error = %v", err)
	}
	if created.Version != 1 {
		t.Fatalf("created version = %d, want 1", created.Version)
	}
	updated, err := queries.SaveAppSetting(ctx, db.SaveAppSettingParams{
		Name: "runtime", Value: []byte(`{"value":"second"}`), ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatalf("update SaveAppSetting() error = %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(updated.Value, &decoded); err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || decoded["value"] != "second" {
		t.Fatalf("updated setting = version %d value %s", updated.Version, updated.Value)
	}
	_, err = queries.SaveAppSetting(ctx, db.SaveAppSettingParams{
		Name: "runtime", Value: []byte(`{"value":"stale"}`), ExpectedVersion: 1,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale SaveAppSetting() error = %v, want pgx.ErrNoRows", err)
	}
}

func TestConfigurationLoadDefaultsEventRetentionWhenAbsentIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	configuration := NewConfiguration(queries, transactor)

	// 旧版本配置没有 events 键：加载后应使用默认 30 天保留期。
	if _, err := queries.SaveAppSetting(ctx, db.SaveAppSettingParams{
		Name: "runtime", Value: []byte(`{"qBittorrent":{}}`), ExpectedVersion: 0,
	}); err != nil {
		t.Fatalf("SaveAppSetting() error = %v", err)
	}
	loaded, err := configuration.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Settings.Events.RetentionDays != domain.DefaultEventsRetentionDays {
		t.Fatalf("default retention days = %d, want %d", loaded.Settings.Events.RetentionDays, domain.DefaultEventsRetentionDays)
	}

	// 显式 0 表示禁用定期清理，加载后必须保留。
	if _, err := queries.SaveAppSetting(ctx, db.SaveAppSettingParams{
		Name: "runtime", Value: []byte(`{"events":{"retentionDays":0}}`), ExpectedVersion: 1,
	}); err != nil {
		t.Fatalf("SaveAppSetting() error = %v", err)
	}
	loaded, err = configuration.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Settings.Events.RetentionDays != 0 {
		t.Fatalf("explicit disabled retention days = %d, want 0", loaded.Settings.Events.RetentionDays)
	}
}
