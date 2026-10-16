//go:build integration

package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

type stoppedMaintenanceExecutor struct{}

func (stoppedMaintenanceExecutor) Run(_ context.Context, _ string, command string) ([]byte, error) {
	if command != "worker-status" {
		return nil, errors.New("配置迁移不得启动或停止 Worker")
	}
	return []byte("stopped\n"), nil
}

type emptyMaintenanceTorrents struct{}

func (emptyMaintenanceTorrents) Login(context.Context) error { return nil }
func (emptyMaintenanceTorrents) ListTorrents(context.Context, string) ([]qbittorrent.Torrent, error) {
	return nil, nil
}
func (emptyMaintenanceTorrents) AddTorrentTags(context.Context, []string, ...string) error {
	return errors.New("配置迁移不得修改 torrent")
}
func (emptyMaintenanceTorrents) RemoveTorrentTags(context.Context, []string, ...string) error {
	return errors.New("配置迁移不得修改 torrent")
}
func (emptyMaintenanceTorrents) StopTorrents(context.Context, []string) error {
	return errors.New("配置迁移不得修改 torrent")
}
func (emptyMaintenanceTorrents) ResumeTorrents(context.Context, []string) error {
	return errors.New("配置迁移不得修改 torrent")
}

func TestRuntimeAddressMigrationPreservesEncryptedSecretsAndRollsBackIntegration(t *testing.T) {
	_, pool := testutil.NewMigratedPostgres(t)
	ctx := context.Background()
	actor := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO admin_users(id,username,password_hash) VALUES($1,$2,'fixture-hash')", actor, "maintenance-"+actor.String()); err != nil {
		t.Fatal(err)
	}
	queries := db.New(pool)
	store := repository.NewConfiguration(queries, database.NewTransactor(pool))
	cipher, err := service.NewSecretCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	configuration := service.NewConfigurationService(store, cipher)
	root := t.TempDir()
	settings := domain.RuntimeSettings{
		QBittorrent:  domain.QBittorrentSettings{URL: "http://host.docker.internal:8080", Username: "maintenance-download"},
		Emby:         domain.EmbySettings{URL: "http://host.docker.internal:8096/emby"},
		NetworkProxy: domain.NetworkProxySettings{Enabled: true, URL: "http://127.0.0.1:17890"},
		Paths: domain.PathSettings{DownloadRoot: filepath.Join(root, "downloads"), WorkRoot: filepath.Join(root, "work"),
			StagingRoot: filepath.Join(root, "staging"), AnimeLibraryRoot: filepath.Join(root, "anime"), MovieLibraryRoot: filepath.Join(root, "movies"),
			FFmpegPath: "/usr/bin/ffmpeg", FFprobePath: "/usr/bin/ffprobe"},
		Transcode: domain.TranscodeProfile{Name: "maintenance-h264", VideoCodec: "h264", Encoder: "libx264", Container: "mp4", FileExtension: "mp4",
			QualityMode: "crf", QualityValue: 20, AudioPolicy: "transcode", AudioCodec: "aac", Preset: "medium", PixelFormat: "yuv420p", ThreadCount: 2, MaxConcurrency: 1},
	}
	prepared, err := configuration.PrepareInitial(settings, domain.SetupSecrets{
		QBittorrentPassword: "fixture-download-password", EmbyAPIKey: "fixture-emby-key", TMDbAPIToken: "fixture-tmdb-key",
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	before, err := configuration.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secretRows := make(map[string][]byte)
	for _, name := range []string{domain.SecretQBittorrentPassword, domain.SecretEmbyAPIKey, domain.SecretTMDbAPIToken} {
		secret, err := queries.GetAppSecret(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		secretRows[name], err = json.Marshal(secret)
		if err != nil {
			t.Fatal(err)
		}
	}
	transfers := service.NewQBittorrentBackgroundTransfers(configuration, func(qbittorrent.ClientOptions) (service.BackgroundTorrentClient, error) {
		return emptyMaintenanceTorrents{}, nil
	})
	scope := &Scope{Pool: pool, Queries: queries, Configuration: configuration, ActorID: actor,
		lock: repository.NewBackgroundRuntimeLock(pool), transfers: transfers,
		controller: service.NewBackgroundRuntimeController("/fixture-control", stoppedMaintenanceExecutor{}, transfers)}
	privateDirectory := t.TempDir()
	if err := os.Chmod(privateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(privateDirectory, "addresses.json")
	preview, err := scope.MigrateAddresses(ctx, "host.docker.internal", "127.0.0.1", backup, "", false, 2)
	if err != nil || preview.Applied || len(preview.Fields) != 2 || preview.Version != before.Version {
		t.Fatalf("预览 = %#v，%v", preview, err)
	}
	applied, err := scope.MigrateAddresses(ctx, "host.docker.internal", "127.0.0.1", backup, "", true, 2)
	if err != nil || !applied.Applied || applied.Version != before.Version+1 {
		t.Fatalf("应用 = %#v，%v", applied, err)
	}
	after, err := configuration.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expected := before.Settings
	expected.QBittorrent.URL = "http://127.0.0.1:8080"
	expected.Emby.URL = "http://127.0.0.1:8096/emby"
	if after.Settings != expected {
		t.Fatal("配置迁移改变了地址以外的设置")
	}
	again, err := scope.MigrateAddresses(ctx, "host.docker.internal", "127.0.0.1", backup, "", true, 2)
	if err != nil || again.Applied || again.Version != after.Version {
		t.Fatalf("地址重放不幂等：%#v，%v", again, err)
	}
	if _, err := scope.MigrateAddresses(ctx, "", "", "", backup, true, 2); err != nil {
		t.Fatal(err)
	}
	restored, err := configuration.Load(ctx)
	if err != nil || restored.Settings != before.Settings {
		t.Fatalf("恢复未保留原配置：%v", err)
	}
	if _, err := scope.MigrateAddresses(ctx, "host.docker.internal", "127.0.0.1", backup, "", true, 2); err == nil {
		t.Fatal("旧版本快照覆盖了后续配置版本")
	}
	for name, expected := range secretRows {
		secret, err := queries.GetAppSecret(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := json.Marshal(secret)
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("地址迁移或恢复改写了密钥记录：%s", name)
		}
	}
	for _, directory := range []string{settings.Paths.DownloadRoot, settings.Paths.WorkRoot, settings.Paths.StagingRoot, settings.Paths.AnimeLibraryRoot, settings.Paths.MovieLibraryRoot} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := scope.MigrateStaging(ctx, 10001, "", "", false, 0); err != nil || result.Tasks != 0 || result.Applied {
		t.Fatalf("空暂存范围预览 = %#v，%v", result, err)
	}
	if _, err := scope.MigrateStaging(ctx, 10001, "", "", false, 1); err == nil {
		t.Fatal("暂存预览忽略了预期任务数量")
	}
	series, acquisition := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO media_series(id,title) VALUES($1,'维护测试剧集')", series); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO acquisitions(id,series_id,source_kind,source_uri) VALUES($1,$2,'manual','magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')", acquisition, series); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO downloads(acquisition_id,status,torrent_hash) VALUES($1,'downloading','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')", acquisition); err != nil {
		t.Fatal(err)
	}
	status, err := scope.Runtime(ctx, nil)
	if err != nil || status.Verified || status.DownloadTorrentHashes != 1 || len(status.Transfers.Selected) != 0 {
		t.Fatalf("未识别丢失的下载引用：%#v，%v", status, err)
	}
	if _, err := scope.MigrateAddresses(ctx, "host.docker.internal", "127.0.0.1", "", "", false, 2); err == nil {
		t.Fatal("无法定位应用下载时仍允许配置迁移")
	}
}
