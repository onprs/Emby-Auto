package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

const maxQBRateLimitKibPerSecond int64 = 2147483647

type ConfigurationStore interface {
	Load(context.Context) (domain.Configuration, error)
	Save(context.Context, domain.SaveConfiguration) (domain.Configuration, error)
	GetSecret(context.Context, string) (domain.EncryptedSecret, error)
}

type ConfigurationService struct {
	store  ConfigurationStore
	cipher *SecretCipher
}

func NewConfigurationService(store ConfigurationStore, cipher *SecretCipher) *ConfigurationService {
	return &ConfigurationService{store: store, cipher: cipher}
}

func (configurationService *ConfigurationService) Load(ctx context.Context) (domain.Configuration, error) {
	configuration, err := configurationService.store.Load(ctx)
	if err != nil {
		return domain.Configuration{}, NewError(
			"service_unavailable",
			"configuration storage is unavailable",
			fmt.Errorf("load configuration: %w", err),
			map[string]any{"dependency": "postgresql"},
		)
	}
	return configuration, nil
}

func (configurationService *ConfigurationService) PrepareInitial(
	settings domain.RuntimeSettings,
	secrets domain.SetupSecrets,
	updatedBy uuid.UUID,
) (domain.SaveConfiguration, error) {
	settings.Agent = settings.Agent.WithDefaults()
	if settings.Events.RetentionDays == 0 {
		settings.Events.RetentionDays = domain.DefaultEventsRetentionDays
	}
	if err := validateInitialRuntimeSettings(settings); err != nil {
		return domain.SaveConfiguration{}, err
	}

	values := map[string]string{
		domain.SecretQBittorrentPassword: secrets.QBittorrentPassword,
		domain.SecretEmbyAPIKey:          secrets.EmbyAPIKey,
		domain.SecretTMDbAPIToken:        secrets.TMDbAPIToken,
	}
	mutations := make([]domain.SecretMutation, 0, len(values))
	for _, name := range []string{
		domain.SecretQBittorrentPassword,
		domain.SecretEmbyAPIKey,
		domain.SecretTMDbAPIToken,
	} {
		mutation, _, err := configurationService.prepareSecret(name, domain.SecretUpdate{
			Action: domain.SecretSet,
			Value:  values[name],
		})
		if err != nil {
			return domain.SaveConfiguration{}, err
		}
		mutations = append(mutations, mutation)
	}
	return domain.SaveConfiguration{
		ExpectedVersion: 0,
		Settings:        settings,
		Secrets:         mutations,
		UpdatedBy:       updatedBy,
	}, nil
}

func (configurationService *ConfigurationService) Update(
	ctx context.Context,
	update domain.ConfigurationUpdate,
	updatedBy uuid.UUID,
) (domain.Configuration, error) {
	if update.ExpectedVersion < 0 {
		return domain.Configuration{}, invalidConfiguration("expectedVersion", "must be nonnegative")
	}
	var current *domain.Configuration
	loadCurrent := func(purpose string) (domain.Configuration, error) {
		if current != nil {
			return *current, nil
		}
		loaded, loadErr := configurationService.store.Load(ctx)
		if loadErr != nil {
			return domain.Configuration{}, NewError(
				"service_unavailable",
				"configuration storage is unavailable",
				fmt.Errorf("load configuration for %s: %w", purpose, loadErr),
				map[string]any{"dependency": "postgresql"},
			)
		}
		current = &loaded
		return loaded, nil
	}

	if update.Events == nil {
		loaded, loadErr := loadCurrent("event retention compatibility")
		if loadErr != nil {
			return domain.Configuration{}, loadErr
		}
		update.Settings.Events = loaded.Settings.Events
	} else {
		update.Settings.Events = *update.Events
	}
	update.Settings.Agent = update.Settings.Agent.WithDefaults()
	if err := validateRuntimeSettings(update.Settings); err != nil {
		return domain.Configuration{}, err
	}

	mutations := make([]domain.SecretMutation, 0, len(update.Secrets))
	for _, name := range []string{
		domain.SecretQBittorrentPassword,
		domain.SecretEmbyAPIKey,
		domain.SecretTMDbAPIToken,
		domain.SecretAgentAPIKey,
	} {
		secretUpdate, ok := update.Secrets[name]
		if !ok {
			return domain.Configuration{}, invalidConfiguration(name, "secret action is required")
		}
		mutation, include, err := configurationService.prepareSecret(name, secretUpdate)
		if err != nil {
			return domain.Configuration{}, err
		}
		if include {
			mutations = append(mutations, mutation)
		}
	}
	if update.Settings.Agent.Enabled {
		agentKey := update.Secrets[domain.SecretAgentAPIKey]
		switch agentKey.Action {
		case domain.SecretSet:
			// prepareSecret already requires a non-empty value.
		case domain.SecretKeep:
			loaded, loadErr := loadCurrent("Agent secret validation")
			if loadErr != nil {
				return domain.Configuration{}, loadErr
			}
			if !loaded.Secrets[domain.SecretAgentAPIKey].Configured {
				return domain.Configuration{}, invalidConfiguration("agent.apiKey", "must be configured when Agent assistance is enabled")
			}
		case domain.SecretClear:
			return domain.Configuration{}, invalidConfiguration("agent.apiKey", "cannot be cleared while Agent assistance is enabled")
		}
	}

	configuration, err := configurationService.store.Save(ctx, domain.SaveConfiguration{
		ExpectedVersion: update.ExpectedVersion,
		Settings:        update.Settings,
		Secrets:         mutations,
		UpdatedBy:       updatedBy,
	})
	if errors.Is(err, domain.ErrVersionConflict) {
		return domain.Configuration{}, NewError(
			"state_conflict",
			"configuration was modified by another request",
			ErrStateConflict,
			map[string]any{"expectedVersion": update.ExpectedVersion},
		)
	}
	if err != nil {
		return domain.Configuration{}, NewError(
			"service_unavailable",
			"configuration storage is unavailable",
			fmt.Errorf("save configuration: %w", err),
			map[string]any{"dependency": "postgresql"},
		)
	}
	return configuration, nil
}

func (configurationService *ConfigurationService) ResolveSecret(ctx context.Context, name string) (string, error) {
	secret, err := configurationService.store.GetSecret(ctx, name)
	if err != nil {
		return "", fmt.Errorf("load encrypted secret %q: %w", name, err)
	}
	return configurationService.cipher.Decrypt(secret)
}

func (configurationService *ConfigurationService) prepareSecret(
	name string,
	update domain.SecretUpdate,
) (domain.SecretMutation, bool, error) {
	switch update.Action {
	case domain.SecretKeep:
		if update.Value != "" {
			return domain.SecretMutation{}, false, invalidConfiguration(name, "value must be omitted when action is keep")
		}
		return domain.SecretMutation{}, false, nil
	case domain.SecretClear:
		if update.Value != "" {
			return domain.SecretMutation{}, false, invalidConfiguration(name, "value must be omitted when action is clear")
		}
		return domain.SecretMutation{Name: name, Delete: true}, true, nil
	case domain.SecretSet:
		if update.Value == "" {
			return domain.SecretMutation{}, false, invalidConfiguration(name, "value is required when action is set")
		}
		if len(update.Value) > 4096 {
			return domain.SecretMutation{}, false, invalidConfiguration(name, "value must not exceed 4096 bytes")
		}
		encrypted, err := configurationService.cipher.Encrypt(name, update.Value)
		if err != nil {
			return domain.SecretMutation{}, false, fmt.Errorf("encrypt configuration secret %q: %w", name, err)
		}
		return domain.SecretMutation{Name: name, Value: &encrypted}, true, nil
	default:
		return domain.SecretMutation{}, false, invalidConfiguration(name, "action must be keep, set, or clear")
	}
}

func validateInitialRuntimeSettings(settings domain.RuntimeSettings) error {
	required := []struct {
		field string
		value string
	}{
		{field: "qBittorrent.url", value: settings.QBittorrent.URL},
		{field: "qBittorrent.username", value: settings.QBittorrent.Username},
		{field: "emby.url", value: settings.Emby.URL},
		{field: "paths.downloadRoot", value: settings.Paths.DownloadRoot},
		{field: "paths.workRoot", value: settings.Paths.WorkRoot},
		{field: "paths.stagingRoot", value: settings.Paths.StagingRoot},
		{field: "paths.animeLibraryRoot", value: settings.Paths.AnimeLibraryRoot},
		{field: "paths.movieLibraryRoot", value: settings.Paths.MovieLibraryRoot},
		{field: "paths.ffmpegPath", value: settings.Paths.FFmpegPath},
		{field: "paths.ffprobePath", value: settings.Paths.FFprobePath},
	}
	for _, entry := range required {
		if strings.TrimSpace(entry.value) == "" {
			return invalidConfiguration(entry.field, "is required during installation")
		}
	}
	return validateRuntimeSettings(settings)
}

func validateRuntimeSettings(settings domain.RuntimeSettings) error {
	for field, value := range map[string]int64{
		"qBittorrent.downloadRateLimitKibPerSecond": settings.QBittorrent.DownloadRateLimitKibPerSecond,
		"qBittorrent.uploadRateLimitKibPerSecond":   settings.QBittorrent.UploadRateLimitKibPerSecond,
	} {
		if value < 0 || value > maxQBRateLimitKibPerSecond {
			return invalidConfiguration(field, "must be between 0 and 2147483647 KiB/s")
		}
	}
	requiredLibraryRoots := []struct {
		field string
		value string
	}{
		{field: "paths.animeLibraryRoot", value: settings.Paths.AnimeLibraryRoot},
		{field: "paths.movieLibraryRoot", value: settings.Paths.MovieLibraryRoot},
	}
	for _, entry := range requiredLibraryRoots {
		if strings.TrimSpace(entry.value) == "" {
			return invalidConfiguration(entry.field, "is required")
		}
	}
	if err := domain.ValidateTranscodeProfile(settings.Transcode); err != nil {
		var profileErr *domain.TranscodeProfileError
		if errors.As(err, &profileErr) {
			return invalidConfiguration("transcode."+profileErr.Field, profileErr.Reason)
		}
		return invalidConfiguration("transcode", err.Error())
	}
	for field, value := range map[string]string{
		"qBittorrent.url": settings.QBittorrent.URL,
		"emby.url":        settings.Emby.URL,
	} {
		if value == "" {
			continue
		}
		parsed, err := url.ParseRequestURI(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return invalidConfiguration(field, "must be an HTTP(S) URL without embedded credentials")
		}
	}

	proxyURL := strings.TrimSpace(settings.NetworkProxy.URL)
	if settings.NetworkProxy.Enabled && proxyURL == "" {
		return invalidConfiguration("networkProxy.url", "is required when the network proxy is enabled")
	}
	if proxyURL != "" {
		parsed, err := url.ParseRequestURI(proxyURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return invalidConfiguration("networkProxy.url", "must be an HTTP(S) URL without embedded credentials")
		}
		if len(proxyURL) > 2048 {
			return invalidConfiguration("networkProxy.url", "must not exceed 2048 bytes")
		}
	}

	if err := validateAgentSettings(settings.Agent); err != nil {
		return err
	}

	if settings.Events.RetentionDays < 0 || settings.Events.RetentionDays > 36500 {
		return invalidConfiguration("events.retentionDays", "must be between 0 and 36500 days")
	}

	for field, value := range map[string]string{
		"paths.downloadRoot":     settings.Paths.DownloadRoot,
		"paths.workRoot":         settings.Paths.WorkRoot,
		"paths.stagingRoot":      settings.Paths.StagingRoot,
		"paths.animeLibraryRoot": settings.Paths.AnimeLibraryRoot,
		"paths.movieLibraryRoot": settings.Paths.MovieLibraryRoot,
	} {
		if value == "" {
			continue
		}
		cleaned := filepath.Clean(value)
		if !filepath.IsAbs(cleaned) {
			return invalidConfiguration(field, "must be an absolute path")
		}
		volumeRoot := filepath.Clean(filepath.VolumeName(cleaned) + string(filepath.Separator))
		if strings.EqualFold(cleaned, volumeRoot) {
			return invalidConfiguration(field, "must not be a filesystem root")
		}
	}
	animeRoot := filepath.Clean(settings.Paths.AnimeLibraryRoot)
	movieRoot := filepath.Clean(settings.Paths.MovieLibraryRoot)
	if strings.EqualFold(animeRoot, movieRoot) {
		return invalidConfiguration("paths.movieLibraryRoot", "must differ from the anime library root")
	}
	libraryRoots := []string{animeRoot, movieRoot}
	for _, cleanup := range []struct {
		field string
		path  string
	}{
		{field: "paths.downloadRoot", path: settings.Paths.DownloadRoot},
		{field: "paths.workRoot", path: settings.Paths.WorkRoot},
		{field: "paths.stagingRoot", path: settings.Paths.StagingRoot},
	} {
		for _, libraryRoot := range libraryRoots {
			if configurationPathsOverlap(filepath.Clean(cleanup.path), libraryRoot) {
				return invalidConfiguration(cleanup.field, "must not overlap a media library root")
			}
		}
	}
	return nil
}

func validateAgentSettings(settings domain.AgentSettings) error {
	if settings.Protocol != domain.AgentProtocolOpenAIChatCompletions {
		return invalidConfiguration("agent.protocol", "must be openai_chat_completions")
	}
	if settings.RequestTimeoutSeconds < 10 || settings.RequestTimeoutSeconds > 120 {
		return invalidConfiguration("agent.requestTimeoutSeconds", "must be between 10 and 120 seconds")
	}
	for field, mode := range map[string]string{
		"agent.rssCoordinateMode":         settings.RSSCoordinateMode,
		"agent.downloadFileSelectionMode": settings.DownloadFileSelectionMode,
		"agent.subtitleVideoMatchMode":    settings.SubtitleVideoMatchMode,
	} {
		switch mode {
		case domain.AgentResolutionOff, domain.AgentResolutionSuggest, domain.AgentResolutionValidatedAuto:
		default:
			return invalidConfiguration(field, "must be off, suggest, or validated_auto")
		}
	}
	baseURL := strings.TrimSpace(settings.BaseURL)
	model := strings.TrimSpace(settings.Model)
	if len(baseURL) > 2048 {
		return invalidConfiguration("agent.baseUrl", "must not exceed 2048 bytes")
	}
	if len(model) > 256 {
		return invalidConfiguration("agent.model", "must not exceed 256 bytes")
	}
	if baseURL != "" {
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return invalidConfiguration("agent.baseUrl", "must be an HTTP(S) URL without credentials, query, or fragment")
		}
	}
	if settings.Enabled {
		if baseURL == "" {
			return invalidConfiguration("agent.baseUrl", "is required when Agent assistance is enabled")
		}
		if model == "" {
			return invalidConfiguration("agent.model", "is required when Agent assistance is enabled")
		}
	}
	if settings.AllowAutomaticEpisodeMapping && (!settings.Enabled || !settings.EpisodeMappingEnabled) {
		return invalidConfiguration("agent.allowAutomaticEpisodeMapping", "requires Agent assistance and episode Mapping assistance to be enabled")
	}
	return nil
}

func configurationPathsOverlap(left, right string) bool {
	return configurationPathContains(left, right) || configurationPathContains(right, left)
}

func configurationPathContains(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func invalidConfiguration(field, reason string) *Error {
	return NewError(
		"invalid_configuration",
		"the configuration is invalid",
		ErrInvalidInput,
		map[string]any{"field": field, "reason": reason},
	)
}
