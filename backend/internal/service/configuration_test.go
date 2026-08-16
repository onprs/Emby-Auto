package service

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type configurationStoreStub struct {
	configuration domain.Configuration
	saved         domain.SaveConfiguration
	saveErr       error
	secret        domain.EncryptedSecret
}

func (store *configurationStoreStub) Load(context.Context) (domain.Configuration, error) {
	return store.configuration, nil
}
func (store *configurationStoreStub) Save(_ context.Context, save domain.SaveConfiguration) (domain.Configuration, error) {
	store.saved = save
	return store.configuration, store.saveErr
}
func (store *configurationStoreStub) GetSecret(context.Context, string) (domain.EncryptedSecret, error) {
	return store.secret, nil
}

func TestConfigurationPrepareInitialRequiresCompleteRuntimeAndEncryptsEverySecret(t *testing.T) {
	cipher, err := NewSecretCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewSecretCipher() error = %v", err)
	}
	root := t.TempDir()
	settings := domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://127.0.0.1:8080", Username: "downloader"},
		Emby:        domain.EmbySettings{URL: "http://127.0.0.1:8096/emby"},
		Paths: domain.PathSettings{
			DownloadRoot:     filepath.Join(root, "downloads"),
			WorkRoot:         filepath.Join(root, "work"),
			StagingRoot:      filepath.Join(root, "staging"),
			AnimeLibraryRoot: filepath.Join(root, "library", "anime"),
			MovieLibraryRoot: filepath.Join(root, "library", "movies"),
			FFmpegPath:       "/usr/bin/ffmpeg",
			FFprobePath:      "/usr/bin/ffprobe",
		},
		Transcode: validTestTranscodeProfile(),
	}
	actorID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	save, err := NewConfigurationService(nil, cipher).PrepareInitial(settings, domain.SetupSecrets{
		QBittorrentPassword: "qb-secret",
		EmbyAPIKey:          "emby-secret",
		TMDbAPIToken:        "tmdb-secret",
	}, actorID)
	if err != nil {
		t.Fatalf("PrepareInitial() error = %v", err)
	}
	if save.ExpectedVersion != 0 || save.UpdatedBy != actorID || len(save.Secrets) != 3 {
		t.Fatalf("prepared save = %#v", save)
	}
	for _, mutation := range save.Secrets {
		if mutation.Delete || mutation.Value == nil || len(mutation.Value.Ciphertext) == 0 {
			t.Fatalf("secret mutation = %#v, want encrypted set", mutation)
		}
	}

	settings.Paths.MovieLibraryRoot = ""
	_, err = NewConfigurationService(nil, cipher).PrepareInitial(settings, domain.SetupSecrets{
		QBittorrentPassword: "qb-secret",
		EmbyAPIKey:          "emby-secret",
		TMDbAPIToken:        "tmdb-secret",
	}, actorID)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Details["field"] != "paths.movieLibraryRoot" {
		t.Fatalf("PrepareInitial() error = %#v, want paths.movieLibraryRoot", err)
	}

	settings.Paths.MovieLibraryRoot = filepath.Join(root, "library", "movies")
	settings.Paths.FFmpegPath = ""
	_, err = NewConfigurationService(nil, cipher).PrepareInitial(settings, domain.SetupSecrets{
		QBittorrentPassword: "qb-secret",
		EmbyAPIKey:          "emby-secret",
		TMDbAPIToken:        "tmdb-secret",
	}, actorID)
	if !errors.As(err, &serviceErr) || serviceErr.Details["field"] != "paths.ffmpegPath" {
		t.Fatalf("PrepareInitial() error = %#v, want paths.ffmpegPath", err)
	}
}

func TestConfigurationUpdateEncryptsSetClearsAndKeepsSecrets(t *testing.T) {
	cipher, err := NewSecretCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewSecretCipher() error = %v", err)
	}
	cipher.random = bytes.NewReader(bytes.Repeat([]byte{0x11}, 24))
	store := &configurationStoreStub{configuration: domain.Configuration{Version: 4}}
	service := NewConfigurationService(store, cipher)
	actorID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	root := t.TempDir()

	configuration, err := service.Update(context.Background(), domain.ConfigurationUpdate{
		ExpectedVersion: 3,
		Events:          &domain.EventsSettings{RetentionDays: 30},
		Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{
				URL: "http://127.0.0.1:8081", Username: "downloader",
				DownloadRateLimitKibPerSecond: 2048, UploadRateLimitKibPerSecond: 512,
			},
			Emby: domain.EmbySettings{URL: "https://emby.example.test"},
			NetworkProxy: domain.NetworkProxySettings{
				Enabled: true,
				URL:     "http://127.0.0.1:7890",
			},
			Transcode: validTestTranscodeProfile(),
			Paths: domain.PathSettings{
				DownloadRoot:     filepath.Join(root, "downloads"),
				WorkRoot:         filepath.Join(root, "work"),
				StagingRoot:      filepath.Join(root, "staging"),
				AnimeLibraryRoot: filepath.Join(root, "library", "anime"),
				MovieLibraryRoot: filepath.Join(root, "library", "movies"),
			},
		},
		Secrets: map[string]domain.SecretUpdate{
			domain.SecretQBittorrentPassword: {Action: domain.SecretSet, Value: "qb-password-5678"},
			domain.SecretEmbyAPIKey:          {Action: domain.SecretClear},
			domain.SecretTMDbAPIToken:        {Action: domain.SecretKeep},
			domain.SecretAgentAPIKey:         {Action: domain.SecretKeep},
		},
	}, actorID)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if configuration.Version != 4 {
		t.Fatalf("configuration version = %d, want 4", configuration.Version)
	}
	if store.saved.ExpectedVersion != 3 || store.saved.UpdatedBy != actorID {
		t.Fatalf("save metadata = version %d actor %s, want 3 and %s", store.saved.ExpectedVersion, store.saved.UpdatedBy, actorID)
	}
	if !store.saved.Settings.NetworkProxy.Enabled || store.saved.Settings.NetworkProxy.URL != "http://127.0.0.1:7890" {
		t.Fatalf("saved network proxy = %#v", store.saved.Settings.NetworkProxy)
	}
	if settings := store.saved.Settings.QBittorrent; settings.DownloadRateLimitKibPerSecond != 2048 || settings.UploadRateLimitKibPerSecond != 512 {
		t.Fatalf("saved qBittorrent rate limits = %#v", settings)
	}
	if len(store.saved.Secrets) != 2 {
		t.Fatalf("secret mutations = %d, want set and clear only", len(store.saved.Secrets))
	}
	setMutation := store.saved.Secrets[0]
	if setMutation.Name != domain.SecretQBittorrentPassword || setMutation.Delete || setMutation.Value == nil {
		t.Fatalf("set mutation = %#v, want encrypted qBittorrent password", setMutation)
	}
	if string(setMutation.Value.Ciphertext) == "qb-password-5678" || setMutation.Value.MaskedHint != "********5678" {
		t.Fatalf("encrypted secret = %#v, want ciphertext and masked suffix", setMutation.Value)
	}
	clearMutation := store.saved.Secrets[1]
	if clearMutation.Name != domain.SecretEmbyAPIKey || !clearMutation.Delete || clearMutation.Value != nil {
		t.Fatalf("clear mutation = %#v, want Emby key deletion", clearMutation)
	}
}

func TestConfigurationUpdatePreservesOmittedEventsAndAcceptsExplicitZero(t *testing.T) {
	cipher, err := NewSecretCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewSecretCipher() error = %v", err)
	}

	for _, test := range []struct {
		name       string
		events     *domain.EventsSettings
		wantStored int32
	}{
		{name: "omitted preserves current value", events: nil, wantStored: 73},
		{name: "explicit zero disables cleanup", events: &domain.EventsSettings{RetentionDays: 0}, wantStored: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &configurationStoreStub{configuration: domain.Configuration{
				Version:  1,
				Settings: domain.RuntimeSettings{Events: domain.EventsSettings{RetentionDays: 73}},
			}}
			update := validConfigurationUpdate(t)
			update.Events = test.events
			update.Settings.Events = domain.EventsSettings{}

			if _, err := NewConfigurationService(store, cipher).Update(context.Background(), update, uuid.New()); err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if got := store.saved.Settings.Events.RetentionDays; got != test.wantStored {
				t.Fatalf("saved retention days = %d, want %d", got, test.wantStored)
			}
		})
	}
}

func TestConfigurationUpdateAcceptsQBittorrentRateLimitBoundaries(t *testing.T) {
	cipher, _ := NewSecretCipher([]byte("0123456789abcdef0123456789abcdef"))
	for _, test := range []struct {
		name  string
		limit int64
	}{
		{name: "unlimited", limit: 0},
		{name: "largest representable KiB value", limit: 2097151},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &configurationStoreStub{}
			update := validConfigurationUpdate(t)
			update.Settings.QBittorrent.DownloadRateLimitKibPerSecond = test.limit
			update.Settings.QBittorrent.UploadRateLimitKibPerSecond = test.limit

			if _, err := NewConfigurationService(store, cipher).Update(context.Background(), update, uuid.New()); err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if got := store.saved.Settings.QBittorrent; got.DownloadRateLimitKibPerSecond != test.limit || got.UploadRateLimitKibPerSecond != test.limit {
				t.Fatalf("saved qBittorrent rate limits = %#v, want %d/%d KiB/s", got, test.limit, test.limit)
			}
		})
	}
}

func TestConfigurationUpdateReportsVersionConflict(t *testing.T) {
	cipher, _ := NewSecretCipher([]byte("0123456789abcdef0123456789abcdef"))
	store := &configurationStoreStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{Events: domain.EventsSettings{RetentionDays: 73}}},
		saveErr:       domain.ErrVersionConflict,
	}
	service := NewConfigurationService(store, cipher)
	update := validConfigurationUpdate(t)
	update.Events = nil

	_, err := service.Update(context.Background(), update, uuid.New())
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("Update() error = %v, want ErrStateConflict", err)
	}
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "state_conflict" {
		t.Fatalf("Update() error = %#v, want state_conflict service error", err)
	}
}

func TestConfigurationUpdateRejectsUnsafePathsURLsAndSecretActions(t *testing.T) {
	cipher, _ := NewSecretCipher([]byte("0123456789abcdef0123456789abcdef"))
	service := NewConfigurationService(&configurationStoreStub{}, cipher)

	tests := []struct {
		name   string
		mutate func(*domain.ConfigurationUpdate)
		field  string
	}{
		{name: "negative qB download limit", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.QBittorrent.DownloadRateLimitKibPerSecond = -1
		}, field: "qBittorrent.downloadRateLimitKibPerSecond"},
		{name: "oversized qB upload limit", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.QBittorrent.UploadRateLimitKibPerSecond = 2097152
		}, field: "qBittorrent.uploadRateLimitKibPerSecond"},
		{name: "URL embeds credentials", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.Emby.URL = "https://admin:secret@example.test"
		}, field: "emby.url"},
		{name: "enabled proxy has no URL", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.NetworkProxy.Enabled = true
		}, field: "networkProxy.url"},
		{name: "proxy uses unsupported scheme", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.NetworkProxy = domain.NetworkProxySettings{Enabled: true, URL: "socks5://127.0.0.1:1080"}
		}, field: "networkProxy.url"},
		{name: "proxy embeds credentials", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.NetworkProxy = domain.NetworkProxySettings{Enabled: true, URL: "http://user:secret@127.0.0.1:7890"}
		}, field: "networkProxy.url"},
		{name: "relative cleanup root", mutate: func(update *domain.ConfigurationUpdate) { update.Settings.Paths.WorkRoot = "relative/work" }, field: "paths.workRoot"},
		{name: "missing movie library root", mutate: func(update *domain.ConfigurationUpdate) { update.Settings.Paths.MovieLibraryRoot = "" }, field: "paths.movieLibraryRoot"},
		{name: "shared media library root", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.Paths.MovieLibraryRoot = update.Settings.Paths.AnimeLibraryRoot
		}, field: "paths.movieLibraryRoot"},
		{name: "download root contains media library", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.Paths.DownloadRoot = filepath.Dir(update.Settings.Paths.AnimeLibraryRoot)
		}, field: "paths.downloadRoot"},
		{name: "unsafe transcode preset", mutate: func(update *domain.ConfigurationUpdate) { update.Settings.Transcode.Preset = "-filter_complex" }, field: "transcode.preset"},
		{name: "negative event retention days", mutate: func(update *domain.ConfigurationUpdate) {
			update.Events = &domain.EventsSettings{RetentionDays: -1}
		}, field: "events.retentionDays"},
		{name: "oversized event retention days", mutate: func(update *domain.ConfigurationUpdate) {
			update.Events = &domain.EventsSettings{RetentionDays: 36501}
		}, field: "events.retentionDays"},
		{name: "set without value", mutate: func(update *domain.ConfigurationUpdate) {
			update.Secrets[domain.SecretEmbyAPIKey] = domain.SecretUpdate{Action: domain.SecretSet}
		}, field: domain.SecretEmbyAPIKey},
		{name: "keep with value", mutate: func(update *domain.ConfigurationUpdate) {
			update.Secrets[domain.SecretTMDbAPIToken] = domain.SecretUpdate{Action: domain.SecretKeep, Value: "must-not-be-present"}
		}, field: domain.SecretTMDbAPIToken},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			update := validConfigurationUpdate(t)
			test.mutate(&update)
			_, err := service.Update(context.Background(), update, uuid.New())
			var serviceErr *Error
			if !errors.As(err, &serviceErr) || serviceErr.Code != "invalid_configuration" || serviceErr.Details["field"] != test.field {
				t.Fatalf("Update() error = %#v, want invalid_configuration for %q", err, test.field)
			}
		})
	}
}

func TestConfigurationUpdateValidatesAgentEnablementAndAutomaticMapping(t *testing.T) {
	cipher, _ := NewSecretCipher([]byte("0123456789abcdef0123456789abcdef"))
	tests := []struct {
		name   string
		mutate func(*domain.ConfigurationUpdate)
		field  string
	}{
		{name: "enabled without base URL", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.Agent.Enabled = true
			update.Settings.Agent.Model = "fixture-model"
			update.Secrets[domain.SecretAgentAPIKey] = domain.SecretUpdate{Action: domain.SecretSet, Value: "agent-key"}
		}, field: "agent.baseUrl"},
		{name: "base URL contains query", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.Agent.BaseURL = "https://agent.example/v1?key=value"
		}, field: "agent.baseUrl"},
		{name: "invalid mode", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.Agent.RSSCoordinateMode = "always"
		}, field: "agent.rssCoordinateMode"},
		{name: "invalid subtitle video match mode", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.Agent.SubtitleVideoMatchMode = "always"
		}, field: "agent.subtitleVideoMatchMode"},
		{name: "automatic Mapping while Agent disabled", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.Agent.AllowAutomaticEpisodeMapping = true
			update.Settings.Agent.EpisodeMappingEnabled = true
		}, field: "agent.allowAutomaticEpisodeMapping"},
		{name: "clear key while enabled", mutate: func(update *domain.ConfigurationUpdate) {
			update.Settings.Agent.Enabled = true
			update.Settings.Agent.BaseURL = "https://agent.example/v1"
			update.Settings.Agent.Model = "fixture-model"
			update.Secrets[domain.SecretAgentAPIKey] = domain.SecretUpdate{Action: domain.SecretClear}
		}, field: "agent.apiKey"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			update := validConfigurationUpdate(t)
			test.mutate(&update)
			_, err := NewConfigurationService(&configurationStoreStub{}, cipher).Update(context.Background(), update, uuid.New())
			var serviceErr *Error
			if !errors.As(err, &serviceErr) || serviceErr.Details["field"] != test.field {
				t.Fatalf("Update() error = %#v, want %s", err, test.field)
			}
		})
	}
}

func TestConfigurationUpdateEnablesAgentWithEncryptedSetOrSavedKey(t *testing.T) {
	cipher, _ := NewSecretCipher([]byte("0123456789abcdef0123456789abcdef"))
	for _, useSaved := range []bool{false, true} {
		t.Run(map[bool]string{false: "set key", true: "keep saved key"}[useSaved], func(t *testing.T) {
			store := &configurationStoreStub{configuration: domain.Configuration{Secrets: map[string]domain.SecretMetadata{
				domain.SecretAgentAPIKey: {Configured: useSaved},
			}}}
			update := validConfigurationUpdate(t)
			update.Settings.Agent.Enabled = true
			update.Settings.Agent.BaseURL = "https://agent.example/v1"
			update.Settings.Agent.Model = "fixture-model"
			if useSaved {
				update.Secrets[domain.SecretAgentAPIKey] = domain.SecretUpdate{Action: domain.SecretKeep}
			} else {
				update.Secrets[domain.SecretAgentAPIKey] = domain.SecretUpdate{Action: domain.SecretSet, Value: "agent-secret"}
			}
			if _, err := NewConfigurationService(store, cipher).Update(context.Background(), update, uuid.New()); err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if !useSaved {
				found := false
				for _, mutation := range store.saved.Secrets {
					if mutation.Name == domain.SecretAgentAPIKey {
						found = mutation.Value != nil && string(mutation.Value.Ciphertext) != "agent-secret"
					}
				}
				if !found {
					t.Fatal("Agent API key was not encrypted")
				}
			}
		})
	}
}

func validConfigurationUpdate(t *testing.T) domain.ConfigurationUpdate {
	t.Helper()
	root := t.TempDir()
	return domain.ConfigurationUpdate{
		ExpectedVersion: 1,
		Events:          &domain.EventsSettings{RetentionDays: 30},
		Settings: domain.RuntimeSettings{
			Paths: domain.PathSettings{
				DownloadRoot:     filepath.Join(root, "downloads"),
				WorkRoot:         filepath.Join(root, "work"),
				StagingRoot:      filepath.Join(root, "staging"),
				AnimeLibraryRoot: filepath.Join(root, "library", "anime"),
				MovieLibraryRoot: filepath.Join(root, "library", "movies"),
			},
			Transcode: validTestTranscodeProfile(),
		},
		Secrets: map[string]domain.SecretUpdate{
			domain.SecretQBittorrentPassword: {Action: domain.SecretKeep},
			domain.SecretEmbyAPIKey:          {Action: domain.SecretKeep},
			domain.SecretTMDbAPIToken:        {Action: domain.SecretKeep},
			domain.SecretAgentAPIKey:         {Action: domain.SecretKeep},
		},
	}
}

func validTestTranscodeProfile() domain.TranscodeProfile {
	return domain.TranscodeProfile{
		Name:           "test-h264",
		VideoCodec:     "h264",
		Encoder:        "libx264",
		Container:      "mp4",
		FileExtension:  "mp4",
		QualityMode:    "crf",
		QualityValue:   20,
		AudioPolicy:    "transcode",
		AudioCodec:     "aac",
		Preset:         "medium",
		PixelFormat:    "yuv420p",
		ThreadCount:    2,
		MaxConcurrency: 1,
	}
}
