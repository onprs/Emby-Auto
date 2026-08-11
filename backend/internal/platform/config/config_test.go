package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadUsesEnvironmentAndDefaults(t *testing.T) {
	t.Setenv("API_ADDRESS", "127.0.0.1:9090")
	t.Setenv("DATABASE_URL", "postgres://local/test")
	t.Setenv("DATABASE_CONNECT_TIMEOUT", "3s")
	t.Setenv("SHUTDOWN_TIMEOUT", "7s")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("SESSION_TTL", "24h")
	t.Setenv("SESSION_COOKIE_SECURE", "true")
	t.Setenv("CONFIG_ENCRYPTION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("RIVER_GENERAL_WORKERS", "12")
	t.Setenv("RIVER_TRANSCODE_WORKERS", "3")
	t.Setenv("OPERATION_HEARTBEAT_INTERVAL", "2s")
	t.Setenv("DOWNLOAD_SYNC_INTERVAL", "11s")
	t.Setenv("DOWNLOAD_MANIFEST_RESOLUTION_ENABLED", "false")
	t.Setenv("RSS_SCHEDULE_CONCURRENCY", "6")
	t.Setenv("TMDB_BASE_URL", "http://127.0.0.1:19090/tmdb")
	t.Setenv("SEARCH_PROVIDER_NAME", "fixture")
	t.Setenv("SEARCH_PROVIDER_URL_TEMPLATE", "http://127.0.0.1:19090/search?q={query}")
	t.Setenv("EMBY_MEDIA_OWNER_UID", "10001")
	executor := t.TempDir() + "/host-control"
	if err := os.WriteFile(executor, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EMBY_AUTO_HOST_CONTROL_EXECUTABLE", executor)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.APIAddress != "127.0.0.1:9090" {
		t.Fatalf("APIAddress = %q, want %q", got.APIAddress, "127.0.0.1:9090")
	}
	if got.DatabaseURL != "postgres://local/test" {
		t.Fatalf("DatabaseURL = %q, want %q", got.DatabaseURL, "postgres://local/test")
	}
	if got.DatabaseConnectTimeout != 3*time.Second {
		t.Fatalf("DatabaseConnectTimeout = %v, want 3s", got.DatabaseConnectTimeout)
	}
	if got.ShutdownTimeout != 7*time.Second {
		t.Fatalf("ShutdownTimeout = %v, want 7s", got.ShutdownTimeout)
	}
	if got.LogLevel != "debug" {
		t.Fatalf("LogLevel = %q, want %q", got.LogLevel, "debug")
	}
	if got.SessionTTL != 24*time.Hour || !got.SessionCookieSecure {
		t.Fatalf("session config = %v secure=%t, want 24h secure", got.SessionTTL, got.SessionCookieSecure)
	}
	if string(got.ConfigEncryptionKey) != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("encryption key = %q, want decoded 32-byte value", got.ConfigEncryptionKey)
	}
	if got.RiverGeneralWorkers != 12 || got.RiverTranscodeWorkers != 3 {
		t.Fatalf("River workers = %d/%d, want 12/3", got.RiverGeneralWorkers, got.RiverTranscodeWorkers)
	}
	if got.OperationHeartbeatInterval != 2*time.Second {
		t.Fatalf("heartbeat interval = %v, want 2s", got.OperationHeartbeatInterval)
	}
	if got.DownloadSyncInterval != 11*time.Second || got.DownloadManifestResolutionEnabled {
		t.Fatalf("download runtime = sync %v manifest resolution %t, want 11s/false", got.DownloadSyncInterval, got.DownloadManifestResolutionEnabled)
	}
	if got.RSSScheduleConcurrency != 6 {
		t.Fatalf("RSS schedule concurrency = %d, want 6", got.RSSScheduleConcurrency)
	}
	if got.TMDbBaseURL != "http://127.0.0.1:19090/tmdb" || got.SearchProviderName != "fixture" || got.SearchProviderURLTemplate != "http://127.0.0.1:19090/search?q={query}" {
		t.Fatalf("fixture endpoints = %q %q %q", got.TMDbBaseURL, got.SearchProviderName, got.SearchProviderURLTemplate)
	}
	if got.MediaOwnerUID != 10001 || got.HostControlExecutable != executor {
		t.Fatalf("media owner UID = %d, host control executable = %q", got.MediaOwnerUID, got.HostControlExecutable)
	}
}

func TestLoadEnablesManifestResolutionByDefault(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://local/test")
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !got.DownloadManifestResolutionEnabled {
		t.Fatal("DownloadManifestResolutionEnabled = false, want default true")
	}
}

func TestLoadRejectsNonPositiveTimeout(t *testing.T) {
	t.Setenv("DATABASE_CONNECT_TIMEOUT", "0s")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want non-positive timeout error")
	}
}

func TestLoadRejectsInvalidSecurityAndWorkerConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "invalid encryption key", env: "CONFIG_ENCRYPTION_KEY", value: "not-base64"},
		{name: "invalid secure cookie flag", env: "SESSION_COOKIE_SECURE", value: "sometimes"},
		{name: "zero general workers", env: "RIVER_GENERAL_WORKERS", value: "0"},
		{name: "negative transcode workers", env: "RIVER_TRANSCODE_WORKERS", value: "-1"},
		{name: "zero download sync interval", env: "DOWNLOAD_SYNC_INTERVAL", value: "0s"},
		{name: "invalid manifest resolution flag", env: "DOWNLOAD_MANIFEST_RESOLUTION_ENABLED", value: "eventually"},
		{name: "zero RSS schedule concurrency", env: "RSS_SCHEDULE_CONCURRENCY", value: "0"},
		{name: "zero media owner UID", env: "EMBY_MEDIA_OWNER_UID", value: "0"},
		{name: "relative host control executor", env: "EMBY_AUTO_HOST_CONTROL_EXECUTABLE", value: "scripts/host-control"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.env, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() error = nil for %s=%q", test.env, test.value)
			}
		})
	}
}

func TestLoadRejectsMissingHostControlExecutor(t *testing.T) {
	t.Setenv("EMBY_AUTO_HOST_CONTROL_EXECUTABLE", t.TempDir()+"/missing-executor")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want missing host control executor error")
	}
}

func TestLoadRequiresFixtureSearchProviderSettingsTogether(t *testing.T) {
	t.Setenv("SEARCH_PROVIDER_NAME", "fixture")
	t.Setenv("SEARCH_PROVIDER_URL_TEMPLATE", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want incomplete search provider error")
	}
}
