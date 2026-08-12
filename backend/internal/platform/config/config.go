package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIAddress             = "127.0.0.1:8080"
	defaultDatabaseConnectTimeout = 5 * time.Second
	defaultShutdownTimeout        = 10 * time.Second
	defaultLogLevel               = "info"
	defaultSessionTTL             = 7 * 24 * time.Hour
	defaultGeneralWorkers         = 10
	defaultTranscodeWorkers       = 1
	defaultOperationHeartbeat     = 10 * time.Second
	defaultDownloadSyncInterval   = 15 * time.Second
	defaultRSSScheduleConcurrency = 4
	defaultBootstrapConfigPath    = "runtime/bootstrap.json"
)

// Config contains process-level settings shared by the API and Worker.
type Config struct {
	APIAddress                        string
	DatabaseURL                       string
	DatabaseConnectTimeout            time.Duration
	ShutdownTimeout                   time.Duration
	LogLevel                          string
	SessionTTL                        time.Duration
	SessionCookieSecure               bool
	ConfigEncryptionKey               []byte
	RiverGeneralWorkers               int
	RiverTranscodeWorkers             int
	OperationHeartbeatInterval        time.Duration
	DownloadSyncInterval              time.Duration
	DownloadManifestResolutionEnabled bool
	RSSScheduleConcurrency            int
	BootstrapConfigPath               string
	DatabaseManagedExternally         bool
	TMDbBaseURL                       string
	SearchProviderName                string
	SearchProviderURLTemplate         string
	MediaOwnerUID                     int
	HostControlExecutable             string
}

// Load reads process settings from environment variables.
func Load() (Config, error) {
	databaseConnectTimeout, err := durationFromEnv("DATABASE_CONNECT_TIMEOUT", defaultDatabaseConnectTimeout)
	if err != nil {
		return Config{}, err
	}

	shutdownTimeout, err := durationFromEnv("SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	sessionTTL, err := durationFromEnv("SESSION_TTL", defaultSessionTTL)
	if err != nil {
		return Config{}, err
	}
	heartbeatInterval, err := durationFromEnv("OPERATION_HEARTBEAT_INTERVAL", defaultOperationHeartbeat)
	if err != nil {
		return Config{}, err
	}
	downloadSyncInterval, err := durationFromEnv("DOWNLOAD_SYNC_INTERVAL", defaultDownloadSyncInterval)
	if err != nil {
		return Config{}, err
	}
	downloadManifestResolutionEnabled, err := boolFromEnv("DOWNLOAD_MANIFEST_RESOLUTION_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	cookieSecure, err := boolFromEnv("SESSION_COOKIE_SECURE", false)
	if err != nil {
		return Config{}, err
	}
	generalWorkers, err := positiveIntFromEnv("RIVER_GENERAL_WORKERS", defaultGeneralWorkers)
	if err != nil {
		return Config{}, err
	}
	transcodeWorkers, err := positiveIntFromEnv("RIVER_TRANSCODE_WORKERS", defaultTranscodeWorkers)
	if err != nil {
		return Config{}, err
	}
	rssScheduleConcurrency, err := positiveIntFromEnv("RSS_SCHEDULE_CONCURRENCY", defaultRSSScheduleConcurrency)
	if err != nil {
		return Config{}, err
	}
	encryptionKey, err := encryptionKeyFromEnv("CONFIG_ENCRYPTION_KEY")
	if err != nil {
		return Config{}, err
	}
	bootstrapPath := stringFromEnv("BOOTSTRAP_CONFIG_PATH", defaultBootstrapConfigPath)
	databaseURL := os.Getenv("DATABASE_URL")
	databaseManagedExternally := databaseURL != ""
	bootstrapData, completed, bootstrapErr := NewBootstrapStore(bootstrapPath).Load()
	if bootstrapErr != nil && !os.IsNotExist(bootstrapErr) {
		return Config{}, fmt.Errorf("load bootstrap configuration: %w", bootstrapErr)
	}
	if completed {
		if databaseURL == "" {
			databaseURL = bootstrapData.DatabaseURL
		}
		if len(encryptionKey) == 0 && bootstrapData.EncryptionKey != "" {
			encryptionKey, err = encryptionKeyFromValue("bootstrap encryption key", bootstrapData.EncryptionKey)
			if err != nil {
				return Config{}, err
			}
		}
	}
	searchProviderName := os.Getenv("SEARCH_PROVIDER_NAME")
	searchProviderURLTemplate := os.Getenv("SEARCH_PROVIDER_URL_TEMPLATE")
	if (searchProviderName == "") != (searchProviderURLTemplate == "") {
		return Config{}, fmt.Errorf("SEARCH_PROVIDER_NAME and SEARCH_PROVIDER_URL_TEMPLATE must be set together")
	}
	mediaOwnerUID, err := positiveIntFromEnv("EMBY_MEDIA_OWNER_UID", 0)
	if err != nil {
		return Config{}, err
	}
	hostControlExecutable := strings.TrimSpace(os.Getenv("EMBY_AUTO_HOST_CONTROL_EXECUTABLE"))
	if err := validateExecutableSetting("EMBY_AUTO_HOST_CONTROL_EXECUTABLE", hostControlExecutable); err != nil {
		return Config{}, err
	}
	return Config{
		APIAddress:                        stringFromEnv("API_ADDRESS", defaultAPIAddress),
		DatabaseURL:                       databaseURL,
		DatabaseConnectTimeout:            databaseConnectTimeout,
		ShutdownTimeout:                   shutdownTimeout,
		LogLevel:                          stringFromEnv("LOG_LEVEL", defaultLogLevel),
		SessionTTL:                        sessionTTL,
		SessionCookieSecure:               cookieSecure,
		ConfigEncryptionKey:               encryptionKey,
		RiverGeneralWorkers:               generalWorkers,
		RiverTranscodeWorkers:             transcodeWorkers,
		OperationHeartbeatInterval:        heartbeatInterval,
		DownloadSyncInterval:              downloadSyncInterval,
		DownloadManifestResolutionEnabled: downloadManifestResolutionEnabled,
		RSSScheduleConcurrency:            rssScheduleConcurrency,
		BootstrapConfigPath:               bootstrapPath,
		DatabaseManagedExternally:         databaseManagedExternally,
		TMDbBaseURL:                       os.Getenv("TMDB_BASE_URL"),
		SearchProviderName:                searchProviderName,
		SearchProviderURLTemplate:         searchProviderURLTemplate,
		MediaOwnerUID:                     mediaOwnerUID,
		HostControlExecutable:             hostControlExecutable,
	}, nil
}

func validateExecutableSetting(name, executable string) error {
	if executable == "" {
		return nil
	}
	if !filepath.IsAbs(executable) {
		return fmt.Errorf("%s must be an absolute path", name)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", name, err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		return fmt.Errorf("%s must be an executable regular file", name)
	}
	return nil
}

func durationFromEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("parse %s: duration must be positive", name)
	}

	return duration, nil
}

func boolFromEnv(name string, fallback bool) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func positiveIntFromEnv(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("parse %s: value must be a positive integer", name)
	}
	return parsed, nil
}

func encryptionKeyFromEnv(name string) ([]byte, error) {
	value := os.Getenv(name)
	if value == "" {
		return nil, nil
	}
	return encryptionKeyFromValue(name, value)
}

func encryptionKeyFromValue(name, value string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		decoded, err := encoding.DecodeString(value)
		if err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("parse %s: value must be base64 encoding of exactly 32 bytes", name)
}

func stringFromEnv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
