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
