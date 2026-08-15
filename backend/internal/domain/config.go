package domain

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

const (
	RuntimeSettingsName        = "runtime"
	DefaultEventsRetentionDays int32 = 30
	SecretQBittorrentPassword = "qbittorrent.password"
	SecretEmbyAPIKey          = "emby.api_key"
	SecretTMDbAPIToken        = "tmdb.api_token"
	SecretAgentAPIKey         = "agent.api_key"

	AgentProtocolOpenAIChatCompletions = "openai_chat_completions"
	AgentResolutionOff                 = "off"
	AgentResolutionSuggest             = "suggest"
	AgentResolutionValidatedAuto       = "validated_auto"
)

var ErrVersionConflict = errors.New("configuration version conflict")

type RuntimeSettings struct {
	QBittorrent  QBittorrentSettings  `json:"qBittorrent"`
	Emby         EmbySettings         `json:"emby"`
	NetworkProxy NetworkProxySettings `json:"networkProxy"`
	Agent        AgentSettings        `json:"agent"`
	Paths        PathSettings         `json:"paths"`
	Transcode    TranscodeProfile     `json:"transcode"`
	Events       EventsSettings       `json:"events"`
}

// EventsSettings 控制事件历史保留策略。RetentionDays 为 0 时表示禁用定期清理。
type EventsSettings struct {
	RetentionDays int32 `json:"retentionDays"`
}

func DefaultEventsSettings() EventsSettings {
	return EventsSettings{RetentionDays: DefaultEventsRetentionDays}
}

type QBittorrentSettings struct {
	URL                           string `json:"url"`
	Username                      string `json:"username"`
	DownloadRateLimitKibPerSecond int64  `json:"downloadRateLimitKibPerSecond"`
	UploadRateLimitKibPerSecond   int64  `json:"uploadRateLimitKibPerSecond"`
}

type EmbySettings struct {
	URL string `json:"url"`
}

type NetworkProxySettings struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
}

type AgentSettings struct {
	Enabled                      bool   `json:"enabled"`
	Protocol                     string `json:"protocol"`
	BaseURL                      string `json:"baseUrl"`
	Model                        string `json:"model"`
	UseNetworkProxy              bool   `json:"useNetworkProxy"`
	RequestTimeoutSeconds        int32  `json:"requestTimeoutSeconds"`
	RSSCoordinateMode            string `json:"rssCoordinateMode"`
	DownloadFileSelectionMode    string `json:"downloadFileSelectionMode"`
	CatalogMatchEnabled          bool   `json:"catalogMatchEnabled"`
	EpisodeMappingEnabled        bool   `json:"episodeMappingEnabled"`
	AllowAutomaticEpisodeMapping bool   `json:"allowAutomaticEpisodeMapping"`
	SubtitleVideoMatchMode       string `json:"subtitleVideoMatchMode"`
}

func DefaultAgentSettings() AgentSettings {
	return AgentSettings{
		Protocol:                  AgentProtocolOpenAIChatCompletions,
		UseNetworkProxy:           true,
		RequestTimeoutSeconds:     60,
		RSSCoordinateMode:         AgentResolutionOff,
		DownloadFileSelectionMode: AgentResolutionOff,
		SubtitleVideoMatchMode:    AgentResolutionOff,
	}
}

func (settings AgentSettings) WithDefaults() AgentSettings {
	defaults := DefaultAgentSettings()
	if settings.Protocol == "" {
		settings.Protocol = defaults.Protocol
	}
	if settings.RequestTimeoutSeconds == 0 {
		settings.RequestTimeoutSeconds = defaults.RequestTimeoutSeconds
	}
	if settings.RSSCoordinateMode == "" {
		settings.RSSCoordinateMode = defaults.RSSCoordinateMode
	}
	if settings.DownloadFileSelectionMode == "" {
		settings.DownloadFileSelectionMode = defaults.DownloadFileSelectionMode
	}
	if settings.SubtitleVideoMatchMode == "" {
		settings.SubtitleVideoMatchMode = defaults.SubtitleVideoMatchMode
	}
	return settings
}

type PathSettings struct {
	DownloadRoot     string `json:"downloadRoot"`
	WorkRoot         string `json:"workRoot"`
	StagingRoot      string `json:"stagingRoot"`
	AnimeLibraryRoot string `json:"animeLibraryRoot"`
	MovieLibraryRoot string `json:"movieLibraryRoot"`
	FFmpegPath       string `json:"ffmpegPath"`
	FFprobePath      string `json:"ffprobePath"`

	// LibraryRoot is retained only to read runtime settings written before the library split.
	LibraryRoot string `json:"libraryRoot,omitempty"`
}

func (paths PathSettings) EffectiveAnimeLibraryRoot() string {
	if strings.TrimSpace(paths.AnimeLibraryRoot) != "" {
		return paths.AnimeLibraryRoot
	}
	return paths.LibraryRoot
}

type SecretMetadata struct {
	Name       string
	Configured bool
	MaskedHint string
}

type Configuration struct {
	Version  int32
	Settings RuntimeSettings
	Secrets  map[string]SecretMetadata
}

type EncryptedSecret struct {
	Name       string
	Ciphertext []byte
	Nonce      []byte
	MaskedHint string
}

type SecretAction string

const (
	SecretKeep  SecretAction = "keep"
	SecretSet   SecretAction = "set"
	SecretClear SecretAction = "clear"
)

type SecretUpdate struct {
	Action SecretAction
	Value  string
}

type ConfigurationUpdate struct {
	ExpectedVersion int32
	Settings        RuntimeSettings
	Secrets         map[string]SecretUpdate
}

type SecretMutation struct {
	Name   string
	Delete bool
	Value  *EncryptedSecret
}

type SaveConfiguration struct {
	ExpectedVersion int32
	Settings        RuntimeSettings
	Secrets         []SecretMutation
	UpdatedBy       uuid.UUID
}
