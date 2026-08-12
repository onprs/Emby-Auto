package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/agentapi"
	"github.com/onprs/emby-auto/backend/internal/platform/emby"
	"github.com/onprs/emby-auto/backend/internal/platform/mediatools"
	"github.com/onprs/emby-auto/backend/internal/platform/proxyhttp"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/onprs/emby-auto/backend/internal/platform/tmdb"
)

const connectivityTimeout = 10 * time.Second

// ConnectivityResolver loads runtime settings and decrypts secrets.
type ConnectivityResolver interface {
	Load(ctx context.Context) (domain.Configuration, error)
	ResolveSecret(ctx context.Context, name string) (string, error)
}

// ConnectivityService tests fixed external dependencies without echoing secrets.
type ConnectivityResultStore interface {
	UpsertConnectivityTestResult(context.Context, db.UpsertConnectivityTestResultParams) (db.ConnectivityTestResult, error)
}

type ConnectivityService struct {
	configuration  ConnectivityResolver
	tools          *mediatools.Tools
	results        ConnectivityResultStore
	now            func() time.Time
	requestTimeout time.Duration
	tmdbBaseURL    string
}

func NewConnectivityService(configuration ConnectivityResolver, tools *mediatools.Tools, queries *db.Queries, tmdbBaseURL ...string) *ConnectivityService {
	service := &ConnectivityService{
		configuration:  configuration,
		tools:          tools,
		now:            time.Now,
		requestTimeout: connectivityTimeout,
	}
	if queries != nil {
		service.results = queries
	}
	if len(tmdbBaseURL) > 0 {
		service.tmdbBaseURL = tmdbBaseURL[0]
	}
	return service
}

func (service *ConnectivityService) Test(ctx context.Context, request domain.ConnectivityTestRequest) (domain.ConnectivityTestResult, error) {
	testCtx, cancel := context.WithTimeout(ctx, service.requestTimeout)
	defer cancel()
	result := domain.ConnectivityTestResult{Target: request.Target, CheckedAt: service.now().UTC()}

	configuration, err := service.configuration.Load(testCtx)
	if err != nil {
		return result, err
	}

	var testErr error
	switch request.Target {
	case "qbittorrent":
		testErr = service.testQBittorrent(testCtx, configuration, request.QBittorrent)
	case "tmdb":
		testErr = service.testTMDb(testCtx, configuration, request.TMDb)
	case "emby":
		testErr = service.testEmby(testCtx, configuration, request.Emby)
	case "media_tools":
		testErr = service.testMediaTools(testCtx, configuration)
	case "network_proxy":
		if request.NetworkProxy == nil {
			return result, NewError("invalid_network_proxy", "network proxy settings are required", ErrInvalidInput, map[string]any{"field": "networkProxy"})
		}
		if !request.NetworkProxy.Enabled {
			return result, NewError("invalid_network_proxy", "the network proxy must be enabled for testing", ErrInvalidInput, map[string]any{"field": "networkProxy.enabled"})
		}
		testErr = service.testNetworkProxy(testCtx, request.NetworkProxy)
	case "agent":
		testErr = service.testAgent(testCtx, configuration, request.Agent)
	default:
		return result, NewError("invalid_target", "unknown connectivity target", ErrInvalidInput, map[string]any{"target": request.Target})
	}

	if testErr != nil {
		result.Success = false
		result.Code = "connection_failed"
		var agentErr *agentapi.Error
		if errors.As(testErr, &agentErr) {
			result.Code = agentErr.Code
		}
		result.Message = safeConnectivityMessage(testErr)
	} else {
		result.Success = true
		result.Code = "ok"
		result.Message = "connection succeeded"
	}
	if service.results != nil {
		if _, err := service.results.UpsertConnectivityTestResult(ctx, db.UpsertConnectivityTestResultParams{
			Target: result.Target, Success: result.Success, Code: result.Code, Message: result.Message,
			TestedAt: pgtype.Timestamptz{Time: result.CheckedAt, Valid: true},
		}); err != nil {
			return result, fmt.Errorf("persist connectivity test result: %w", err)
		}
	}
	return result, nil
}

func (service *ConnectivityService) testQBittorrent(ctx context.Context, configuration domain.Configuration, override *domain.QBittorrentTestConfig) error {
	settings := configuration.Settings.QBittorrent
	username := settings.Username
	url := settings.URL
	if override != nil {
		if override.URL != "" {
			url = override.URL
		}
		if override.Username != "" {
			username = override.Username
		}
	}
	var passwordOverride *string
	if override != nil {
		passwordOverride = override.Password
	}
	password, err := service.resolveSecret(ctx, domain.SecretQBittorrentPassword, passwordOverride)
	if err != nil {
		return err
	}
	client, err := qbittorrent.NewClient(qbittorrent.ClientOptions{
		BaseURL: url, Username: username, Password: password, RequestTimeout: connectivityTimeout,
	})
	if err != nil {
		return err
	}
	return client.Login(ctx)
}

func (service *ConnectivityService) testTMDb(ctx context.Context, configuration domain.Configuration, override *domain.TMDbTestConfig) error {
	var tokenOverride *string
	if override != nil {
		tokenOverride = override.APIToken
	}
	token, err := service.resolveSecret(ctx, domain.SecretTMDbAPIToken, tokenOverride)
	if err != nil {
		return err
	}
	httpClient, err := proxyhttp.NewClient(configuration.Settings.NetworkProxy)
	if err != nil {
		return err
	}
	client, err := tmdb.NewClient(tmdb.ClientOptions{
		BaseURL: service.tmdbBaseURL, APIToken: token, RequestTimeout: connectivityTimeout, HTTPClient: httpClient,
	})
	if err != nil {
		return err
	}
	return client.Ping(ctx)
}

func (service *ConnectivityService) testNetworkProxy(ctx context.Context, settings *domain.NetworkProxySettings) error {
	token, err := service.configuration.ResolveSecret(ctx, domain.SecretTMDbAPIToken)
	if err != nil {
		return err
	}
	httpClient, err := proxyhttp.NewClient(*settings)
	if err != nil {
		return err
	}
	client, err := tmdb.NewClient(tmdb.ClientOptions{
		BaseURL: service.tmdbBaseURL, APIToken: token, RequestTimeout: connectivityTimeout, HTTPClient: httpClient,
	})
	if err != nil {
		return err
	}
	return client.Ping(ctx)
}

func (service *ConnectivityService) testEmby(ctx context.Context, configuration domain.Configuration, override *domain.EmbyTestConfig) error {
	url := configuration.Settings.Emby.URL
	if override != nil && override.URL != "" {
		url = override.URL
	}
	var apiKeyOverride *string
	if override != nil {
		apiKeyOverride = override.APIKey
	}
	apiKey, err := service.resolveSecret(ctx, domain.SecretEmbyAPIKey, apiKeyOverride)
	if err != nil {
		return err
	}
	client, err := emby.NewClient(emby.ClientOptions{BaseURL: url, APIKey: apiKey, RequestTimeout: connectivityTimeout})
	if err != nil {
		return err
	}
	_, err = client.Libraries(ctx)
	return err
}

func (service *ConnectivityService) testAgent(ctx context.Context, configuration domain.Configuration, override *domain.AgentTestConfig) error {
	settings := configuration.Settings.Agent.WithDefaults()
	var keyOverride *string
	if override != nil {
		settings.Protocol = override.Protocol
		settings.BaseURL = override.BaseURL
		settings.Model = override.Model
		settings.UseNetworkProxy = override.UseNetworkProxy
		keyOverride = override.APIKey
	}
	if settings.Protocol != domain.AgentProtocolOpenAIChatCompletions {
		return &agentapi.Error{Code: "agent_protocol_unsupported"}
	}
	apiKey, err := service.resolveSecret(ctx, domain.SecretAgentAPIKey, keyOverride)
	if err != nil {
		return &agentapi.Error{Code: "agent_not_configured", Cause: err}
	}
	proxySettings := domain.NetworkProxySettings{}
	if settings.UseNetworkProxy {
		proxySettings = configuration.Settings.NetworkProxy
	}
	httpClient, err := proxyhttp.NewClient(proxySettings)
	if err != nil {
		return &agentapi.Error{Code: "agent_not_configured", Cause: err}
	}
	client, err := agentapi.NewClient(agentapi.ClientOptions{
		BaseURL: settings.BaseURL, APIKey: apiKey, Model: settings.Model,
		RequestTimeout: connectivityTimeout, HTTPClient: httpClient,
	})
	if err != nil {
		return err
	}
	return client.ConnectivityTest(ctx)
}

func (service *ConnectivityService) testMediaTools(ctx context.Context, configuration domain.Configuration) error {
	if service.tools == nil {
		return errMediaToolsUnavailable
	}
	paths := configuration.Settings.Paths
	if err := service.tools.CheckExecutable(ctx, paths.FFmpegPath); err != nil {
		return err
	}
	return service.tools.CheckExecutable(ctx, paths.FFprobePath)
}

func (service *ConnectivityService) resolveSecret(ctx context.Context, name string, override *string) (string, error) {
	if override != nil {
		return *override, nil
	}
	return service.configuration.ResolveSecret(ctx, name)
}

var errMediaToolsUnavailable = &Error{Code: "media_tools_unavailable", Message: "media tools are not configured", Cause: ErrUnavailable}

func safeConnectivityMessage(err error) string {
	message := err.Error()
	const maxLength = 300
	if len(message) > maxLength {
		message = message[:maxLength]
	}
	return message
}
