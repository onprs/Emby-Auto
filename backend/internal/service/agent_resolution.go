package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/agentharness"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/agentapi"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/platform/proxyhttp"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

const agentResolutionPageLimit = 100

type AgentResolutionConfiguration interface {
	Load(context.Context) (domain.Configuration, error)
	ResolveSecret(context.Context, string) (string, error)
}

type SubtitleTextInspector interface {
	InspectSubtitleText(context.Context, domain.SubtitleInspectionRequest) (domain.SubtitleInspection, error)
}

type AgentResolutionCatalog interface {
	AutomaticEpisodeMappingEnabled(context.Context, uuid.UUID) (bool, error)
	TryDeterministicEpisodeMapping(context.Context, uuid.UUID) (bool, error)
	PreviewEpisodeMapping(context.Context, domain.EpisodeMappingPlanInput) (domain.EpisodeMappingPreview, error)
	ApplyAgentEpisodeMapping(context.Context, domain.AgentResolution, domain.AgentEpisodeMappingProposal, domain.AgentProposalValidation) (domain.SavedEpisodeMapping, error)
}

type AgentResolutionTMDbSearch interface {
	SearchTV(context.Context, string) ([]domain.TMDbSeriesSearchResult, error)
}

type AgentResolutionService struct {
	queries       *db.Queries
	transactor    *database.Transactor
	operations    *OperationScheduler
	configuration AgentResolutionConfiguration
	catalog       AgentResolutionCatalog
	tmdbSearch    AgentResolutionTMDbSearch
	rssRealtime   RSSRealtimeTargetVerifier
	rssMapping    RSSPreacquisitionMappingAgent
	runner        agentharness.Runner
	now           func() time.Time
	subtitleReader SubtitleTextInspector
}

type AutomaticAgentResolutionRequest struct {
	Capability domain.AgentCapability
	ResourceID uuid.UUID
}

type AgentResolutionCommandResult struct {
	Resolution domain.AgentResolution
	Operation  domain.Operation
}

type AgentRunError struct {
	Code      string
	Message   string
	Retryable bool
	Cause     error
}

func (err *AgentRunError) Error() string {
	if err == nil {
		return "Agent resolution failed"
	}
	return err.Message
}

func (err *AgentRunError) Unwrap() error { return err.Cause }

func NewAgentResolutionService(
	queries *db.Queries,
	transactor *database.Transactor,
	operations *OperationScheduler,
	configuration AgentResolutionConfiguration,
	catalog AgentResolutionCatalog,
	tmdbSearch AgentResolutionTMDbSearch,
) *AgentResolutionService {
	return &AgentResolutionService{
		queries: queries, transactor: transactor, operations: operations, configuration: configuration,
		catalog: catalog, tmdbSearch: tmdbSearch, runner: agentharness.Runner{MaxSteps: 6}, now: time.Now,
	}
}

func (service *AgentResolutionService) WithRSSRealtimeTargetVerifier(verifier RSSRealtimeTargetVerifier) *AgentResolutionService {
	service.rssRealtime = verifier
	return service
}

func (service *AgentResolutionService) WithRSSPreacquisitionMappingAgent(mapping RSSPreacquisitionMappingAgent) *AgentResolutionService {
	service.rssMapping = mapping
	return service
}

func (service *AgentResolutionService) WithSubtitleTextInspector(inspector SubtitleTextInspector) *AgentResolutionService {
	service.subtitleReader = inspector
	return service
}

func (service *AgentResolutionService) CapabilityEnabled(ctx context.Context, capability domain.AgentCapability) (bool, error) {
	configuration, err := service.configuration.Load(ctx)
	if err != nil {
		return false, err
	}
	settings := configuration.Settings.Agent.WithDefaults()
	return settings.Enabled && agentCapabilityEnabled(settings, capability), nil
}

func (service *AgentResolutionService) ReconcileAutomaticRSSReleaseAdjudications(ctx context.Context) (int, error) {
	if service.queries == nil {
		return 0, errors.New("automatic RSS release adjudication reconciliation storage is unavailable")
	}
	enabled, err := service.CapabilityEnabled(ctx, domain.AgentCapabilityRSSReleaseAdjudication)
	if err != nil {
		return 0, err
	}
	if !enabled {
		return 0, nil
	}
	rows, err := service.queries.ListAutomaticRSSAdjudicationBatches(ctx)
	if err != nil {
		return 0, fmt.Errorf("list automatic RSS release adjudication reconciliation candidates: %w", err)
	}
	attempted := 0
	for _, row := range rows {
		result, createErr := service.CreateAutomatic(ctx, AutomaticAgentResolutionRequest{
			Capability: domain.AgentCapabilityRSSReleaseAdjudication,
			ResourceID: repository.UUIDFromPG(row),
		})
		if createErr != nil && !errors.Is(createErr, ErrStateConflict) {
			return attempted, fmt.Errorf("reconcile automatic RSS release adjudication: %w", createErr)
		}
		if createErr == nil && AutomaticAgentResolutionRetryable(result.Resolution) {
			if _, retryErr := service.RetryAutomatic(ctx, result.Resolution.ID, result.Resolution.Version); retryErr != nil && !errors.Is(retryErr, ErrStateConflict) {
				return attempted, fmt.Errorf("resume automatic RSS release adjudication: %w", retryErr)
			}
		}
		attempted++
	}
	return attempted, nil
}

func (service *AgentResolutionService) ReconcileAutomaticEpisodeMappings(ctx context.Context) (int, error) {
	if service.queries == nil {
		return 0, errors.New("automatic episode Mapping reconciliation storage is unavailable")
	}
	rows, err := service.queries.ListAutomaticRSSMappingAcquisitions(ctx)
	if err != nil {
		return 0, fmt.Errorf("list automatic episode Mapping reconciliation candidates: %w", err)
	}
	attempted := 0
	for _, row := range rows {
		result, createErr := service.CreateAutomatic(ctx, AutomaticAgentResolutionRequest{
			Capability: domain.AgentCapabilityEpisodeMapping,
			ResourceID: repository.UUIDFromPG(row),
		})
		if createErr != nil && !errors.Is(createErr, ErrStateConflict) {
			return attempted, fmt.Errorf("reconcile automatic episode Mapping: %w", createErr)
		}
		if createErr == nil && AutomaticAgentResolutionRetryable(result.Resolution) {
			if _, retryErr := service.RetryAutomatic(ctx, result.Resolution.ID, result.Resolution.Version); retryErr != nil && !errors.Is(retryErr, ErrStateConflict) {
				return attempted, fmt.Errorf("resume automatic episode Mapping: %w", retryErr)
			}
		}
		attempted++
	}
	return attempted, nil
}

func (service *AgentResolutionService) ReconcileAutomaticRSSPreacquisitionMappings(ctx context.Context) (int, error) {
	if service.queries == nil || service.rssMapping == nil {
		return 0, errors.New("automatic RSS pre-acquisition Mapping reconciliation is unavailable")
	}
	if _, err := service.queries.ExpireInactiveRSSPreacquisitionMappingScopes(ctx); err != nil {
		return 0, fmt.Errorf("expire inactive RSS pre-acquisition Mapping scopes: %w", err)
	}
	rows, err := service.queries.ListAutomaticRSSPreacquisitionMappingScopes(ctx)
	if err != nil {
		return 0, fmt.Errorf("list automatic RSS pre-acquisition Mapping scopes: %w", err)
	}
	return service.reconcileAutomaticRSSPreacquisitionMappingScopes(ctx, rows)
}

// ReconcileAutomaticRSSPreacquisitionMappingsForSeries closes the normal
// create-subscription race where rss.poll can discover a scope before the
// concurrently scheduled TMDb catalog sync commits its episodes.
func (service *AgentResolutionService) ReconcileAutomaticRSSPreacquisitionMappingsForSeries(
	ctx context.Context,
	seriesID uuid.UUID,
) (int, error) {
	if service.queries == nil || service.rssMapping == nil {
		return 0, errors.New("automatic RSS pre-acquisition Mapping reconciliation is unavailable")
	}
	if seriesID == uuid.Nil {
		return 0, invalidAgentResolution("seriesId", "must be present")
	}
	rows, err := service.queries.ListAutomaticRSSPreacquisitionMappingScopesBySeries(ctx, repository.UUIDToPG(seriesID))
	if err != nil {
		return 0, fmt.Errorf("list automatic RSS pre-acquisition Mapping scopes for series: %w", err)
	}
	return service.reconcileAutomaticRSSPreacquisitionMappingScopes(ctx, rows)
}

func (service *AgentResolutionService) reconcileAutomaticRSSPreacquisitionMappingScopes(
	ctx context.Context,
	rows []pgtype.UUID,
) (int, error) {
	attempted := 0
	for _, row := range rows {
		result, createErr := service.CreateAutomatic(ctx, AutomaticAgentResolutionRequest{
			Capability: domain.AgentCapabilityRSSPreacquisitionMapping, ResourceID: repository.UUIDFromPG(row),
		})
		if createErr != nil {
			var serviceErr *Error
			if errors.As(createErr, &serviceErr) && serviceErr.Code == "agent_resolution_not_required" {
				attempted++
				continue
			}
			if errors.Is(createErr, ErrStateConflict) {
				continue
			}
			return attempted, fmt.Errorf("reconcile automatic RSS pre-acquisition Mapping: %w", createErr)
		}
		if AutomaticAgentResolutionRetryable(result.Resolution) {
			if _, retryErr := service.RetryAutomatic(ctx, result.Resolution.ID, result.Resolution.Version); retryErr != nil && !errors.Is(retryErr, ErrStateConflict) {
				return attempted, fmt.Errorf("resume automatic RSS pre-acquisition Mapping: %w", retryErr)
			}
		}
		attempted++
	}
	return attempted, nil
}

func (service *AgentResolutionService) CreateAutomatic(ctx context.Context, input AutomaticAgentResolutionRequest) (AgentResolutionCommandResult, error) {
	if input.ResourceID == uuid.Nil {
		return AgentResolutionCommandResult{}, invalidAgentResolution("resourceId", "must be present")
	}
	if input.Capability == domain.AgentCapabilityRSSPreacquisitionMapping {
		if service.rssMapping == nil {
			return AgentResolutionCommandResult{}, errors.New("automatic RSS pre-acquisition Mapping is unavailable")
		}
		enabled, err := service.rssMapping.AutomaticRSSPreacquisitionMappingEnabled(ctx, input.ResourceID)
		if err != nil {
			return AgentResolutionCommandResult{}, err
		}
		if !enabled {
			return AgentResolutionCommandResult{}, NewError(
				"automatic_episode_mapping_disabled",
				"automatic episode Mapping is disabled for this RSS subscription",
				ErrStateConflict,
				map[string]any{"capability": input.Capability},
			)
		}
		resolved, err := service.rssMapping.TryDeterministicRSSPreacquisitionMapping(ctx, input.ResourceID)
		if err != nil {
			return AgentResolutionCommandResult{}, err
		}
		if resolved {
			return AgentResolutionCommandResult{}, NewError(
				"agent_resolution_not_required",
				"deterministic RSS pre-acquisition Mapping resolved the scope without Agent assistance",
				ErrStateConflict,
				map[string]any{"capability": input.Capability},
			)
		}
	}
	if input.Capability == domain.AgentCapabilityEpisodeMapping {
		if service.catalog == nil {
			return AgentResolutionCommandResult{}, errors.New("automatic episode Mapping catalog is unavailable")
		}
		enabled, err := service.catalog.AutomaticEpisodeMappingEnabled(ctx, input.ResourceID)
		if err != nil {
			return AgentResolutionCommandResult{}, err
		}
		if !enabled {
			return AgentResolutionCommandResult{}, NewError(
				"automatic_episode_mapping_disabled",
				"automatic episode Mapping is disabled for this RSS subscription",
				ErrStateConflict,
				map[string]any{"capability": input.Capability},
			)
		}
		resolved, err := service.catalog.TryDeterministicEpisodeMapping(ctx, input.ResourceID)
		if err != nil {
			return AgentResolutionCommandResult{}, err
		}
		if resolved {
			return AgentResolutionCommandResult{}, NewError(
				"agent_resolution_not_required",
				"deterministic episode Mapping resolved the acquisition without Agent assistance",
				ErrStateConflict,
				map[string]any{"capability": input.Capability},
			)
		}
	}
	configuration, err := service.configuration.Load(ctx)
	if err != nil {
		return AgentResolutionCommandResult{}, err
	}
	settings := configuration.Settings.Agent.WithDefaults()
	if !settings.Enabled {
		return AgentResolutionCommandResult{}, NewError("agent_disabled", "Agent assistance is disabled", ErrStateConflict, nil)
	}
	if !agentCapabilityEnabled(settings, input.Capability) {
		return AgentResolutionCommandResult{}, NewError("agent_capability_disabled", "the requested Agent capability is disabled", ErrStateConflict, map[string]any{"capability": input.Capability})
	}
	snapshot, err := service.buildAgentContext(ctx, input.Capability, input.ResourceID)
	if err != nil {
		return AgentResolutionCommandResult{}, err
	}
	promptVersion, ok := agentharness.PromptVersion(input.Capability)
	if !ok {
		return AgentResolutionCommandResult{}, invalidAgentResolution("capability", "has no prompt version")
	}
	origin := providerOrigin(settings.BaseURL)
	if origin == "" || strings.TrimSpace(settings.Model) == "" {
		return AgentResolutionCommandResult{}, NewError("agent_not_configured", "Agent Provider configuration is incomplete", ErrStateConflict, nil)
	}
	identityPayload, _ := json.Marshal(struct {
		Capability           domain.AgentCapability `json:"capability"`
		ResourceType         string                 `json:"resourceType"`
		ResourceID           uuid.UUID              `json:"resourceId"`
		ResourceVersion      *int                   `json:"resourceVersion,omitempty"`
		Fingerprint          string                 `json:"fingerprint"`
		ConfigurationVersion int                    `json:"configurationVersion"`
		Protocol             string                 `json:"protocol"`
		ProviderOrigin       string                 `json:"providerOrigin"`
		PromptVersion        string                 `json:"promptVersion"`
		ToolsetVersion       string                 `json:"toolsetVersion"`
		Model                string                 `json:"model"`
	}{
		input.Capability, snapshot.ResourceType, input.ResourceID, snapshot.ResourceVersion,
		hex.EncodeToString(snapshot.Fingerprint[:]), int(configuration.Version), settings.Protocol, origin,
		promptVersion, agentharness.ToolsetVersion, settings.Model,
	})
	identityDigest := sha256.Sum256(identityPayload)
	resolutionID := uuid.NewSHA1(uuid.NameSpaceOID, identityDigest[:])
	idempotencyKey := "agent.resolve:" + hex.EncodeToString(identityDigest[:])
	result := AgentResolutionCommandResult{}
	err = service.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		scheduled, scheduleErr := service.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind: appqueue.KindAgentResolve, ResourceType: "agent_resolution", ResourceID: resolutionID,
			IdempotencyKey: idempotencyKey, MaxAttempts: 3,
			Timeout: time.Duration(settings.RequestTimeoutSeconds) * time.Second,
			Payload: map[string]any{"resolutionId": resolutionID},
		})
		if scheduleErr != nil {
			return scheduleErr
		}
		row, createErr := scope.Queries.CreateAgentResolution(ctx, db.CreateAgentResolutionParams{
			ID: repository.UUIDToPG(resolutionID), OperationID: repository.UUIDToPG(scheduled.Operation.ID),
			ResourceVersion: intPointerToInt32(snapshot.ResourceVersion), Capability: string(input.Capability),
			ResourceType: snapshot.ResourceType, ResourceID: repository.UUIDToPG(input.ResourceID), Trigger: "automatic",
			InputFingerprint: snapshot.Fingerprint[:], ConfigurationVersion: configuration.Version,
			Protocol: settings.Protocol, ProviderOrigin: origin, Model: settings.Model,
			PromptVersion: promptVersion, ToolsetVersion: agentharness.ToolsetVersion,
		})
		if createErr != nil {
			return fmt.Errorf("create Agent resolution: %w", createErr)
		}
		if repository.UUIDFromPG(row.OperationID) != scheduled.Operation.ID || !bytes.Equal(row.InputFingerprint, snapshot.Fingerprint[:]) {
			return NewError("idempotency_conflict", "the Agent resolution identity conflicts with an existing command", ErrStateConflict, nil)
		}
		encoded, _ := json.Marshal(map[string]any{"capability": input.Capability, "status": domain.AgentResolutionQueued})
		resourceType := snapshot.ResourceType
		if _, eventErr := scope.Queries.AppendEvent(ctx, db.AppendEventParams{
			ID: repository.UUIDToPG(uuid.New()), Topic: "agent.resolution_queued", ResourceType: &resourceType,
			ResourceID: repository.UUIDToPG(input.ResourceID), OperationID: repository.UUIDToPG(scheduled.Operation.ID),
			ActorUserID: pgtype.UUID{}, Data: encoded,
		}); eventErr != nil {
			return fmt.Errorf("append Agent resolution event: %w", eventErr)
		}
		result = AgentResolutionCommandResult{Resolution: agentResolutionFromDB(row), Operation: scheduled.Operation}
		return nil
	})
	if err != nil {
		return AgentResolutionCommandResult{}, err
	}
	return result, nil
}

func (service *AgentResolutionService) Run(ctx context.Context, operation domain.Operation) error {
	if operation.Kind != appqueue.KindAgentResolve || operation.ResourceType != "agent_resolution" || operation.ResourceID == uuid.Nil {
		return &AgentRunError{Code: "agent_tool_scope_violation", Message: "Agent operation scope is invalid"}
	}
	row, err := service.queries.GetAgentResolution(ctx, repository.UUIDToPG(operation.ResourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return &AgentRunError{Code: "agent_resolution_not_found", Message: "Agent resolution was not found"}
	}
	if err != nil {
		return err
	}
	resolution := agentResolutionFromDB(row)
	if resolution.Status == domain.AgentResolutionApplied || resolution.Status == domain.AgentResolutionReviewRequired || resolution.Status == domain.AgentResolutionRejected || resolution.Status == domain.AgentResolutionCancelled || resolution.Status == domain.AgentResolutionExpired {
		return nil
	}
	configuration, err := service.configuration.Load(ctx)
	if err != nil {
		return err
	}
	settings := configuration.Settings.Agent.WithDefaults()
	if !settings.Enabled || !agentCapabilityEnabled(settings, resolution.Capability) {
		return service.finishInactive(ctx, resolution.ID, domain.AgentResolutionCancelled, "agent_disabled", "Agent assistance was disabled before execution")
	}
	if resolution.Capability == domain.AgentCapabilityRSSPreacquisitionMapping {
		current, err := service.queries.IsCurrentRSSPreacquisitionMappingScope(ctx, repository.UUIDToPG(resolution.ResourceID))
		if errors.Is(err, pgx.ErrNoRows) {
			return service.finishInactive(ctx, resolution.ID, domain.AgentResolutionExpired, "agent_resolution_stale", "RSS pre-acquisition Mapping scope is no longer available")
		}
		if err != nil {
			return fmt.Errorf("select current RSS pre-acquisition Mapping scope: %w", err)
		}
		if current == nil || !*current {
			return service.finishInactive(ctx, resolution.ID, domain.AgentResolutionExpired, "agent_resolution_stale", "RSS pre-acquisition Mapping scope was superseded")
		}
	}
	if resolution.Capability == domain.AgentCapabilityEpisodeMapping {
		canonical, err := service.queries.IsCanonicalRSSAgentMappingAcquisition(ctx, repository.UUIDToPG(resolution.ResourceID))
		if errors.Is(err, pgx.ErrNoRows) {
			return service.finishInactive(ctx, resolution.ID, domain.AgentResolutionExpired, "agent_resolution_stale", "Agent Mapping resource is no longer available")
		}
		if err != nil {
			return fmt.Errorf("select canonical Agent Mapping acquisition: %w", err)
		}
		if !canonical {
			return service.finishInactive(ctx, resolution.ID, domain.AgentResolutionCancelled, "agent_mapping_superseded", "another acquisition is the RSS Mapping anchor")
		}
	}
	if configuration.Version != int32(resolution.ConfigurationVersion) || settings.Protocol != resolution.Protocol || settings.Model != resolution.Model || providerOrigin(settings.BaseURL) != resolution.ProviderOrigin {
		return service.finishInactive(ctx, resolution.ID, domain.AgentResolutionExpired, "agent_resolution_stale", "Agent configuration changed before execution")
	}
	snapshot, err := service.buildAgentContext(ctx, resolution.Capability, resolution.ResourceID)
	if err != nil {
		return service.finishInactive(ctx, resolution.ID, domain.AgentResolutionExpired, "agent_resolution_stale", "Agent resource context is no longer available")
	}
	if !bytes.Equal(snapshot.Fingerprint[:], resolution.InputFingerprint) || !sameOptionalInt(snapshot.ResourceVersion, resolution.ResourceVersion) {
		return service.finishInactive(ctx, resolution.ID, domain.AgentResolutionExpired, "agent_resolution_stale", "Agent resource context changed before execution")
	}

	if resolution.Status != domain.AgentResolutionProposed {
		started, err := service.queries.StartAgentResolution(ctx, repository.UUIDToPG(resolution.ID))
		if err != nil {
			return err
		}
		resolution = agentResolutionFromDB(started)
		apiKey, err := service.configuration.ResolveSecret(ctx, domain.SecretAgentAPIKey)
		if err != nil {
			return &AgentRunError{Code: "agent_not_configured", Message: "Agent API key is unavailable", Cause: err}
		}
		proxySettings := domain.NetworkProxySettings{}
		if settings.UseNetworkProxy {
			proxySettings = configuration.Settings.NetworkProxy
		}
		httpClient, err := proxyhttp.NewClient(proxySettings)
		if err != nil {
			return &AgentRunError{Code: "agent_not_configured", Message: "Agent network configuration is invalid", Cause: err}
		}
		client, err := agentapi.NewClient(agentapi.ClientOptions{
			BaseURL: settings.BaseURL, APIKey: apiKey, Model: settings.Model,
			RequestTimeout: time.Duration(settings.RequestTimeoutSeconds) * time.Second, HTTPClient: httpClient,
		})
		if err != nil {
			return agentRunError(err)
		}
		startedAt := service.now()
		validateSubmission := service.agentSubmissionValidator(ctx, resolution, snapshot)
		harnessResult, runErr := service.runner.Run(ctx, client, agentharness.Context{
			Capability: resolution.Capability, Resource: snapshot.Resource, Tools: snapshot.Tools,
			MaxSteps: agentToolStepBudget(resolution.Capability, snapshot), ValidateSubmission: validateSubmission,
		})
		if err := service.persistAgentResolutionSteps(ctx, resolution.ID, operation.AttemptCount, harnessResult.Steps); err != nil {
			return err
		}
		if runErr != nil {
			return agentRunError(runErr)
		}
		inputTokens, outputTokens := harnessResult.InputTokens, harnessResult.OutputTokens
		latency := service.now().Sub(startedAt).Milliseconds()
		proposed, err := service.queries.SaveAgentResolutionProposal(ctx, db.SaveAgentResolutionProposalParams{
			Proposal: harnessResult.Proposal, InputTokens: &inputTokens, OutputTokens: &outputTokens,
			ToolCallCount: int32(len(harnessResult.Steps)), LatencyMilliseconds: &latency, ID: repository.UUIDToPG(resolution.ID),
		})
		if err != nil {
			return fmt.Errorf("persist Agent proposal: %w", err)
		}
		resolution = agentResolutionFromDB(proposed)
	}
	currentSnapshot, err := service.buildAgentContext(ctx, resolution.Capability, resolution.ResourceID)
	if err != nil || !bytes.Equal(currentSnapshot.Fingerprint[:], resolution.InputFingerprint) || !sameOptionalInt(currentSnapshot.ResourceVersion, resolution.ResourceVersion) {
		return service.finishInactive(ctx, resolution.ID, domain.AgentResolutionExpired, "agent_resolution_stale", "Agent resource context changed during execution")
	}
	return service.validateAndApply(ctx, configuration, resolution, snapshot)
}

func (service *AgentResolutionService) Get(ctx context.Context, id uuid.UUID) (domain.AgentResolution, error) {
	row, err := service.queries.GetAgentResolution(ctx, repository.UUIDToPG(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AgentResolution{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.AgentResolution{}, err
	}
	return agentResolutionFromDB(row), nil
}

func (service *AgentResolutionService) List(
	ctx context.Context,
	cursor *uuid.UUID,
	limit int,
	status *string,
	capability *string,
	resourceType *string,
	resourceID *uuid.UUID,
) (domain.AgentResolutionPage, error) {
	if limit <= 0 || limit > agentResolutionPageLimit {
		limit = 50
	}
	rows, err := service.queries.ListAgentResolutions(ctx, db.ListAgentResolutionsParams{
		Status: status, Capability: capability, ResourceType: resourceType,
		ResourceID: optionalUUID(resourceID), CursorID: optionalUUID(cursor), PageSize: int32(limit + 1),
	})
	if err != nil {
		return domain.AgentResolutionPage{}, err
	}
	page := domain.AgentResolutionPage{Items: make([]domain.AgentResolution, 0, min(limit, len(rows)))}
	for index, row := range rows {
		if index == limit {
			cursorID := page.Items[len(page.Items)-1].ID
			page.NextCursor = &cursorID
			break
		}
		page.Items = append(page.Items, agentResolutionFromDB(row))
	}
	return page, nil
}

func (service *AgentResolutionService) RetryAutomatic(
	ctx context.Context,
	id uuid.UUID,
	expectedVersion int,
) (AgentResolutionCommandResult, error) {
	if id == uuid.Nil || expectedVersion <= 0 {
		return AgentResolutionCommandResult{}, invalidAgentResolution("retry", "resolution and positive version are required")
	}
	configuration, err := service.configuration.Load(ctx)
	if err != nil {
		return AgentResolutionCommandResult{}, err
	}
	settings := configuration.Settings.Agent.WithDefaults()
	if !settings.Enabled {
		return AgentResolutionCommandResult{}, NewError("agent_disabled", "Agent assistance is disabled", ErrStateConflict, nil)
	}
	result := AgentResolutionCommandResult{}
	err = service.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		current, err := scope.Queries.LockAgentResolution(ctx, repository.UUIDToPG(id))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		currentResolution := agentResolutionFromDB(current)
		if current.Version != int32(expectedVersion) || !AutomaticAgentResolutionRetryable(currentResolution) {
			return NewError("state_conflict", "the automatic Agent resolution cannot be resumed", ErrStateConflict, nil)
		}
		scheduled, err := service.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind: appqueue.KindAgentResolve, ResourceType: "agent_resolution", ResourceID: id,
			IdempotencyKey: fmt.Sprintf("agent.resolve.auto-retry:%s:%d", id, expectedVersion),
			MaxAttempts:    3, Timeout: time.Duration(settings.RequestTimeoutSeconds) * time.Second,
			Payload: map[string]any{"resolutionId": id, "command": "automatic_retry"},
		})
		if err != nil {
			return err
		}
		requeued, err := scope.Queries.RequeueAgentResolution(ctx, db.RequeueAgentResolutionParams{
			OperationID: repository.UUIDToPG(scheduled.Operation.ID), ID: repository.UUIDToPG(id), ExpectedVersion: int32(expectedVersion),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return NewError("state_conflict", "the automatic Agent resolution changed before retry", ErrStateConflict, nil)
		}
		if err != nil {
			return err
		}
		result = AgentResolutionCommandResult{Resolution: agentResolutionFromDB(requeued), Operation: scheduled.Operation}
		return nil
	})
	if err != nil {
		return AgentResolutionCommandResult{}, err
	}
	return result, nil
}

func AutomaticAgentResolutionRetryable(resolution domain.AgentResolution) bool {
	if resolution.Status != domain.AgentResolutionFailed || resolution.Trigger != "automatic" {
		return false
	}
	if len(bytes.TrimSpace(resolution.Proposal)) > 2 {
		return true
	}
	return resolution.ErrorCode == "agent_submission_invalid"
}

func (service *AgentResolutionService) validateAndApply(
	ctx context.Context,
	configuration domain.Configuration,
	resolution domain.AgentResolution,
	snapshot agentContextSnapshot,
) error {
	validation, proposal, err := service.validateProposal(ctx, resolution, snapshot)
	if err != nil {
		return err
	}
	if resolution.Capability == domain.AgentCapabilityCatalogCandidate && validation.Verdict == domain.AgentValidationReviewRequired {
		validationJSON, _ := json.Marshal(validation)
		_, err := service.queries.CompleteAgentResolution(ctx, db.CompleteAgentResolutionParams{
			Status: string(domain.AgentResolutionReviewRequired), Validation: validationJSON, ID: repository.UUIDToPG(resolution.ID),
		})
		return err
	}
	if validation.Verdict != domain.AgentValidationAutoApplicable {
		return service.failAutomaticProposal(ctx, resolution.ID, validation)
	}
	if !agentCapabilityAutomatic(configuration.Settings.Agent, resolution.Capability) {
		return service.failAutomaticProposal(ctx, resolution.ID, domain.AgentProposalValidation{
			Verdict: domain.AgentValidationInvalid, ReasonCodes: []string{"agent_capability_not_automatic"},
		})
	}

	realtimeCheckID, err := service.verifyRSSProposalTargets(ctx, resolution.Capability, proposal)
	if err != nil {
		return err
	}

	switch resolution.Capability {
	case domain.AgentCapabilityRSSReleaseAdjudication:
		return service.applyRSSReleaseAdjudication(ctx, resolution, proposal.(domain.AgentRSSReleaseAdjudicationProposal), validation, realtimeCheckID)
	case domain.AgentCapabilityRSSCoordinate:
		return service.applyRSSCoordinate(ctx, resolution, proposal.(domain.AgentRSSCoordinateProposal), validation, realtimeCheckID)
	case domain.AgentCapabilityDownloadFileResolution:
		return service.applyDownloadFileResolution(ctx, resolution, proposal.(domain.AgentDownloadFileResolutionProposal), validation)
	case domain.AgentCapabilityRSSPreacquisitionMapping:
		return service.rssMapping.ApplyAgentRSSPreacquisitionMapping(ctx, resolution, proposal.(domain.AgentRSSPreacquisitionMappingProposal), validation)
	case domain.AgentCapabilityEpisodeMapping:
		_, err := service.catalog.ApplyAgentEpisodeMapping(ctx, resolution, proposal.(domain.AgentEpisodeMappingProposal), validation)
		return err
	case domain.AgentCapabilitySubtitleVideoMatch:
		return service.applySubtitleVideoMatch(ctx, resolution, proposal.(domain.AgentSubtitleVideoMatchProposal), validation)
	default:
		return service.failAutomaticProposal(ctx, resolution.ID, domain.AgentProposalValidation{
			Verdict: domain.AgentValidationInvalid, ReasonCodes: []string{"agent_capability_unsupported"},
		})
	}
}

func (service *AgentResolutionService) failAutomaticProposal(
	ctx context.Context,
	id uuid.UUID,
	validation domain.AgentProposalValidation,
) error {
	validationJSON, _ := json.Marshal(validation)
	code := "agent_proposal_invalid"
	message := "Agent proposal failed automatic validation"
	if validation.Verdict == domain.AgentValidationReviewRequired {
		code = "agent_proposal_unresolved"
		message = "Agent proposal could not be resolved automatically"
	}
	_, err := service.queries.FailValidatedAgentResolution(ctx, db.FailValidatedAgentResolutionParams{
		Validation: validationJSON, ErrorCode: &code, ErrorMessage: &message, ID: repository.UUIDToPG(id),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func (service *AgentResolutionService) validateProposal(
	ctx context.Context,
	resolution domain.AgentResolution,
	snapshot agentContextSnapshot,
) (domain.AgentProposalValidation, any, error) {
	invalid := func(codes ...string) domain.AgentProposalValidation {
		return domain.AgentProposalValidation{Verdict: domain.AgentValidationInvalid, ReasonCodes: codes}
	}
	review := func(codes ...string) domain.AgentProposalValidation {
		return domain.AgentProposalValidation{Verdict: domain.AgentValidationReviewRequired, ReasonCodes: codes}
	}
	auto := func() domain.AgentProposalValidation {
		return domain.AgentProposalValidation{Verdict: domain.AgentValidationAutoApplicable, ReasonCodes: []string{}}
	}
	switch resolution.Capability {
	case domain.AgentCapabilityRSSPreacquisitionMapping:
		var proposal domain.AgentRSSPreacquisitionMappingProposal
		if err := strictJSON(resolution.Proposal, &proposal); err != nil || proposal.ScopeID != resolution.ResourceID ||
			proposal.SourceSeason <= 0 || proposal.SourceEpisode <= 0 || proposal.TargetSeason <= 0 || proposal.TargetEpisode <= 0 {
			return invalid("agent_tool_scope_violation"), proposal, nil
		}
		if _, ok := snapshot.RSSMappingSources[domain.EpisodeCoordinate{Season: proposal.SourceSeason, Episode: proposal.SourceEpisode}]; !ok {
			return invalid("agent_tool_scope_violation"), proposal, nil
		}
		if _, ok := snapshot.RSSMappingTargets[domain.EpisodeCoordinate{Season: proposal.TargetSeason, Episode: proposal.TargetEpisode}]; !ok {
			return invalid("agent_tool_scope_violation"), proposal, nil
		}
		if len(proposal.EvidenceCodes) == 0 || len(proposal.EvidenceCodes) > 16 {
			return invalid("agent_mapping_evidence_invalid"), proposal, nil
		}
		for _, code := range proposal.EvidenceCodes {
			if strings.TrimSpace(code) == "" || len(code) > 128 {
				return invalid("agent_mapping_evidence_invalid"), proposal, nil
			}
		}
		preview, err := service.rssMapping.PreviewRSSPreacquisitionMapping(
			ctx, proposal.ScopeID,
			domain.EpisodeCoordinate{Season: proposal.SourceSeason, Episode: proposal.SourceEpisode},
			domain.EpisodeCoordinate{Season: proposal.TargetSeason, Episode: proposal.TargetEpisode},
		)
		if err != nil {
			return invalid(serviceCode(err, "agent_mapping_preview_incomplete")), proposal, nil
		}
		if len(preview) == 0 {
			return invalid("agent_mapping_preview_incomplete"), proposal, nil
		}
		for _, row := range preview {
			if row.Status != domain.MappingMapped || row.TargetEpisodeID == uuid.Nil || row.TargetSeason <= 0 {
				return invalid("agent_mapping_preview_incomplete"), proposal, nil
			}
		}
		if proposal.Decision != "resolved" {
			return review("agent_requested_review"), proposal, nil
		}
		return auto(), proposal, nil
	case domain.AgentCapabilityEpisodeMapping:
		var proposal domain.AgentEpisodeMappingProposal
		if err := strictJSON(resolution.Proposal, &proposal); err != nil {
			return invalid("agent_submission_invalid"), proposal, nil
		}
		if proposal.AcquisitionID != resolution.ResourceID || proposal.SourceFileID == uuid.Nil || proposal.TargetSeason <= 0 || proposal.TargetEpisode <= 0 {
			return invalid("agent_tool_scope_violation"), proposal, nil
		}
		preview, err := service.catalog.PreviewEpisodeMapping(ctx, domain.EpisodeMappingPlanInput{
			AcquisitionID: proposal.AcquisitionID,
			Anchor:        domain.EpisodeMappingAnchorInput{SourceFileID: proposal.SourceFileID, Target: domain.EpisodeCoordinate{Season: proposal.TargetSeason, Episode: proposal.TargetEpisode}},
		})
		if err != nil {
			return invalid(serviceCode(err, "agent_mapping_preview_incomplete")), proposal, nil
		}
		if len(preview.Rows) == 0 {
			return invalid("agent_mapping_preview_incomplete"), proposal, nil
		}
		for _, row := range preview.Rows {
			if row.Status != domain.MappingMapped || row.TargetEpisodeID == uuid.Nil || row.TargetSeason <= 0 {
				return review("agent_mapping_preview_incomplete"), proposal, nil
			}
		}
		if proposal.Decision != "resolved" {
			return review("agent_requested_review"), proposal, nil
		}
		return auto(), proposal, nil
	case domain.AgentCapabilityRSSReleaseAdjudication:
		var proposal domain.AgentRSSReleaseAdjudicationProposal
		if err := strictJSON(resolution.Proposal, &proposal); err != nil || proposal.BatchID != resolution.ResourceID {
			return invalid("agent_proposal_invalid"), proposal, nil
		}
		validation := validateRSSReleaseAdjudicationProposal(proposal, snapshot)
		return validation, proposal, nil
	case domain.AgentCapabilityRSSCoordinate:
		var proposal domain.AgentRSSCoordinateProposal
		if err := strictJSON(resolution.Proposal, &proposal); err != nil || proposal.EntryID != resolution.ResourceID || proposal.SourceSeason <= 0 || proposal.SourceEpisode <= 0 {
			return invalid("agent_proposal_invalid"), proposal, nil
		}
		if proposal.Decision != "resolved" {
			return review("agent_requested_review"), proposal, nil
		}
		return auto(), proposal, nil
	case domain.AgentCapabilityDownloadFileResolution:
		var proposal domain.AgentDownloadFileResolutionProposal
		if err := strictJSON(resolution.Proposal, &proposal); err != nil {
			return invalid("agent_proposal_invalid"), proposal, nil
		}
		validation := validateDownloadFileProposal(proposal, snapshot)
		return validation, proposal, nil
	case domain.AgentCapabilitySubtitleVideoMatch:
		var proposal domain.AgentSubtitleVideoMatchProposal
		if err := strictJSON(resolution.Proposal, &proposal); err != nil {
			return invalid("agent_proposal_invalid"), proposal, nil
		}
		validation := validateSubtitleVideoMatchProposal(proposal, snapshot)
		return validation, proposal, nil
	case domain.AgentCapabilityCatalogCandidate:
		var proposal domain.AgentCatalogCandidateProposal
		if err := strictJSON(resolution.Proposal, &proposal); err != nil || strings.TrimSpace(proposal.Query) == "" || len(proposal.CandidateIDs) == 0 {
			return invalid("agent_proposal_invalid"), proposal, nil
		}
		queryIDs := snapshot.AllowedCatalogQueries[strings.ToLower(strings.TrimSpace(proposal.Query))]
		if len(queryIDs) == 0 {
			return invalid("agent_catalog_query_scope_violation"), proposal, nil
		}
		for _, id := range proposal.CandidateIDs {
			if _, allowed := queryIDs[id]; !allowed {
				return invalid("agent_tool_scope_violation"), proposal, nil
			}
		}
		return review("catalog_candidate_requires_user_confirmation"), proposal, nil
	default:
		return invalid("agent_capability_unsupported"), nil, nil
	}
}

func (service *AgentResolutionService) agentSubmissionValidator(
	ctx context.Context,
	resolution domain.AgentResolution,
	snapshot agentContextSnapshot,
) func(json.RawMessage) error {
	return func(raw json.RawMessage) error {
		candidate := resolution
		candidate.Proposal = raw
		validation, _, err := service.validateProposal(ctx, candidate, snapshot)
		if err != nil {
			return err
		}
		if resolution.Capability == domain.AgentCapabilityCatalogCandidate && validation.Verdict == domain.AgentValidationReviewRequired {
			return nil
		}
		if validation.Verdict != domain.AgentValidationAutoApplicable {
			code := "agent_submission_invalid"
			if len(validation.ReasonCodes) > 0 {
				code = validation.ReasonCodes[0]
			}
			return &agentharness.SubmissionValidationError{Code: code}
		}
		return nil
	}
}

func validateRSSReleaseAdjudicationProposal(proposal domain.AgentRSSReleaseAdjudicationProposal, snapshot agentContextSnapshot) domain.AgentProposalValidation {
	invalid := func(code string) domain.AgentProposalValidation {
		return domain.AgentProposalValidation{Verdict: domain.AgentValidationInvalid, ReasonCodes: []string{code}}
	}
	if proposal.Decision != "resolved" {
		return invalid("rss_adjudication_decision_invalid")
	}
	if len(snapshot.RSSAdjudicationEntries) == 0 || len(snapshot.RSSAdjudicationEntries) > rssAdjudicationBatchSize || len(proposal.ScopedEntryIDs) != len(snapshot.RSSAdjudicationEntries) || len(proposal.Entries) != len(snapshot.RSSAdjudicationEntries) {
		return invalid("rss_adjudication_scope_incomplete")
	}
	scoped := make(map[uuid.UUID]struct{}, len(proposal.ScopedEntryIDs))
	for _, id := range proposal.ScopedEntryIDs {
		if id == uuid.Nil {
			return invalid("rss_adjudication_scope_invalid")
		}
		if _, duplicate := scoped[id]; duplicate {
			return invalid("rss_adjudication_scope_duplicate")
		}
		if _, allowed := snapshot.RSSAdjudicationEntries[id]; !allowed {
			return invalid("agent_tool_scope_violation")
		}
		scoped[id] = struct{}{}
	}
	seen := make(map[uuid.UUID]struct{}, len(proposal.Entries))
	selectedCoordinates := make(map[[2]int]struct{})
	currentCoordinateCounts := make(map[[2]int]int)
	blockedCoordinates := make(map[[2]int]struct{})
	for _, entry := range snapshot.RSSAdjudicationEntries {
		if entry.Deterministic.Downloadable {
			coordinate := [2]int{entry.Deterministic.SourceSeason, entry.Deterministic.SourceEpisode}
			currentCoordinateCounts[coordinate]++
		}
	}
	for _, historical := range snapshot.RSSAdjudicationHistory {
		if historical.SourceSeason == nil || historical.SourceEpisode == nil {
			continue
		}
		if historical.Imported || historical.WorkflowStatus == string(domain.RSSEnqueueing) || historical.WorkflowStatus == string(domain.RSSEnqueued) {
			blockedCoordinates[[2]int{*historical.SourceSeason, *historical.SourceEpisode}] = struct{}{}
		}
	}
	unsafeReplacement := false
	for _, item := range proposal.Entries {
		entry, allowed := snapshot.RSSAdjudicationEntries[item.EntryID]
		if !allowed {
			return invalid("agent_tool_scope_violation")
		}
		if _, duplicate := seen[item.EntryID]; duplicate {
			return invalid("rss_adjudication_entry_duplicate")
		}
		seen[item.EntryID] = struct{}{}
		if len(item.EvidenceCodes) == 0 || len(item.EvidenceCodes) > 16 {
			return invalid("rss_adjudication_evidence_invalid")
		}
		for _, code := range item.EvidenceCodes {
			if strings.TrimSpace(code) == "" || len(code) > 128 {
				return invalid("rss_adjudication_evidence_invalid")
			}
		}
		if item.RelatedEntryID != nil {
			if *item.RelatedEntryID == uuid.Nil || *item.RelatedEntryID == item.EntryID {
				return invalid("rss_adjudication_relation_invalid")
			}
			if _, current := snapshot.RSSAdjudicationEntries[*item.RelatedEntryID]; !current {
				if _, historical := snapshot.RSSAdjudicationHistory[*item.RelatedEntryID]; !historical {
					return invalid("agent_tool_scope_violation")
				}
			}
		}
		switch item.Disposition {
		case "select":
			if item.SourceSeason == nil || item.SourceEpisode == nil || *item.SourceSeason <= 0 || *item.SourceEpisode <= 0 || *item.SourceSeason > math.MaxInt32 || *item.SourceEpisode > math.MaxInt32 {
				return invalid("rss_adjudication_coordinate_invalid")
			}
			for _, reason := range append(append([]string{}, entry.RejectionReasons...), entry.Deterministic.RejectionReasons...) {
				if reason == "download_uri_missing" || reason == "title_excluded" || reason == "title_include_mismatch" || reason == "episode_range_batch" ||
					reason == rssTargetInLibraryReason || reason == rssTargetImportedReason || reason == rssTargetProcessingReason {
					return invalid("rss_adjudication_hard_rejection")
				}
			}
			coordinate := [2]int{*item.SourceSeason, *item.SourceEpisode}
			if _, duplicate := selectedCoordinates[coordinate]; duplicate {
				return invalid("rss_adjudication_coordinate_duplicate")
			}
			selectedCoordinates[coordinate] = struct{}{}
			for _, historical := range snapshot.RSSAdjudicationHistory {
				if historical.SourceSeason == nil || historical.SourceEpisode == nil || *historical.SourceSeason != coordinate[0] || *historical.SourceEpisode != coordinate[1] {
					continue
				}
				if historical.Imported || historical.WorkflowStatus == string(domain.RSSEnqueueing) || historical.WorkflowStatus == string(domain.RSSEnqueued) {
					unsafeReplacement = true
				}
			}
		case "ignore":
			if item.SourceSeason != nil || item.SourceEpisode != nil {
				return invalid("rss_adjudication_ignored_coordinate_present")
			}
		default:
			return invalid("rss_adjudication_disposition_invalid")
		}
	}
	if len(seen) != len(scoped) {
		return invalid("rss_adjudication_scope_incomplete")
	}
	if unsafeReplacement {
		return invalid("rss_adjudication_historical_coordinate_conflict")
	}
	for coordinate, count := range currentCoordinateCounts {
		if count < 2 {
			continue
		}
		_, selected := selectedCoordinates[coordinate]
		_, blocked := blockedCoordinates[coordinate]
		if blocked && selected {
			return invalid("rss_adjudication_historical_coordinate_conflict")
		}
		if !blocked && !selected {
			return invalid("rss_adjudication_duplicate_coordinate_unresolved")
		}
	}
	return domain.AgentProposalValidation{Verdict: domain.AgentValidationAutoApplicable, ReasonCodes: []string{}}
}

func validateDownloadFileProposal(proposal domain.AgentDownloadFileResolutionProposal, snapshot agentContextSnapshot) domain.AgentProposalValidation {
	invalid := func(code string) domain.AgentProposalValidation {
		return domain.AgentProposalValidation{Verdict: domain.AgentValidationInvalid, ReasonCodes: []string{code}}
	}
	if len(proposal.Videos) == 0 {
		return invalid("download_file_resolution_invalid")
	}
	seenCoordinates := map[[2]int]struct{}{}
	selectedVideos := map[uuid.UUID]struct{}{}
	selectedFiles := map[uuid.UUID]struct{}{}
	for _, video := range proposal.Videos {
		file, owned := snapshot.Files[video.FileID]
		if !owned || file.MediaKind != string(domain.MediaVideo) || video.SourceSeason <= 0 || video.SourceEpisode <= 0 {
			return invalid("download_file_scope_violation")
		}
		if snapshot.DefaultSourceEpisode != nil && (video.SourceSeason != snapshot.DefaultSourceSeason || video.SourceEpisode != *snapshot.DefaultSourceEpisode) {
			return invalid("download_single_episode_coordinate_mismatch")
		}
		if _, duplicate := selectedFiles[video.FileID]; duplicate {
			return invalid("download_file_duplicate")
		}
		coordinate := [2]int{video.SourceSeason, video.SourceEpisode}
		if _, duplicate := seenCoordinates[coordinate]; duplicate {
			return invalid("download_coordinate_duplicate")
		}
		seenCoordinates[coordinate] = struct{}{}
		selectedVideos[video.FileID] = struct{}{}
		selectedFiles[video.FileID] = struct{}{}
	}
	subtitleCounts := map[uuid.UUID]int{}
	for _, subtitle := range proposal.Subtitles {
		file, owned := snapshot.Files[subtitle.FileID]
		if !owned || file.MediaKind != string(domain.MediaSubtitle) {
			return invalid("download_subtitle_scope_violation")
		}
		if _, duplicate := selectedFiles[subtitle.FileID]; duplicate {
			return invalid("download_file_duplicate")
		}
		if _, video := selectedVideos[subtitle.VideoFileID]; !video {
			return invalid("download_subtitle_video_invalid")
		}
		subtitleCounts[subtitle.VideoFileID]++
		if subtitleCounts[subtitle.VideoFileID] > 8 {
			return invalid("download_subtitle_limit_exceeded")
		}
		selectedFiles[subtitle.FileID] = struct{}{}
	}
	if proposal.Decision != "resolved" {
		return domain.AgentProposalValidation{Verdict: domain.AgentValidationReviewRequired, ReasonCodes: []string{"agent_requested_review"}}
	}
	return domain.AgentProposalValidation{Verdict: domain.AgentValidationAutoApplicable, ReasonCodes: []string{}}
}

func (service *AgentResolutionService) verifyRSSProposalTargets(
	ctx context.Context,
	capability domain.AgentCapability,
	proposal any,
) (uuid.UUID, error) {
	if service.rssRealtime == nil {
		return uuid.Nil, nil
	}
	var (
		checkID uuid.UUID
		err     error
	)
	switch capability {
	case domain.AgentCapabilityRSSReleaseAdjudication:
		adjudication := proposal.(domain.AgentRSSReleaseAdjudicationProposal)
		batch, queryErr := service.queries.GetAgentRSSAdjudicationBatch(ctx, repository.UUIDToPG(adjudication.BatchID))
		if queryErr != nil {
			return uuid.Nil, &AgentRunError{
				Code: "rss_realtime_storage_unavailable", Message: "the RSS adjudication scope is unavailable", Retryable: true, Cause: queryErr,
			}
		}
		coordinates := make([]domain.EpisodeCoordinate, 0, len(adjudication.Entries))
		for _, item := range adjudication.Entries {
			if item.Disposition == "select" && item.SourceSeason != nil && item.SourceEpisode != nil {
				coordinates = append(coordinates, domain.EpisodeCoordinate{Season: *item.SourceSeason, Episode: *item.SourceEpisode})
			}
		}
		checkID, err = service.rssRealtime.VerifyCoordinates(
			ctx, repository.UUIDFromPG(batch.SubscriptionID), coordinates,
		)
	case domain.AgentCapabilityRSSCoordinate:
		coordinate := proposal.(domain.AgentRSSCoordinateProposal)
		entry, queryErr := service.queries.GetAgentRSSContext(ctx, repository.UUIDToPG(coordinate.EntryID))
		if queryErr != nil {
			return uuid.Nil, &AgentRunError{
				Code: "rss_realtime_storage_unavailable", Message: "the RSS coordinate scope is unavailable", Retryable: true, Cause: queryErr,
			}
		}
		if !entry.MappingProfileID.Valid {
			return uuid.Nil, nil
		}
		checkID, err = service.rssRealtime.VerifyCoordinates(ctx, repository.UUIDFromPG(entry.SubscriptionID), []domain.EpisodeCoordinate{{
			Season: coordinate.SourceSeason, Episode: coordinate.SourceEpisode,
		}})
	default:
		return uuid.Nil, nil
	}
	if err == nil {
		return checkID, nil
	}
	var verificationErr *RSSRealtimeVerificationError
	if errors.As(err, &verificationErr) {
		return uuid.Nil, &AgentRunError{
			Code: verificationErr.Code, Message: verificationErr.Message, Retryable: verificationErr.Retryable, Cause: verificationErr,
		}
	}
	return uuid.Nil, &AgentRunError{
		Code: "emby_realtime_check_failed", Message: "real-time Emby target verification failed", Retryable: true, Cause: err,
	}
}

func (service *AgentResolutionService) applyRSSReleaseAdjudication(
	ctx context.Context,
	resolution domain.AgentResolution,
	proposal domain.AgentRSSReleaseAdjudicationProposal,
	validation domain.AgentProposalValidation,
	realtimeCheckID uuid.UUID,
) error {
	validationJSON, _ := json.Marshal(validation)
	return service.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		lockedResolution, err := scope.Queries.LockAgentResolution(ctx, repository.UUIDToPG(resolution.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Agent RSS adjudication resolution: %w", err)
		}
		if lockedResolution.Version != int32(resolution.Version) || (lockedResolution.Status != string(domain.AgentResolutionProposed) && lockedResolution.Status != string(domain.AgentResolutionReviewRequired)) {
			return NewError("state_conflict", "the Agent resolution changed before RSS adjudication was applied", ErrStateConflict, nil)
		}
		batch, err := scope.Queries.LockRSSAdjudicationBatch(ctx, repository.UUIDToPG(proposal.BatchID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock RSS adjudication batch: %w", err)
		}
		if batch.Status != "pending" {
			return NewError("agent_resolution_stale", "the RSS adjudication batch is no longer pending", ErrStateConflict, nil)
		}
		batchContext, err := scope.Queries.GetAgentRSSAdjudicationBatch(ctx, repository.UUIDToPG(proposal.BatchID))
		if err != nil {
			return fmt.Errorf("reload RSS adjudication batch: %w", err)
		}
		if !batchContext.SubscriptionEnabled || batchContext.SubscriptionCompletedAt.Valid || batchContext.SubscriptionDeletedAt.Valid {
			return NewError("agent_resolution_stale", "the RSS subscription is no longer active", ErrStateConflict, nil)
		}
		entries, err := scope.Queries.LockRSSAdjudicationEntries(ctx, repository.UUIDToPG(proposal.BatchID))
		if err != nil {
			return fmt.Errorf("lock RSS adjudication entries: %w", err)
		}
		if len(entries) != len(proposal.Entries) || len(entries) != int(batch.EntryCount) {
			return NewError("agent_resolution_stale", "the RSS adjudication scope changed", ErrStateConflict, nil)
		}
		proposalByID := make(map[uuid.UUID]domain.AgentRSSReleaseDisposition, len(proposal.Entries))
		for _, item := range proposal.Entries {
			proposalByID[item.EntryID] = item
		}
		source := string(domain.DecisionSourceAgentAuto)
		selectedCount := 0
		ignoredCount := 0
		deterministicIgnoredCount := 0
		for _, entry := range entries {
			entryID := repository.UUIDFromPG(entry.ID)
			item, ok := proposalByID[entryID]
			if !ok || entry.AdjudicationState != "pending" || (entry.Status != string(domain.RSSDiscovered) && entry.Status != string(domain.RSSEnqueueFailed)) {
				return NewError("agent_resolution_stale", "an RSS adjudication entry changed before apply", ErrStateConflict, map[string]any{"entryId": entryID})
			}
			state := "ignored"
			entrySource := source
			rejectionReasons := []string{"agent_ignored"}
			relatedEntryID := optionalUUID(item.RelatedEntryID)
			occupancy := rssTargetOccupancy{}
			if item.Disposition == "select" {
				occupancy, err = lockRSSMappedTargetOccupancyWithRealtimeCheck(
					ctx,
					scope,
					repository.UUIDFromPG(batchContext.SubscriptionID),
					*item.SourceSeason,
					*item.SourceEpisode,
					entryID,
					realtimeCheckID,
				)
				if err != nil {
					return err
				}
				if occupancy.Reason == "" {
					state = "selected"
					rejectionReasons = []string{}
				} else {
					entrySource = string(domain.DecisionSourceDeterministic)
					rejectionReasons = []string{occupancy.Reason}
					relatedEntryID = pgtype.UUID{}
					deterministicIgnoredCount++
				}
			} else if item.Disposition != "ignore" {
				return NewError("agent_proposal_invalid", "the RSS adjudication still contains unresolved entries", ErrStateConflict, nil)
			}
			updated, err := scope.Queries.ApplyAgentRSSAdjudicationEntry(ctx, db.ApplyAgentRSSAdjudicationEntryParams{
				AdjudicationState: state, AdjudicationSource: &entrySource,
				AdjudicationResolutionID: repository.UUIDToPG(resolution.ID), RelatedEntryID: relatedEntryID,
				SourceSeason: intPointerToInt32(item.SourceSeason), SourceEpisode: intPointerToInt32(item.SourceEpisode),
				RejectionReasons: rejectionReasons,
				ID:               entry.ID, AdjudicationBatchID: repository.UUIDToPG(proposal.BatchID),
			})
			if err != nil {
				return fmt.Errorf("apply RSS entry adjudication: %w", err)
			}
			if state == "ignored" {
				ignoredCount++
				if occupancy.Reason != "" {
					if _, err := markRSSEntryTargetOccupiedInTx(ctx, scope, entryID, uuid.Nil, occupancy); err != nil {
						return err
					}
				}
				if err := appendResourceEvent(ctx, scope.Queries, "rss_entry", entryID, uuid.Nil, uuid.Nil, "rss.entry.ignored", map[string]any{
					"agentResolutionId": resolution.ID, "adjudicationBatchId": proposal.BatchID, "source": entrySource,
					"reasonCode": rejectionReasons[0],
				}); err != nil {
					return err
				}
				continue
			}
			selectedCount++
			enqueueing, err := scope.Queries.MarkRSSEntryEnqueueing(ctx, entry.ID)
			if err != nil {
				return fmt.Errorf("mark adjudicated RSS entry enqueueing: %w", err)
			}
			sourcePayload, err := json.Marshal(map[string]any{
				"rssEntryId": entryID, "identityKey": updated.IdentityKey,
				"sourceSeason": *item.SourceSeason, "sourceEpisode": *item.SourceEpisode,
			})
			if err != nil {
				return fmt.Errorf("encode adjudicated RSS acquisition payload: %w", err)
			}
			acquisition, err := scope.Queries.UpsertRSSAcquisition(ctx, db.UpsertRSSAcquisitionParams{
				ID: repository.UUIDToPG(uuid.New()), SeriesID: batchContext.SeriesID, MappingProfileID: batchContext.MappingProfileID,
				RssEntryID: entry.ID, SourcePayload: sourcePayload,
			})
			if err != nil {
				return fmt.Errorf("create adjudicated RSS acquisition: %w", err)
			}
			download, err := scope.Queries.CreateRSSDownloadAttempt(ctx, db.CreateRSSDownloadAttemptParams{
				ID: repository.UUIDToPG(uuid.New()), AcquisitionID: acquisition.ID,
			})
			if err != nil {
				return fmt.Errorf("create adjudicated RSS download: %w", err)
			}
			scheduled, err := service.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
				Kind: appqueue.KindDownloadEnqueue, ResourceType: "download", ResourceID: repository.UUIDFromPG(download.ID),
				IdempotencyKey: "download.enqueue:" + repository.UUIDFromPG(download.ID).String(), MaxAttempts: 5, Timeout: 2 * time.Minute,
				Payload: map[string]any{"defaultSeason": *item.SourceSeason, "defaultEpisode": *item.SourceEpisode, "singleEpisode": true},
			})
			if err != nil {
				return fmt.Errorf("schedule adjudicated RSS download: %w", err)
			}
			if err := appendResourceEvent(ctx, scope.Queries, "rss_entry", entryID, scheduled.Operation.ID, uuid.Nil, "rss.entry.enqueueing", map[string]any{
				"status": domain.RSSEnqueueing, "enqueueAttempt": enqueueing.EnqueueAttempts,
				"acquisitionId": repository.UUIDFromPG(acquisition.ID), "downloadId": repository.UUIDFromPG(download.ID),
				"agentResolutionId": resolution.ID, "adjudicationBatchId": proposal.BatchID,
			}); err != nil {
				return err
			}
		}
		if _, err := scope.Queries.CompleteRSSAdjudicationBatch(ctx, repository.UUIDToPG(proposal.BatchID)); err != nil {
			return fmt.Errorf("complete RSS adjudication batch: %w", err)
		}
		if err := NewRSSWorkflow(service.queries, service.transactor, service.operations).completeRSSSubscriptionAtFulfillmentInTx(
			ctx,
			scope,
			repository.UUIDFromPG(batchContext.SubscriptionID),
			uuid.Nil,
			"agent_adjudication",
		); err != nil {
			return err
		}
		if _, err := scope.Queries.CompleteAgentResolution(ctx, db.CompleteAgentResolutionParams{
			Status: string(domain.AgentResolutionApplied), Validation: validationJSON, ID: repository.UUIDToPG(resolution.ID),
		}); err != nil {
			return fmt.Errorf("complete Agent RSS adjudication: %w", err)
		}
		return appendResourceEvent(ctx, scope.Queries, "rss_adjudication_batch", proposal.BatchID, uuid.Nil, uuid.Nil, "rss.adjudication_applied", map[string]any{
			"agentResolutionId": resolution.ID, "selectedCount": selectedCount, "ignoredCount": ignoredCount,
			"deterministicIgnoredCount": deterministicIgnoredCount, "source": source,
		})
	})
}

func (service *AgentResolutionService) applyRSSCoordinate(
	ctx context.Context,
	resolution domain.AgentResolution,
	proposal domain.AgentRSSCoordinateProposal,
	validation domain.AgentProposalValidation,
	realtimeCheckID uuid.UUID,
) error {
	validationJSON, _ := json.Marshal(validation)
	return service.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		lockedResolution, err := scope.Queries.LockAgentResolution(ctx, repository.UUIDToPG(resolution.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Agent RSS resolution: %w", err)
		}
		if lockedResolution.Version != int32(resolution.Version) || (lockedResolution.Status != string(domain.AgentResolutionProposed) && lockedResolution.Status != string(domain.AgentResolutionReviewRequired)) {
			return NewError("state_conflict", "the Agent resolution changed before RSS coordinate was applied", ErrStateConflict, nil)
		}
		entry, err := scope.Queries.LockRSSEntryForAgentCoordinate(ctx, repository.UUIDToPG(resolution.ResourceID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Agent RSS entry: %w", err)
		}
		analysis := domain.AnalyzeRSSRelease(entry.Title, stringValue(entry.DownloadUri), int(entry.DefaultSourceSeason), entry.IncludeKeywords, entry.ExcludeKeywords)
		softReason := false
		for _, reason := range analysis.RejectionReasons {
			switch reason {
			case "episode_not_detected", "episode_ambiguous":
				softReason = true
			default:
				return NewError("agent_resolution_stale", "RSS hard rejection still prevents coordinate recovery", ErrStateConflict, map[string]any{"reasonCode": reason})
			}
		}
		if !softReason || (entry.Status != string(domain.RSSDiscovered) && entry.Status != string(domain.RSSEnqueueFailed)) {
			return NewError("agent_resolution_stale", "the RSS entry is no longer waiting for coordinate recovery", ErrStateConflict, map[string]any{"status": entry.Status})
		}
		source := string(domain.DecisionSourceAgentAuto)
		season, episode := int32(proposal.SourceSeason), int32(proposal.SourceEpisode)
		if _, err := scope.Queries.ApplyAgentRSSCoordinate(ctx, db.ApplyAgentRSSCoordinateParams{
			SourceSeason: &season, SourceEpisode: &episode, CoordinateSource: &source,
			AgentResolutionID: repository.UUIDToPG(resolution.ID), ID: entry.ID,
		}); err != nil {
			return fmt.Errorf("apply Agent RSS coordinate: %w", err)
		}
		if !entry.MappingProfileID.Valid {
			if service.operations == nil {
				return fmt.Errorf("schedule RSS mapping continuation: operation scheduler is unavailable")
			}
			scheduled, err := service.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
				Kind: appqueue.KindRSSPoll, ResourceType: "rss_subscription", ResourceID: repository.UUIDFromPG(entry.SubscriptionID),
				IdempotencyKey: "rss.poll:mapping-coordinate:" + resolution.ID.String(), MaxAttempts: 5, Timeout: 30 * time.Second,
				Payload: map[string]any{"continuous": false, "subscriptionVersion": entry.SubscriptionVersion},
			})
			if err != nil {
				return fmt.Errorf("schedule RSS mapping continuation: %w", err)
			}
			if _, err := scope.Queries.CompleteAgentResolution(ctx, db.CompleteAgentResolutionParams{
				Status: string(domain.AgentResolutionApplied), Validation: validationJSON, ID: repository.UUIDToPG(resolution.ID),
			}); err != nil {
				return fmt.Errorf("complete pre-mapping Agent RSS coordinate resolution: %w", err)
			}
			return appendResourceEvent(ctx, scope.Queries, "rss_entry", resolution.ResourceID, scheduled.Operation.ID, uuid.Nil, "rss.coordinate_resolved", map[string]any{
				"sourceSeason": proposal.SourceSeason, "sourceEpisode": proposal.SourceEpisode,
				"source": source, "agentResolutionId": resolution.ID, "mappingPending": true,
			})
		}
		occupancy, err := lockRSSMappedTargetOccupancyWithRealtimeCheck(
			ctx,
			scope,
			repository.UUIDFromPG(entry.SubscriptionID),
			proposal.SourceSeason,
			proposal.SourceEpisode,
			resolution.ResourceID,
			realtimeCheckID,
		)
		if err != nil {
			return err
		}
		if occupancy.Reason != "" {
			if _, err := markRSSEntryTargetOccupiedInTx(ctx, scope, resolution.ResourceID, uuid.Nil, occupancy); err != nil {
				return err
			}
		}

		var downstreamOperationID uuid.UUID
		if occupancy.Reason == "" && entry.SubscriptionEnabled && !entry.SubscriptionCompletedAt.Valid && !entry.SubscriptionDeletedAt.Valid {
			updated, err := scope.Queries.MarkRSSEntryEnqueueing(ctx, entry.ID)
			if err != nil {
				return fmt.Errorf("mark Agent-resolved RSS entry enqueueing: %w", err)
			}
			sourcePayload, err := json.Marshal(map[string]any{
				"rssEntryId": resolution.ResourceID, "identityKey": entry.IdentityKey,
				"sourceSeason": proposal.SourceSeason, "sourceEpisode": proposal.SourceEpisode,
			})
			if err != nil {
				return fmt.Errorf("encode Agent RSS acquisition payload: %w", err)
			}
			acquisition, err := scope.Queries.UpsertRSSAcquisition(ctx, db.UpsertRSSAcquisitionParams{
				ID: repository.UUIDToPG(uuid.New()), SeriesID: entry.SeriesID, MappingProfileID: entry.MappingProfileID,
				RssEntryID: entry.ID, SourcePayload: sourcePayload,
			})
			if err != nil {
				return fmt.Errorf("create Agent RSS acquisition: %w", err)
			}
			download, err := scope.Queries.CreateRSSDownloadAttempt(ctx, db.CreateRSSDownloadAttemptParams{
				ID: repository.UUIDToPG(uuid.New()), AcquisitionID: acquisition.ID,
			})
			if err != nil {
				return fmt.Errorf("create Agent RSS download: %w", err)
			}
			scheduled, err := service.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
				Kind: appqueue.KindDownloadEnqueue, ResourceType: "download", ResourceID: repository.UUIDFromPG(download.ID),
				IdempotencyKey: "download.enqueue:" + repository.UUIDFromPG(download.ID).String(),
				MaxAttempts:    5, Timeout: 2 * time.Minute,
				Payload: map[string]any{"defaultSeason": proposal.SourceSeason, "defaultEpisode": proposal.SourceEpisode, "singleEpisode": true},
			})
			if err != nil {
				return fmt.Errorf("schedule Agent RSS download: %w", err)
			}
			downstreamOperationID = scheduled.Operation.ID
			if err := appendResourceEvent(ctx, scope.Queries, "rss_entry", resolution.ResourceID, scheduled.Operation.ID, uuid.Nil, "rss.entry.enqueueing", map[string]any{
				"status": domain.RSSEnqueueing, "enqueueAttempt": updated.EnqueueAttempts,
				"acquisitionId": repository.UUIDFromPG(acquisition.ID), "downloadId": repository.UUIDFromPG(download.ID),
				"agentResolutionId": resolution.ID,
			}); err != nil {
				return err
			}
		}
		if occupancy.Fulfilled {
			if err := NewRSSWorkflow(service.queries, service.transactor, service.operations).completeRSSSubscriptionAtFulfillmentInTx(
				ctx,
				scope,
				repository.UUIDFromPG(entry.SubscriptionID),
				uuid.Nil,
				"agent_coordinate",
			); err != nil {
				return err
			}
		}
		if _, err := scope.Queries.CompleteAgentResolution(ctx, db.CompleteAgentResolutionParams{
			Status: string(domain.AgentResolutionApplied), Validation: validationJSON, ID: repository.UUIDToPG(resolution.ID),
		}); err != nil {
			return fmt.Errorf("complete Agent RSS resolution: %w", err)
		}
		return appendResourceEvent(ctx, scope.Queries, "rss_entry", resolution.ResourceID, downstreamOperationID, uuid.Nil, "rss.coordinate_resolved", map[string]any{
			"sourceSeason": proposal.SourceSeason, "sourceEpisode": proposal.SourceEpisode,
			"source": source, "agentResolutionId": resolution.ID, "targetOccupancyReason": occupancy.Reason,
		})
	})
}

func (service *AgentResolutionService) applyDownloadFileResolution(
	ctx context.Context,
	resolution domain.AgentResolution,
	proposal domain.AgentDownloadFileResolutionProposal,
	validation domain.AgentProposalValidation,
) error {
	validationJSON, _ := json.Marshal(validation)
	return service.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		lockedResolution, err := scope.Queries.LockAgentResolution(ctx, repository.UUIDToPG(resolution.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Agent file resolution: %w", err)
		}
		if lockedResolution.Version != int32(resolution.Version) || (lockedResolution.Status != string(domain.AgentResolutionProposed) && lockedResolution.Status != string(domain.AgentResolutionReviewRequired)) {
			return NewError("state_conflict", "the Agent resolution changed before file resolution was applied", ErrStateConflict, nil)
		}
		download, err := scope.Queries.LockDownloadByID(ctx, repository.UUIDToPG(resolution.ResourceID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Agent resolution download: %w", err)
		}
		if resolution.ResourceVersion == nil || download.Version != int32(*resolution.ResourceVersion) {
			return NewError("agent_resolution_stale", "the download changed before Agent file resolution was applied", ErrStateConflict, nil)
		}
		if download.Status != string(domain.DownloadFileResolutionPending) && (download.Status != string(domain.DownloadFailed) || stringValue(download.FailureStage) != "file_resolution") {
			return NewError("invalid_state", "the download is not waiting for file resolution", ErrStateConflict, map[string]any{"status": download.Status})
		}
		files, err := scope.Queries.ListDownloadFiles(ctx, download.ID)
		if err != nil {
			return fmt.Errorf("list Agent resolution download files: %w", err)
		}
		contextRow, err := scope.Queries.GetAgentDownloadContext(ctx, download.ID)
		if err != nil {
			return fmt.Errorf("load Agent download coordinate constraints: %w", err)
		}
		items, err := downloadResolutionItemsFromAgentProposal(files, proposal)
		if err != nil {
			return err
		}
		normalized, err := validateDownloadFileResolution(files, items)
		if err != nil {
			return err
		}
		if err := validateSingleEpisodeFileResolution(files, normalized, int(contextRow.DefaultSourceSeason), int32PointerToInt(contextRow.DefaultSourceEpisode)); err != nil {
			return err
		}
		for _, item := range normalized {
			if _, err := scope.Queries.SetDownloadFileResolution(ctx, db.SetDownloadFileResolutionParams{
				Selected: item.Selected, SourceSeason: optionalResolutionInt32(item.SourceSeason),
				SourceEpisode: optionalResolutionInt32(item.SourceEpisode), ID: repository.UUIDToPG(item.FileID),
				DownloadID: download.ID,
			}); err != nil {
				return fmt.Errorf("apply Agent download file resolution: %w", err)
			}
		}
		source := string(domain.DecisionSourceAgentAuto)
		updated, err := scope.Queries.SetDownloadResolutionSource(ctx, db.SetDownloadResolutionSourceParams{
			FileResolutionSource: &source, AgentResolutionID: repository.UUIDToPG(resolution.ID),
			ID: download.ID, ExpectedVersion: download.Version,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return NewError("state_conflict", "the download changed before Agent file resolution was saved", ErrStateConflict, nil)
		}
		if err != nil {
			return fmt.Errorf("save Agent download resolution source: %w", err)
		}
		scheduled, err := service.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind: appqueue.KindDownloadSelectionApply, ResourceType: "download", ResourceID: resolution.ResourceID,
			IdempotencyKey: "download.selection.apply:agent:" + resolution.ID.String(),
			MaxAttempts:    5, Timeout: time.Minute, Payload: map[string]any{"agentResolutionId": resolution.ID},
		})
		if err != nil {
			return fmt.Errorf("schedule Agent download selection apply: %w", err)
		}
		if _, err := scope.Queries.CompleteAgentResolution(ctx, db.CompleteAgentResolutionParams{
			Status: string(domain.AgentResolutionApplied), Validation: validationJSON, ID: repository.UUIDToPG(resolution.ID),
		}); err != nil {
			return fmt.Errorf("complete Agent download resolution: %w", err)
		}
		return appendResourceEvent(ctx, scope.Queries, "download", resolution.ResourceID, scheduled.Operation.ID, uuid.Nil, "download.file_resolution_saved", map[string]any{
			"source": source, "agentResolutionId": resolution.ID, "downloadVersion": updated.Version,
		})
	})
}

func validateSubtitleVideoMatchProposal(proposal domain.AgentSubtitleVideoMatchProposal, snapshot agentContextSnapshot) domain.AgentProposalValidation {
	invalid := func(code string) domain.AgentProposalValidation {
		return domain.AgentProposalValidation{Verdict: domain.AgentValidationInvalid, ReasonCodes: []string{code}}
	}
	if snapshot.SubtitleVideoMatch == nil {
		return invalid("subtitle_video_match_scope_invalid")
	}
	if proposal.TaskID != snapshot.SubtitleVideoMatch.TaskID {
		return invalid("agent_tool_scope_violation")
	}
	if len(proposal.EvidenceCodes) == 0 || len(proposal.EvidenceCodes) > 16 {
		return invalid("subtitle_video_match_evidence_invalid")
	}
	for _, code := range proposal.EvidenceCodes {
		if strings.TrimSpace(code) == "" || len(code) > 128 {
			return invalid("subtitle_video_match_evidence_invalid")
		}
	}
	if proposal.Decision != "resolved" {
		return domain.AgentProposalValidation{Verdict: domain.AgentValidationReviewRequired, ReasonCodes: []string{"agent_requested_review"}}
	}
	selectedID := strings.TrimSpace(proposal.Selected.CandidateID)
	if selectedID == "" {
		return invalid("subtitle_video_match_selection_invalid")
	}
	for _, candidate := range snapshot.SubtitleVideoMatch.Candidates {
		if candidate.ID == selectedID {
			return domain.AgentProposalValidation{Verdict: domain.AgentValidationAutoApplicable, ReasonCodes: []string{}}
		}
	}
	return invalid("subtitle_video_match_scope_violation")
}

func (service *AgentResolutionService) applySubtitleVideoMatch(
	ctx context.Context,
	resolution domain.AgentResolution,
	proposal domain.AgentSubtitleVideoMatchProposal,
	validation domain.AgentProposalValidation,
) error {
	validationJSON, _ := json.Marshal(validation)
	return service.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		lockedResolution, err := scope.Queries.LockAgentResolution(ctx, repository.UUIDToPG(resolution.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Agent subtitle video match resolution: %w", err)
		}
		if lockedResolution.Version != int32(resolution.Version) || (lockedResolution.Status != string(domain.AgentResolutionProposed) && lockedResolution.Status != string(domain.AgentResolutionReviewRequired)) {
			return NewError("state_conflict", "the Agent resolution changed before subtitle video match was applied", ErrStateConflict, nil)
		}
		contextRow, err := scope.Queries.GetSubtitleVideoMatchContext(ctx, repository.UUIDToPG(resolution.ResourceID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Agent subtitle video match scope: %w", err)
		}
		if contextRow.ScopeStatus != "pending" {
			return NewError("invalid_state", "the subtitle video match scope is not pending", ErrStateConflict, map[string]any{"status": contextRow.ScopeStatus})
		}
		candidates, err := scope.Queries.ListSubtitleVideoMatchCandidates(ctx, repository.UUIDToPG(resolution.ResourceID))
		if err != nil {
			return fmt.Errorf("list Agent subtitle video match scope candidates: %w", err)
		}
		selectedID := strings.TrimSpace(proposal.Selected.CandidateID)
		found := false
		for _, candidate := range candidates {
			if candidate.CandidateID == selectedID {
				found = true
				break
			}
		}
		if !found {
			return NewError("subtitle_video_match_scope_violation", "the selected subtitle candidate is outside the scope", ErrInvalidInput, nil)
		}
		if _, err := scope.Queries.ApplySubtitleVideoMatchSelection(ctx, db.ApplySubtitleVideoMatchSelectionParams{
			ID: repository.UUIDToPG(resolution.ResourceID), SelectedCandidateID: &selectedID, AgentResolutionID: repository.UUIDToPG(resolution.ID),
		}); errors.Is(err, pgx.ErrNoRows) {
			return NewError("state_conflict", "the subtitle video match scope changed before Agent selection was saved", ErrStateConflict, nil)
		} else if err != nil {
			return fmt.Errorf("save Agent subtitle video match selection: %w", err)
		}
		scheduled, err := service.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind: appqueue.KindSubtitlePrepare, ResourceType: "episode_task", ResourceID: repository.UUIDFromPG(contextRow.TaskID),
			IdempotencyKey: "subtitle.prepare:agent:" + resolution.ID.String(),
			MaxAttempts:    5, Timeout: 30 * time.Second, Payload: map[string]any{"agentResolutionId": resolution.ID},
		})
		if err != nil {
			return fmt.Errorf("schedule Agent subtitle prepare: %w", err)
		}
		if _, err := scope.Queries.CompleteAgentResolution(ctx, db.CompleteAgentResolutionParams{
			Status: string(domain.AgentResolutionApplied), Validation: validationJSON, ID: repository.UUIDToPG(resolution.ID),
		}); err != nil {
			return fmt.Errorf("complete Agent subtitle video match resolution: %w", err)
		}
		return appendResourceEvent(ctx, scope.Queries, "episode_task", repository.UUIDFromPG(contextRow.TaskID), scheduled.Operation.ID, uuid.Nil, "subtitle.video_match_saved", map[string]any{
			"agentResolutionId": resolution.ID, "selectedCandidateId": selectedID,
		})
	})
}

func downloadResolutionItemsFromAgentProposal(files []db.DownloadFile, proposal domain.AgentDownloadFileResolutionProposal) ([]domain.DownloadFileResolutionItem, error) {
	videoCoordinates := make(map[uuid.UUID][2]int, len(proposal.Videos))
	selected := make(map[uuid.UUID][2]int, len(proposal.Videos)+len(proposal.Subtitles))
	for _, video := range proposal.Videos {
		coordinate := [2]int{video.SourceSeason, video.SourceEpisode}
		videoCoordinates[video.FileID] = coordinate
		selected[video.FileID] = coordinate
	}
	for _, subtitle := range proposal.Subtitles {
		coordinate, ok := videoCoordinates[subtitle.VideoFileID]
		if !ok {
			return nil, NewError("download_subtitle_video_invalid", "an Agent subtitle does not reference a selected video", ErrInvalidInput, nil)
		}
		selected[subtitle.FileID] = coordinate
	}
	items := make([]domain.DownloadFileResolutionItem, 0, len(files))
	for _, file := range files {
		fileID := repository.UUIDFromPG(file.ID)
		item := domain.DownloadFileResolutionItem{FileID: fileID}
		if coordinate, ok := selected[fileID]; ok {
			season, episode := coordinate[0], coordinate[1]
			item.Selected, item.SourceSeason, item.SourceEpisode = true, &season, &episode
		} else {
			item.SourceSeason = int32PointerToInt(file.SourceSeason)
			item.SourceEpisode = int32PointerToInt(file.SourceEpisode)
		}
		items = append(items, item)
	}
	return items, nil
}

func (service *AgentResolutionService) finishInactive(ctx context.Context, id uuid.UUID, status domain.AgentResolutionStatus, code, message string) error {
	_, err := service.queries.FailAgentResolution(ctx, db.FailAgentResolutionParams{
		Status: string(status), ErrorCode: &code, ErrorMessage: &message, ID: repository.UUIDToPG(id),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func agentCapabilityAutomatic(settings domain.AgentSettings, capability domain.AgentCapability) bool {
	settings = settings.WithDefaults()
	switch capability {
	case domain.AgentCapabilityRSSCoordinate, domain.AgentCapabilityRSSReleaseAdjudication:
		return settings.RSSCoordinateMode != domain.AgentResolutionOff
	case domain.AgentCapabilityDownloadFileResolution:
		return settings.DownloadFileSelectionMode != domain.AgentResolutionOff
	case domain.AgentCapabilityRSSPreacquisitionMapping, domain.AgentCapabilityEpisodeMapping:
		return settings.EpisodeMappingEnabled
	case domain.AgentCapabilitySubtitleVideoMatch:
		return settings.SubtitleVideoMatchMode != domain.AgentResolutionOff
	default:
		return false
	}
}

func agentCapabilityEnabled(settings domain.AgentSettings, capability domain.AgentCapability) bool {
	switch capability {
	case domain.AgentCapabilityRSSCoordinate, domain.AgentCapabilityRSSReleaseAdjudication:
		return settings.RSSCoordinateMode != domain.AgentResolutionOff
	case domain.AgentCapabilityDownloadFileResolution:
		return settings.DownloadFileSelectionMode != domain.AgentResolutionOff
	case domain.AgentCapabilityCatalogCandidate:
		return settings.CatalogMatchEnabled
	case domain.AgentCapabilityRSSPreacquisitionMapping, domain.AgentCapabilityEpisodeMapping:
		return settings.EpisodeMappingEnabled
	case domain.AgentCapabilitySubtitleVideoMatch:
		return settings.SubtitleVideoMatchMode != domain.AgentResolutionOff
	default:
		return false
	}
}

func agentResolutionFromDB(row db.AgentResolution) domain.AgentResolution {
	validation := domain.AgentProposalValidation{}
	_ = json.Unmarshal(row.Validation, &validation)
	result := domain.AgentResolution{
		ID: repository.UUIDFromPG(row.ID), OperationID: repository.UUIDFromPG(row.OperationID), Version: int(row.Version),
		Capability: domain.AgentCapability(row.Capability), ResourceType: row.ResourceType, ResourceID: repository.UUIDFromPG(row.ResourceID),
		Trigger: row.Trigger, Status: domain.AgentResolutionStatus(row.Status), InputFingerprint: row.InputFingerprint,
		ConfigurationVersion: int(row.ConfigurationVersion), Protocol: row.Protocol, ProviderOrigin: row.ProviderOrigin,
		Model: row.Model, PromptVersion: row.PromptVersion, ToolsetVersion: row.ToolsetVersion,
		Proposal: row.Proposal, Validation: validation, ErrorCode: stringValue(row.ErrorCode), ErrorMessage: stringValue(row.ErrorMessage),
		InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, ToolCallCount: int(row.ToolCallCount),
		LatencyMilliseconds: row.LatencyMilliseconds, ReviewDecision: stringValue(row.ReviewDecision),
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if row.ResourceVersion != nil {
		value := int(*row.ResourceVersion)
		result.ResourceVersion = &value
	}
	result.CreatedBy = pgUUIDPointer(row.CreatedBy)
	result.ReviewedBy = pgUUIDPointer(row.ReviewedBy)
	result.StartedAt = pgTimePointer(row.StartedAt)
	result.CompletedAt = pgTimePointer(row.CompletedAt)
	result.ReviewedAt = pgTimePointer(row.ReviewedAt)
	result.AppliedAt = pgTimePointer(row.AppliedAt)
	return result
}

func (service *AgentResolutionService) persistAgentResolutionSteps(
	ctx context.Context,
	resolutionID uuid.UUID,
	attempt int,
	steps []agentharness.Step,
) error {
	const sequenceStride = 64
	if attempt < 1 {
		attempt = 1
	}
	for _, step := range steps {
		sequence := (attempt-1)*sequenceStride + step.Sequence
		status := step.Status
		if status == "" {
			status = "succeeded"
		}
		if _, err := service.queries.CreateAgentResolutionStep(ctx, db.CreateAgentResolutionStepParams{
			ID: repository.UUIDToPG(uuid.New()), ResolutionID: repository.UUIDToPG(resolutionID), Sequence: int32(sequence),
			ToolName: step.ToolName, Status: status, ArgumentsDigest: step.ArgumentsDigest, ResultDigest: step.ResultDigest,
			ErrorCode: optionalString(step.ErrorCode),
		}); err != nil {
			return fmt.Errorf("persist Agent resolution step: %w", err)
		}
	}
	return nil
}

func agentToolStepBudget(capability domain.AgentCapability, snapshot agentContextSnapshot) int {
	const (
		defaultSteps = 6
		maximumSteps = 64
	)
	if capability != domain.AgentCapabilityRSSReleaseAdjudication {
		return defaultSteps
	}
	steps := len(snapshot.RSSAdjudicationEntries) + defaultSteps
	if steps > maximumSteps {
		return maximumSteps
	}
	return steps
}

func agentRunError(err error) error {
	var apiErr *agentapi.Error
	if errors.As(err, &apiErr) {
		return &AgentRunError{Code: apiErr.Code, Message: apiErr.Code, Retryable: apiErr.Retryable, Cause: err}
	}
	return &AgentRunError{Code: "agent_resolution_failed", Message: "Agent resolution failed", Retryable: true, Cause: err}
}

func invalidAgentResolution(field, reason string) *Error {
	return NewError("invalid_agent_resolution", "the Agent resolution request is invalid", ErrInvalidInput, map[string]any{"field": field, "reason": reason})
}

func intPointerToInt32(value *int) *int32 {
	if value == nil {
		return nil
	}
	converted := int32(*value)
	return &converted
}

func optionalUUID(value *uuid.UUID) pgtype.UUID {
	if value == nil || *value == uuid.Nil {
		return pgtype.UUID{}
	}
	return repository.UUIDToPG(*value)
}

func pgUUIDPointer(value pgtype.UUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	converted := repository.UUIDFromPG(value)
	return &converted
}

func pgTimePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	converted := value.Time
	return &converted
}

func sameOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func serviceCode(err error, fallback string) string {
	var serviceErr *Error
	if errors.As(err, &serviceErr) && serviceErr.Code != "" {
		return serviceErr.Code
	}
	return fallback
}
