package domain

import (
	"encoding/json"
	"testing"
)

func TestPathSettingsReadsLegacyLibraryRootAsAnimeRoot(t *testing.T) {
	var settings RuntimeSettings
	if err := json.Unmarshal([]byte(`{"paths":{"libraryRoot":"/legacy/library"}}`), &settings); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := settings.Paths.EffectiveAnimeLibraryRoot(); got != "/legacy/library" {
		t.Fatalf("EffectiveAnimeLibraryRoot() = %q, want legacy library root", got)
	}
	if settings.Paths.MovieLibraryRoot != "" {
		t.Fatalf("MovieLibraryRoot = %q, want no implicit legacy fallback", settings.Paths.MovieLibraryRoot)
	}
}

func TestRuntimeSettingsWithoutNetworkProxyRemainDisabled(t *testing.T) {
	var settings RuntimeSettings
	if err := json.Unmarshal([]byte(`{"qBittorrent":{"url":"http://qb.test"}}`), &settings); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if settings.NetworkProxy.Enabled || settings.NetworkProxy.URL != "" {
		t.Fatalf("NetworkProxy = %#v, want disabled zero value", settings.NetworkProxy)
	}
	if settings.QBittorrent.DownloadRateLimitKibPerSecond != 0 || settings.QBittorrent.UploadRateLimitKibPerSecond != 0 {
		t.Fatalf("qBittorrent rate limits = %#v, want legacy unlimited defaults", settings.QBittorrent)
	}
}

func TestPathSettingsPrefersExplicitAnimeRoot(t *testing.T) {
	paths := PathSettings{AnimeLibraryRoot: "/media/anime", LibraryRoot: "/legacy/library"}
	if got := paths.EffectiveAnimeLibraryRoot(); got != "/media/anime" {
		t.Fatalf("EffectiveAnimeLibraryRoot() = %q, want explicit anime root", got)
	}
}
