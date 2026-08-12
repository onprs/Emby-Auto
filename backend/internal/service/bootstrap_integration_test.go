//go:build integration

package service

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	platformconfig "github.com/onprs/emby-auto/backend/internal/platform/config"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestBootstrapInitializeMigratesCreatesAdminAndLocksSetupIntegration(t *testing.T) {
	adminDatabaseURL := testutil.AdminDatabaseURL(t)
	parsed, err := url.Parse(adminDatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	databaseName := "emby_auto_setup_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	adminPool, err := database.Open(ctx, adminDatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	identifier := pgx.Identifier{databaseName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = adminPool.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+identifier+" WITH (FORCE)")
	})

	var activation domain.RuntimeBootstrap
	bootstrap := NewBootstrap(BootstrapOptions{
		Store:     platformconfig.NewBootstrapStore(filepath.Join(t.TempDir(), "bootstrap.json")),
		Migrator:  database.NewMigrator(),
		Passwords: NewPasswordHasher(),
		Activate: func(_ context.Context, runtime domain.RuntimeBootstrap) error {
			activation = runtime
			return nil
		},
	})
	input := domain.InitializeSetup{
		Database: &domain.SetupDatabase{
			Host:     parsed.Hostname(),
			Port:     port,
			Database: databaseName,
			Username: parsed.User.Username(),
			Password: password,
			SSLMode:  "disable",
		},
		AdministratorUsername: "setup-admin",
		AdministratorPassword: "fixture-password-123",
		Settings:              validSetupRuntimeSettings(t),
		Secrets: domain.SetupSecrets{
			QBittorrentPassword: "setup-qb-secret",
			EmbyAPIKey:          "setup-emby-secret",
			TMDbAPIToken:        "setup-tmdb-secret",
		},
	}
	status, err := bootstrap.Initialize(ctx, input)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if status.State != domain.SetupCompleted || !status.DatabaseConfigured || !status.AdministratorConfigured || activation.DatabaseURL == "" || len(activation.ConfigEncryptionKey) != 32 || activation.AdminID == uuid.Nil {
		t.Fatalf("Initialize() status = %#v, activation = %#v", status, activation)
	}
	status, err = bootstrap.Status(ctx)
	if err != nil || status.State != domain.SetupCompleted {
		t.Fatalf("Status() = %#v, %v", status, err)
	}
	if _, err := bootstrap.Initialize(ctx, input); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("second Initialize() error = %v", err)
	}

	targetURL := *parsed
	targetURL.Path = "/" + databaseName
	targetPool, err := database.Open(ctx, targetURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer targetPool.Close()
	queries := db.New(targetPool)
	admin, err := queries.GetFirstAdminUser(ctx)
	if err != nil || admin.Username != "setup-admin" {
		t.Fatalf("administrator = %#v, %v", admin, err)
	}
	installation, err := queries.GetInstallationState(ctx)
	if err != nil || installation.CompletedBy != admin.ID {
		t.Fatalf("installation = %#v, %v", installation, err)
	}
	configurationStore := repository.NewConfiguration(queries, database.NewTransactor(targetPool))
	configuration, err := configurationStore.Load(ctx)
	expectedSettings := input.Settings
	expectedSettings.Agent = expectedSettings.Agent.WithDefaults()
	if err != nil || configuration.Version != 1 || configuration.Settings != expectedSettings {
		t.Fatalf("configuration = %#v, %v", configuration, err)
	}
	cipher, err := NewSecretCipher(activation.ConfigEncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	configurationService := NewConfigurationService(configurationStore, cipher)
	for name, expected := range map[string]string{
		domain.SecretQBittorrentPassword: input.Secrets.QBittorrentPassword,
		domain.SecretEmbyAPIKey:          input.Secrets.EmbyAPIKey,
		domain.SecretTMDbAPIToken:        input.Secrets.TMDbAPIToken,
	} {
		actual, secretErr := configurationService.ResolveSecret(ctx, name)
		if secretErr != nil || actual != expected {
			t.Fatalf("secret %q = %q, %v", name, actual, secretErr)
		}
		secret, secretErr := queries.GetAppSecret(ctx, name)
		if secretErr != nil || strings.Contains(string(secret.Ciphertext), expected) {
			t.Fatalf("stored secret %q contains plaintext or cannot be loaded: %v", name, secretErr)
		}
	}
	profile, err := queries.GetDefaultTranscodeProfile(ctx)
	if err != nil || profile.Name != input.Settings.Transcode.Name || !profile.Active || !profile.IsDefault {
		t.Fatalf("default transcode profile = %#v, %v", profile, err)
	}
	latestAppVersion, err := database.LatestApplicationMigrationVersion()
	if err != nil {
		t.Fatal(err)
	}
	var appVersion int64
	if err := targetPool.QueryRow(ctx, `SELECT version FROM schema_migrations`).Scan(&appVersion); err != nil || appVersion != latestAppVersion {
		t.Fatalf("application migration version = %d, want %d: %v", appVersion, latestAppVersion, err)
	}
	var riverMigrationCount int
	if err := targetPool.QueryRow(ctx, `SELECT count(*) FROM river_migration`).Scan(&riverMigrationCount); err != nil || riverMigrationCount == 0 {
		t.Fatalf("River migration count = %d, %v", riverMigrationCount, err)
	}
}

func TestBootstrapInitializeRollsBackAdministratorAndConfigurationTogetherIntegration(t *testing.T) {
	databaseURL, pool := testutil.NewMigratedPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION reject_installation_completion() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'injected installation failure';
		END;
		$$;
		CREATE TRIGGER installation_failure
		BEFORE INSERT ON installation_state
		FOR EACH ROW EXECUTE FUNCTION reject_installation_completion();
	`); err != nil {
		t.Fatal(err)
	}

	bootstrap := NewBootstrap(BootstrapOptions{
		DatabaseURL:               databaseURL,
		DatabaseManagedExternally: true,
		Store:                     platformconfig.NewBootstrapStore(filepath.Join(t.TempDir(), "bootstrap.json")),
		Migrator:                  database.NewMigrator(),
		Passwords:                 NewPasswordHasher(),
	})
	_, err := bootstrap.Initialize(ctx, domain.InitializeSetup{
		AdministratorUsername: "setup-admin",
		AdministratorPassword: "fixture-password-123",
		Settings:              validSetupRuntimeSettings(t),
		Secrets: domain.SetupSecrets{
			QBittorrentPassword: "setup-qb-secret",
			EmbyAPIKey:          "setup-emby-secret",
			TMDbAPIToken:        "setup-tmdb-secret",
		},
	})
	if err == nil {
		t.Fatal("Initialize() error = nil, want injected transaction failure")
	}
	for table, expected := range map[string]int{
		"admin_users":        0,
		"app_settings":       0,
		"app_secrets":        0,
		"transcode_profiles": 0,
		"events":             0,
		"installation_state": 0,
	} {
		var count int
		if queryErr := pool.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&count); queryErr != nil || count != expected {
			t.Fatalf("%s count = %d, want %d: %v", table, count, expected, queryErr)
		}
	}
}

func validSetupRuntimeSettings(t *testing.T) domain.RuntimeSettings {
	t.Helper()
	root := t.TempDir()
	return domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://127.0.0.1:8080", Username: "downloader"},
		Emby:        domain.EmbySettings{URL: "http://127.0.0.1:8096/emby"},
		Paths: domain.PathSettings{
			DownloadRoot:     filepath.Join(root, "downloads"),
			WorkRoot:         filepath.Join(root, "work"),
			StagingRoot:      filepath.Join(root, "staging"),
			AnimeLibraryRoot: filepath.Join(root, "library", "anime"),
			MovieLibraryRoot: filepath.Join(root, "library", "movies"),
			FFmpegPath:       filepath.Join(root, "bin", "ffmpeg"),
			FFprobePath:      filepath.Join(root, "bin", "ffprobe"),
		},
		Transcode: validTestTranscodeProfile(),
	}
}
