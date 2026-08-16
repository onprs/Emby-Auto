package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	appservice "github.com/onprs/emby-auto/backend/internal/service"
)

type runtimeConfigurationStub struct {
	configuration domain.Configuration
	secrets       map[string]string
	loadErr       error
	resolveErrs   map[string]error
	loadCalls     int
	resolved      []string
	update        domain.ConfigurationUpdate
	updatedBy     uuid.UUID
}

func (stub *runtimeConfigurationStub) Load(context.Context) (domain.Configuration, error) {
	stub.loadCalls++
	return stub.configuration, stub.loadErr
}
func (stub *runtimeConfigurationStub) Update(_ context.Context, update domain.ConfigurationUpdate, updatedBy uuid.UUID) (domain.Configuration, error) {
	stub.update = update
	stub.updatedBy = updatedBy
	return stub.configuration, nil
}
func (stub *runtimeConfigurationStub) ResolveSecret(_ context.Context, name string) (string, error) {
	stub.resolved = append(stub.resolved, name)
	if err := stub.resolveErrs[name]; err != nil {
		return "", err
	}
	return stub.secrets[name], nil
}

type persistedRuntimeConfigurationStore struct {
	configuration domain.Configuration
	saved         domain.SaveConfiguration
}

func (store *persistedRuntimeConfigurationStore) Load(context.Context) (domain.Configuration, error) {
	return store.configuration, nil
}

func (store *persistedRuntimeConfigurationStore) Save(_ context.Context, save domain.SaveConfiguration) (domain.Configuration, error) {
	if save.ExpectedVersion != store.configuration.Version {
		return domain.Configuration{}, domain.ErrVersionConflict
	}
	store.saved = save
	store.configuration.Version++
	store.configuration.Settings = save.Settings
	return store.configuration, nil
}

func (store *persistedRuntimeConfigurationStore) GetSecret(context.Context, string) (domain.EncryptedSecret, error) {
	return domain.EncryptedSecret{}, domain.ErrNotFound
}

func requireSecretRevealNoStore(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("Cache-Control"); got != configurationSecretsCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, configurationSecretsCacheControl)
	}
}

func TestRevealConfigurationSecretsReturnsPlaintextForAuthenticatedAdmin(t *testing.T) {
	userID := uuid.MustParse("10000000-0000-0000-0000-000000000003")
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: userID, Username: "admin"}}}
	configuration := &runtimeConfigurationStub{
		configuration: domain.Configuration{
			Version: 2,
			Secrets: map[string]domain.SecretMetadata{
				domain.SecretQBittorrentPassword: {Configured: true, MaskedHint: "********ord"},
				domain.SecretEmbyAPIKey:          {Configured: true, MaskedHint: "********key"},
				domain.SecretTMDbAPIToken:        {Configured: true, MaskedHint: "********ken"},
				domain.SecretAgentAPIKey:         {Configured: true, MaskedHint: "********ent"},
			},
		},
		secrets: map[string]string{
			domain.SecretQBittorrentPassword: "real-password",
			domain.SecretEmbyAPIKey:          "real-api-key",
			domain.SecretTMDbAPIToken:        "real-tmdb-token",
			domain.SecretAgentAPIKey:         "real-agent-api-key",
		},
	}
	handler := NewHandler(NewServer(
		readinessStub{},
		WithAuthentication(authentication, false),
		WithRuntimeConfiguration(configuration),
	))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/config/secrets/reveal", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	requireSecretRevealNoStore(t, response)
	var decoded RevealedSecrets
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.AgentApiKey == nil || *decoded.AgentApiKey != "real-agent-api-key" {
		t.Fatalf("Agent API key = %#v, want configured plaintext", decoded.AgentApiKey)
	}
	if decoded.QbPassword == nil || *decoded.QbPassword != "real-password" ||
		decoded.EmbyApiKey == nil || *decoded.EmbyApiKey != "real-api-key" ||
		decoded.TmdbApiToken == nil || *decoded.TmdbApiToken != "real-tmdb-token" {
		t.Fatalf("revealed secrets = %#v, want plaintext values", decoded)
	}
}

func TestRevealConfigurationSecretsReturnsOnlyConfiguredValues(t *testing.T) {
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: uuid.MustParse("10000000-0000-0000-0000-000000000005"), Username: "admin"}}}
	configuration := &runtimeConfigurationStub{
		configuration: domain.Configuration{Secrets: map[string]domain.SecretMetadata{
			domain.SecretQBittorrentPassword: {Configured: true},
			domain.SecretEmbyAPIKey:          {Configured: false},
			domain.SecretTMDbAPIToken:        {Configured: false},
			domain.SecretAgentAPIKey:         {Configured: true},
		}},
		secrets: map[string]string{
			domain.SecretQBittorrentPassword: "real-password",
			domain.SecretEmbyAPIKey:          "must-not-be-returned-emby",
			domain.SecretTMDbAPIToken:        "must-not-be-returned-tmdb",
			domain.SecretAgentAPIKey:         "real-agent-api-key",
		},
	}
	handler := NewHandler(NewServer(
		readinessStub{},
		WithAuthentication(authentication, false),
		WithRuntimeConfiguration(configuration),
	))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/config/secrets/reveal", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	requireSecretRevealNoStore(t, response)
	var decoded RevealedSecrets
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.QbPassword == nil || *decoded.QbPassword != "real-password" ||
		decoded.AgentApiKey == nil || *decoded.AgentApiKey != "real-agent-api-key" {
		t.Fatalf("configured secrets = %#v", decoded)
	}
	if decoded.EmbyApiKey != nil || decoded.TmdbApiToken != nil || strings.Contains(response.Body.String(), "must-not-be-returned") {
		t.Fatalf("unconfigured secrets were returned: %s", response.Body.String())
	}
	if len(configuration.resolved) != 2 ||
		configuration.resolved[0] != domain.SecretQBittorrentPassword ||
		configuration.resolved[1] != domain.SecretAgentAPIKey {
		t.Fatalf("resolved secrets = %v, want only configured values", configuration.resolved)
	}
}

func TestRevealConfigurationSecretsAuthenticatesBeforeConfigurationAccess(t *testing.T) {
	configuration := &runtimeConfigurationStub{
		loadErr: errors.New("configuration must not be loaded"),
		secrets: map[string]string{domain.SecretQBittorrentPassword: "must-not-be-returned"},
	}
	handler := NewHandler(NewServer(
		readinessStub{},
		WithAuthentication(&authenticationStub{}, false),
		WithRuntimeConfiguration(configuration),
	))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/config/secrets/reveal", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	requireSecretRevealNoStore(t, response)
	if configuration.loadCalls != 0 || len(configuration.resolved) != 0 {
		t.Fatalf("anonymous request accessed configuration: load=%d resolve=%v", configuration.loadCalls, configuration.resolved)
	}
}

func TestRevealConfigurationSecretsHidesDecryptionFailures(t *testing.T) {
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: uuid.MustParse("10000000-0000-0000-0000-000000000004"), Username: "admin"}}}
	configuration := &runtimeConfigurationStub{
		configuration: domain.Configuration{Secrets: map[string]domain.SecretMetadata{
			domain.SecretQBittorrentPassword: {Configured: true},
		}},
		secrets: map[string]string{domain.SecretQBittorrentPassword: "must-not-be-returned"},
		resolveErrs: map[string]error{
			domain.SecretQBittorrentPassword: errors.New("decrypt sensitive-ciphertext failed"),
		},
	}
	handler := NewHandler(NewServer(
		readinessStub{},
		WithAuthentication(authentication, false),
		WithRuntimeConfiguration(configuration),
	))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/config/secrets/reveal", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	requireSecretRevealNoStore(t, response)
	if strings.Contains(response.Body.String(), "sensitive-ciphertext") || strings.Contains(response.Body.String(), "must-not-be-returned") {
		t.Fatalf("decryption failure leaked secret material: %s", response.Body.String())
	}
}

func TestConfigurationResponseContainsOnlyMaskedSecretMetadata(t *testing.T) {
	userID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: userID, Username: "admin"}}}
	configuration := &runtimeConfigurationStub{configuration: domain.Configuration{
		Version: 7,
		Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{
				URL: "http://qb.test", Username: "downloader",
				DownloadRateLimitKibPerSecond: 2048, UploadRateLimitKibPerSecond: 512,
			},
			Emby:         domain.EmbySettings{URL: "https://emby.test"},
			NetworkProxy: domain.NetworkProxySettings{Enabled: true, URL: "http://127.0.0.1:7890"},
			Agent: domain.AgentSettings{
				Enabled: true, Protocol: domain.AgentProtocolOpenAIChatCompletions,
				BaseURL: "https://agent.example/v1", Model: "fixture-model", UseNetworkProxy: true,
				RequestTimeoutSeconds: 60, RSSCoordinateMode: domain.AgentResolutionSuggest,
				DownloadFileSelectionMode: domain.AgentResolutionOff, EpisodeMappingEnabled: true,
			},
			Paths: domain.PathSettings{LibraryRoot: "C:\\legacy\\anime"},
			Transcode: domain.TranscodeProfile{
				Name: "h264", VideoCodec: "h264", Encoder: "libx264", Container: "mp4", FileExtension: "mp4",
				QualityMode: "crf", QualityValue: 20, AudioPolicy: "transcode", AudioCodec: "aac",
				Preset: "medium", PixelFormat: "yuv420p", ThreadCount: 2, MaxConcurrency: 1,
			},
			Events: domain.EventsSettings{RetentionDays: 30},
		},
		Secrets: map[string]domain.SecretMetadata{
			domain.SecretQBittorrentPassword: {Configured: true, MaskedHint: "********5678"},
			domain.SecretEmbyAPIKey:          {Configured: true, MaskedHint: "********abcd"},
			domain.SecretTMDbAPIToken:        {Configured: false, MaskedHint: ""},
			domain.SecretAgentAPIKey:         {Configured: true, MaskedHint: "********0012"},
		},
	}}
	handler := NewHandler(NewServer(
		readinessStub{},
		WithAuthentication(authentication, false),
		WithRuntimeConfiguration(configuration),
	))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "real-password") || strings.Contains(body, "real-api-key") {
		t.Fatalf("response contains secret plaintext: %s", body)
	}
	if strings.Contains(body, `"libraryRoot"`) {
		t.Fatalf("response exposes legacy libraryRoot: %s", body)
	}
	var decoded Configuration
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.Version != 7 || !decoded.QBittorrent.Password.Configured || decoded.QBittorrent.Password.Masked != "********5678" {
		t.Fatalf("configuration response = %#v, want version and masked password", decoded)
	}
	if decoded.QBittorrent.DownloadRateLimitKibPerSecond != 2048 || decoded.QBittorrent.UploadRateLimitKibPerSecond != 512 {
		t.Fatalf("qBittorrent rate limits = %d/%d, want 2048/512 KiB/s", decoded.QBittorrent.DownloadRateLimitKibPerSecond, decoded.QBittorrent.UploadRateLimitKibPerSecond)
	}
	if decoded.Tmdb.ApiToken.Configured || decoded.Tmdb.ApiToken.Masked != "" {
		t.Fatalf("TMDb token = %#v, want unconfigured", decoded.Tmdb.ApiToken)
	}
	if !decoded.NetworkProxy.Enabled || decoded.NetworkProxy.Url != "http://127.0.0.1:7890" {
		t.Fatalf("network proxy = %#v", decoded.NetworkProxy)
	}
	if !decoded.Agent.Enabled || decoded.Agent.Model != "fixture-model" || !decoded.Agent.ApiKey.Configured || decoded.Agent.ApiKey.Masked != "********0012" {
		t.Fatalf("Agent configuration = %#v, want enabled masked configuration", decoded.Agent)
	}
	if decoded.Paths.AnimeLibraryRoot != `C:\legacy\anime` || decoded.Paths.MovieLibraryRoot != "" {
		t.Fatalf("library roots = %#v, want legacy anime fallback and empty movie root", decoded.Paths)
	}
	if decoded.Events.RetentionDays != 30 {
		t.Fatalf("event retention days = %d, want 30", decoded.Events.RetentionDays)
	}
}

func rawConfigurationUpdateBody(t *testing.T, events map[string]any) []byte {
	t.Helper()
	root := t.TempDir()
	body := map[string]any{
		"expectedVersion": int32(8),
		"qBittorrent": map[string]any{
			"url": "http://qb.test", "username": "downloader", "password": map[string]any{"action": "keep"},
			"downloadRateLimitKibPerSecond": 4096, "uploadRateLimitKibPerSecond": 1024,
		},
		"emby":         map[string]any{"url": "https://emby.test", "apiKey": map[string]any{"action": "keep"}},
		"tmdb":         map[string]any{"apiToken": map[string]any{"action": "keep"}},
		"networkProxy": map[string]any{"enabled": false, "url": ""},
		"agent": map[string]any{
			"enabled": false, "protocol": "openai_chat_completions", "baseUrl": "", "model": "",
			"apiKey": map[string]any{"action": "keep"}, "useNetworkProxy": true, "requestTimeoutSeconds": 60,
			"rssCoordinateMode": "off", "downloadFileSelectionMode": "off", "catalogMatchEnabled": false,
			"episodeMappingEnabled": false, "allowAutomaticEpisodeMapping": false, "subtitleVideoMatchMode": "off",
		},
		"paths": map[string]any{
			"downloadRoot": filepath.Join(root, "downloads"), "workRoot": filepath.Join(root, "work"),
			"stagingRoot": filepath.Join(root, "staging"), "animeLibraryRoot": filepath.Join(root, "anime"),
			"movieLibraryRoot": filepath.Join(root, "movies"), "ffmpegPath": "ffmpeg", "ffprobePath": "ffprobe",
		},
		"transcode": map[string]any{
			"name": "h264", "videoCodec": "h264", "encoder": "libx264", "container": "mp4", "fileExtension": "mp4",
			"qualityMode": "crf", "qualityValue": 20, "audioPolicy": "transcode", "audioCodec": "aac",
			"preset": "medium", "pixelFormat": "yuv420p", "threadCount": 2, "maxConcurrency": 1,
		},
	}
	if events != nil {
		body["events"] = events
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode configuration update body: %v", err)
	}
	return encoded
}

func TestConfigurationUpdateRawHTTPEnforcesEventRetentionPresenceAndBounds(t *testing.T) {
	userID := uuid.MustParse("10000000-0000-0000-0000-000000000006")
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: userID, Username: "admin"}}}

	for _, test := range []struct {
		name       string
		events     map[string]any
		wantStatus int
		wantDays   int32
	}{
		{name: "legacy body omits events", wantStatus: http.StatusOK, wantDays: 73},
		{name: "events object omits required retention days", events: map[string]any{}, wantStatus: http.StatusBadRequest, wantDays: 73},
		{name: "explicit zero disables cleanup", events: map[string]any{"retentionDays": 0}, wantStatus: http.StatusOK, wantDays: 0},
		{name: "negative retention is rejected", events: map[string]any{"retentionDays": -1}, wantStatus: http.StatusBadRequest, wantDays: 73},
		{name: "retention above maximum is rejected", events: map[string]any{"retentionDays": 36501}, wantStatus: http.StatusBadRequest, wantDays: 73},
		{name: "non-integer retention is rejected", events: map[string]any{"retentionDays": "thirty"}, wantStatus: http.StatusBadRequest, wantDays: 73},
		{name: "null retention is rejected", events: map[string]any{"retentionDays": nil}, wantStatus: http.StatusBadRequest, wantDays: 73},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &persistedRuntimeConfigurationStore{configuration: domain.Configuration{
				Version:  8,
				Settings: domain.RuntimeSettings{Events: domain.EventsSettings{RetentionDays: 73}},
				Secrets:  map[string]domain.SecretMetadata{},
			}}
			cipher, err := appservice.NewSecretCipher([]byte("0123456789abcdef0123456789abcdef"))
			if err != nil {
				t.Fatalf("NewSecretCipher() error = %v", err)
			}
			handler := NewHandler(NewServer(
				readinessStub{},
				WithAuthentication(authentication, false),
				WithRuntimeConfiguration(appservice.NewConfigurationService(store, cipher)),
			))
			request := httptest.NewRequest(http.MethodPut, "/api/v1/config", strings.NewReader(string(rawConfigurationUpdateBody(t, test.events))))
			request.Header.Set("Content-Type", "application/json")
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, test.wantStatus, response.Body.String())
			}
			if got := store.configuration.Settings.Events.RetentionDays; got != test.wantDays {
				t.Fatalf("persisted retention days = %d, want %d", got, test.wantDays)
			}
			if test.wantStatus != http.StatusOK {
				if store.configuration.Version != 8 {
					t.Fatalf("configuration version = %d, want unchanged version 8", store.configuration.Version)
				}
				return
			}
			if got := store.saved.Settings.Events.RetentionDays; got != test.wantDays {
				t.Fatalf("saved retention days = %d, want %d", got, test.wantDays)
			}
			var decoded Configuration
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if decoded.Events.RetentionDays != test.wantDays {
				t.Fatalf("response retention days = %d, want %d", decoded.Events.RetentionDays, test.wantDays)
			}
		})
	}
}

func TestConfigurationUpdateMapsExplicitSecretActionsAndActor(t *testing.T) {
	userID := uuid.MustParse("10000000-0000-0000-0000-000000000002")
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: userID, Username: "admin"}}}
	configuration := &runtimeConfigurationStub{configuration: domain.Configuration{Version: 4, Secrets: map[string]domain.SecretMetadata{}}}
	handler := NewHandler(NewServer(
		readinessStub{},
		WithAuthentication(authentication, false),
		WithRuntimeConfiguration(configuration),
	))
	body := `{
		"expectedVersion":3,
		"qBittorrent":{"url":"http://qb.test","username":"downloader","password":{"action":"set","value":"qb-secret"},"downloadRateLimitKibPerSecond":4096,"uploadRateLimitKibPerSecond":1024},
		"emby":{"url":"https://emby.test","apiKey":{"action":"clear"}},
		"tmdb":{"apiToken":{"action":"keep"}},
		"networkProxy":{"enabled":true,"url":"http://127.0.0.1:7890"},
		"agent":{"enabled":true,"protocol":"openai_chat_completions","baseUrl":"https://agent.example/v1","model":"fixture-model","apiKey":{"action":"set","value":"agent-secret"},"useNetworkProxy":true,"requestTimeoutSeconds":60,"rssCoordinateMode":"suggest","downloadFileSelectionMode":"off","catalogMatchEnabled":false,"episodeMappingEnabled":true,"allowAutomaticEpisodeMapping":false},
		"paths":{"downloadRoot":"C:\\media\\downloads","workRoot":"C:\\media\\work","stagingRoot":"C:\\media\\staging","animeLibraryRoot":"C:\\media\\library\\anime","movieLibraryRoot":"C:\\media\\library\\movies","ffmpegPath":"ffmpeg","ffprobePath":"ffprobe"},
		"transcode":{"name":"h264","videoCodec":"h264","encoder":"libx264","container":"mp4","fileExtension":"mp4","qualityMode":"crf","qualityValue":20,"audioPolicy":"transcode","audioCodec":"aac","preset":"medium","pixelFormat":"yuv420p","threadCount":2,"maxConcurrency":1},
		"events":{"retentionDays":90}
	}`
	request := httptest.NewRequest(http.MethodPut, "/api/v1/config", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if configuration.updatedBy != userID || configuration.update.ExpectedVersion != 3 {
		t.Fatalf("update actor/version = %s/%d, want %s/3", configuration.updatedBy, configuration.update.ExpectedVersion, userID)
	}
	if got := configuration.update.Secrets[domain.SecretQBittorrentPassword]; got.Action != domain.SecretSet || got.Value != "qb-secret" {
		t.Fatalf("qB secret update = %#v, want set with value", got)
	}
	if got := configuration.update.Secrets[domain.SecretEmbyAPIKey]; got.Action != domain.SecretClear || got.Value != "" {
		t.Fatalf("Emby secret update = %#v, want clear", got)
	}
	if got := configuration.update.Secrets[domain.SecretTMDbAPIToken]; got.Action != domain.SecretKeep || got.Value != "" {
		t.Fatalf("TMDb secret update = %#v, want keep", got)
	}
	if got := configuration.update.Secrets[domain.SecretAgentAPIKey]; got.Action != domain.SecretSet || got.Value != "agent-secret" {
		t.Fatalf("Agent secret update = %#v, want set", got)
	}
	if settings := configuration.update.Settings.Agent; !settings.Enabled || settings.Model != "fixture-model" || settings.RSSCoordinateMode != domain.AgentResolutionSuggest {
		t.Fatalf("Agent settings = %#v", settings)
	}
	if settings := configuration.update.Settings.QBittorrent; settings.DownloadRateLimitKibPerSecond != 4096 || settings.UploadRateLimitKibPerSecond != 1024 {
		t.Fatalf("qBittorrent rate limits = %#v, want 4096/1024 KiB/s", settings)
	}
	if configuration.update.Events == nil || configuration.update.Events.RetentionDays != 90 || configuration.update.Settings.Events.RetentionDays != 90 {
		t.Fatalf("event retention update = %#v, settings = %#v; want explicit 90", configuration.update.Events, configuration.update.Settings.Events)
	}
	if proxy := configuration.update.Settings.NetworkProxy; !proxy.Enabled || proxy.URL != "http://127.0.0.1:7890" {
		t.Fatalf("network proxy = %#v", proxy)
	}
	if profile := configuration.update.Settings.Transcode; profile.Encoder != "libx264" || profile.AudioCodec != "aac" || profile.MaxConcurrency != 1 {
		t.Fatalf("transcode profile = %#v", profile)
	}
}
