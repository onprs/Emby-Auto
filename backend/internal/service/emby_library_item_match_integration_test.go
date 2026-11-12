//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

// TestEmbyLibraryItemTMDbProviderIDIsNormalizedIntegration 验证媒体库条目的
// TMDb 标识生成列：大小写不同的 ProviderIds 键都应归一化为可索引的列值，
// 并在 provider_ids 更新后自动重算。
func TestEmbyLibraryItemTMDbProviderIDIsNormalizedIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	actorID, operationID, scanID, libraryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "tmdb-provider-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds, started_at)
VALUES ($1, 'emby.scan', 'emby_scan', $2, $3, 'running', 3, 60, now())
`, operationID, scanID, "tmdb-provider-"+operationID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO emby_scan_runs (id, operation_id, status, created_by)
VALUES ($1, $2, 'running', $3)
`, scanID, operationID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO emby_libraries (id, emby_id, name, last_scan_run_id, last_seen_at)
VALUES ($1, $2, 'Anime', $3, now())
`, libraryID, "library-"+libraryID.String(), scanID); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		embyID      string
		providerIDs string
		want        string
	}{
		{embyID: "episode-tmdb-" + uuid.NewString(), providerIDs: `{"Tmdb":"42"}`, want: "42"},
		{embyID: "episode-moviedb-" + uuid.NewString(), providerIDs: `{"TheMovieDb":"43"}`, want: "43"},
		{embyID: "episode-lower-" + uuid.NewString(), providerIDs: `{"tmdb":" 44 "}`, want: "44"},
		{embyID: "episode-imdb-" + uuid.NewString(), providerIDs: `{"Imdb":"tt45"}`, want: ""},
	}
	for _, test := range cases {
		if _, err := pool.Exec(ctx, `
INSERT INTO emby_library_items (
    id, emby_id, library_id, item_type, name, provider_ids,
    season_number, episode_number, last_scan_run_id, last_seen_at
) VALUES ($1, $2, $3, 'Episode', 'Episode', $4::jsonb, 1, 1, $5, now())
`, uuid.New(), test.embyID, libraryID, test.providerIDs, scanID); err != nil {
			t.Fatal(err)
		}
		var got string
		if err := pool.QueryRow(ctx, `SELECT tmdb_provider_id FROM emby_library_items WHERE emby_id = $1`, test.embyID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Fatalf("provider_ids=%s tmdb_provider_id=%q, want %q", test.providerIDs, got, test.want)
		}
	}
	updatedEmbyID := "episode-updated-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
INSERT INTO emby_library_items (
    id, emby_id, library_id, item_type, name, provider_ids,
    season_number, episode_number, last_scan_run_id, last_seen_at
) VALUES ($1, $2, $3, 'Episode', 'Episode', '{"Tmdb":"46"}'::jsonb, 1, 1, $4, now())
`, uuid.New(), updatedEmbyID, libraryID, scanID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE emby_library_items SET provider_ids = '{"TheMovieDb":"47"}'::jsonb WHERE emby_id = $1`, updatedEmbyID); err != nil {
		t.Fatal(err)
	}
	var updated string
	if err := pool.QueryRow(ctx, `SELECT tmdb_provider_id FROM emby_library_items WHERE emby_id = $1`, updatedEmbyID).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated != "47" {
		t.Fatalf("provider_ids 更新后 tmdb_provider_id=%q, want 47", updated)
	}
}
