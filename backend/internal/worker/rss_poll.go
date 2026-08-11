package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/riverqueue/river"
)

type RSSFeedClient interface {
	Fetch(context.Context, string) (domain.RSSFeed, error)
}

type RSSFeedClientFactory func(context.Context) (RSSFeedClient, error)

type RSSPollStore interface {
	LoadPollCommand(context.Context, uuid.UUID) (domain.RSSPollCommand, error)
	PreparePollMapping(context.Context, uuid.UUID, uuid.UUID, domain.RSSFeed) (domain.RSSPollMappingPreparation, error)
	PersistPoll(context.Context, uuid.UUID, uuid.UUID, domain.RSSFeed, domain.RSSPollPersistOptions) (domain.RSSPollPersistResult, error)
	RecordPollFailure(context.Context, uuid.UUID, uuid.UUID, string, string) error
	RecordPollBatch(context.Context, uuid.UUID, uuid.UUID, domain.RSSPollBatchSummary) error
	ScheduleRSSDownload(context.Context, domain.RSSEnqueueCandidate) error
	ListAgentMappingAcquisitions(context.Context, uuid.UUID) ([]uuid.UUID, error)
}

type RSSAgentResolutionService interface {
	DownloadAgentResolutionCreator
	CapabilityEnabled(context.Context, domain.AgentCapability) (bool, error)
	RetryAutomatic(context.Context, uuid.UUID, int) (service.AgentResolutionCommandResult, error)
}

type RSSPollHandler struct {
	feeds            RSSFeedClient
	newClient        RSSFeedClientFactory
	store            RSSPollStore
	maxConcurrency   int
	agentResolutions RSSAgentResolutionService
	realtimeVerifier service.RSSRealtimeTargetVerifier
}

type rssPollPayload struct {
	Continuous          bool  `json:"continuous"`
	SubscriptionVersion int32 `json:"subscriptionVersion"`
}

func NewRSSPollHandler(feeds RSSFeedClient, store RSSPollStore, maxConcurrency int, agentResolutions ...RSSAgentResolutionService) *RSSPollHandler {
	handler := &RSSPollHandler{feeds: feeds, store: store, maxConcurrency: maxConcurrency}
	if len(agentResolutions) > 0 {
		handler.agentResolutions = agentResolutions[0]
	}
	return handler
}

func NewConfiguredRSSPollHandler(newClient RSSFeedClientFactory, store RSSPollStore, maxConcurrency int, agentResolutions ...RSSAgentResolutionService) *RSSPollHandler {
	handler := &RSSPollHandler{newClient: newClient, store: store, maxConcurrency: maxConcurrency}
	if len(agentResolutions) > 0 {
		handler.agentResolutions = agentResolutions[0]
	}
	return handler
}

func (handler *RSSPollHandler) WithRealtimeTargetVerifier(verifier service.RSSRealtimeTargetVerifier) *RSSPollHandler {
	handler.realtimeVerifier = verifier
	return handler
}

func (handler *RSSPollHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "rss_subscription" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_rss_operation", "rss.poll requires an RSS subscription resource", nil)
	}
	if (handler.feeds == nil && handler.newClient == nil) || handler.store == nil || handler.maxConcurrency <= 0 {
		return permanentFailure("rss_handler_not_configured", "RSS poll handler dependencies are unavailable", nil)
	}
	payload := rssPollPayload{}
	if len(operation.Payload) > 0 {
		if err := json.Unmarshal(operation.Payload, &payload); err != nil {
			return permanentFailure("invalid_rss_operation", "RSS poll payload is invalid", err)
		}
	}
	command, err := handler.store.LoadPollCommand(ctx, operation.ResourceID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return permanentFailure("rss_subscription_not_found", "the RSS subscription no longer exists", err)
		}
		return retryableFailure("rss_storage_unavailable", "RSS subscription storage is unavailable", err)
	}
	if command.SubscriptionID != operation.ResourceID {
		return permanentFailure("rss_resource_mismatch", "the operation does not match its RSS subscription", nil)
	}
	if command.Deleted || command.Completed || !command.Enabled {
		return nil
	}
	if payload.Continuous && payload.SubscriptionVersion > 0 && payload.SubscriptionVersion != command.Version {
		return nil
	}

	feeds := handler.feeds
	if handler.newClient != nil {
		feeds, err = handler.newClient(ctx)
		if err != nil {
			return retryableFailure("rss_client_unavailable", "the RSS client could not be configured", err)
		}
	}
	feed, err := feeds.Fetch(ctx, command.FeedURL)
	if err != nil {
		if auditErr := handler.store.RecordPollFailure(ctx, operation.ID, command.SubscriptionID, "rss_fetch_failed", err.Error()); auditErr != nil {
			return retryableFailure("rss_storage_unavailable", "RSS poll failure could not be recorded", auditErr)
		}
		if payload.Continuous {
			return river.JobSnooze(command.PollInterval)
		}
		return retryableFailure("rss_fetch_failed", "the RSS feed could not be fetched", err)
	}
	if command.AutoEpisodeMapping {
		preparation, err := handler.store.PreparePollMapping(ctx, operation.ID, command.SubscriptionID, feed)
		if err != nil {
			return retryableFailure("rss_mapping_storage_unavailable", "the RSS mapping could not be prepared", err)
		}
		if preparation.Applied {
			return nil
		}
		if !preparation.Ready {
			mappingApplied, err := handler.schedulePreacquisitionMapping(ctx, preparation)
			if mappingApplied {
				return nil
			}
			if err != nil {
				failureCode := "rss_preacquisition_mapping_pending"
				var serviceErr *service.Error
				if errors.As(err, &serviceErr) && serviceErr.Code != "" {
					failureCode = serviceErr.Code
				}
				if auditErr := handler.store.RecordPollFailure(ctx, operation.ID, command.SubscriptionID, failureCode, err.Error()); auditErr != nil {
					return retryableFailure("rss_storage_unavailable", "RSS mapping wait could not be recorded", auditErr)
				}
			}
			if payload.Continuous {
				return river.JobSnooze(command.PollInterval)
			}
			return nil
		}
	}
	adjudicateReleases := false
	if handler.agentResolutions != nil {
		adjudicateReleases, err = handler.agentResolutions.CapabilityEnabled(ctx, domain.AgentCapabilityRSSReleaseAdjudication)
		if err != nil {
			return retryableFailure("agent_configuration_unavailable", "Agent RSS mode could not be loaded", err)
		}
	}
	realtimeCheckID := uuid.Nil
	if handler.realtimeVerifier != nil {
		realtimeCheckID, err = handler.realtimeVerifier.VerifySubscription(ctx, command.SubscriptionID)
		if err != nil {
			return rssRealtimeWorkerFailure(err)
		}
	}
	persisted, err := handler.store.PersistPoll(ctx, operation.ID, command.SubscriptionID, feed, domain.RSSPollPersistOptions{
		AdjudicateReleases: adjudicateReleases,
		RealtimeCheckID:    realtimeCheckID,
	})
	if err != nil {
		var serviceErr *service.Error
		if errors.As(err, &serviceErr) && strings.HasPrefix(serviceErr.Code, "rss_realtime_") {
			return retryableFailure(serviceErr.Code, serviceErr.Message, serviceErr)
		}
		return retryableFailure("rss_storage_unavailable", "RSS entries could not be persisted", err)
	}
	for _, batchID := range persisted.AgentAdjudicationBatchIDs {
		if handler.agentResolutions == nil {
			break
		}
		result, err := handler.agentResolutions.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{
			Capability: domain.AgentCapabilityRSSReleaseAdjudication, ResourceID: batchID,
		})
		if err != nil && !errors.Is(err, service.ErrStateConflict) {
			return retryableFailure("agent_resolution_schedule_failed", "Agent RSS adjudication could not be scheduled", err)
		}
		if err == nil && service.AutomaticAgentResolutionRetryable(result.Resolution) {
			if _, retryErr := handler.agentResolutions.RetryAutomatic(ctx, result.Resolution.ID, result.Resolution.Version); retryErr != nil && !errors.Is(retryErr, service.ErrStateConflict) {
				return retryableFailure("agent_resolution_schedule_failed", "Agent RSS adjudication could not be resumed", retryErr)
			}
		}
	}
	for _, entryID := range persisted.AgentCoordinateCandidates {
		if handler.agentResolutions == nil {
			break
		}
		result, err := handler.agentResolutions.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{
			Capability: domain.AgentCapabilityRSSCoordinate, ResourceID: entryID,
		})
		if err != nil && !errors.Is(err, service.ErrStateConflict) {
			return retryableFailure("agent_resolution_schedule_failed", "Agent RSS coordinate resolution could not be scheduled", err)
		}
		if err == nil && service.AutomaticAgentResolutionRetryable(result.Resolution) {
			if _, retryErr := handler.agentResolutions.RetryAutomatic(ctx, result.Resolution.ID, result.Resolution.Version); retryErr != nil && !errors.Is(retryErr, service.ErrStateConflict) {
				return retryableFailure("agent_resolution_schedule_failed", "Agent RSS coordinate resolution could not be resumed", retryErr)
			}
		}
	}
	if err := handler.schedulePendingEpisodeMappings(ctx, command); err != nil {
		return err
	}
	scheduler := service.RSSEntryScheduler(handler.store)
	if handler.realtimeVerifier != nil {
		realtimeStore, ok := handler.store.(interface {
			ScheduleRSSDownloadWithRealtimeCheck(context.Context, domain.RSSEnqueueCandidate, uuid.UUID) error
		})
		if !ok {
			return permanentFailure("rss_realtime_not_configured", "RSS enqueue does not accept real-time Emby checks", nil)
		}
		scheduler = &rssRealtimeEntryScheduler{verifier: handler.realtimeVerifier, store: realtimeStore}
	}
	batch, err := service.ScheduleRSSBatch(ctx, persisted.Candidates, handler.maxConcurrency, scheduler)
	if err != nil {
		return retryableFailure("rss_schedule_interrupted", "RSS download scheduling was interrupted", err)
	}

	summary := domain.RSSPollBatchSummary{
		FetchedCount:  persisted.FetchedCount,
		EligibleCount: len(batch.Outcomes),
	}
	var realtimeScheduleErr error
	for _, outcome := range batch.Outcomes {
		if outcome.Err == nil {
			summary.ScheduledCount++
		} else {
			summary.FailedCount++
			var verificationErr *service.RSSRealtimeVerificationError
			var serviceErr *service.Error
			if errors.As(outcome.Err, &verificationErr) ||
				(errors.As(outcome.Err, &serviceErr) && strings.HasPrefix(serviceErr.Code, "rss_realtime_")) {
				realtimeScheduleErr = outcome.Err
			}
		}
	}
	if realtimeScheduleErr != nil {
		return rssRealtimeWorkerFailure(realtimeScheduleErr)
	}
	if err := handler.store.RecordPollBatch(ctx, operation.ID, command.SubscriptionID, summary); err != nil {
		return retryableFailure("rss_storage_unavailable", "RSS poll summary could not be recorded", err)
	}
	if payload.Continuous {
		return river.JobSnooze(command.PollInterval)
	}
	return nil
}

type rssRealtimeEntryStore interface {
	ScheduleRSSDownloadWithRealtimeCheck(context.Context, domain.RSSEnqueueCandidate, uuid.UUID) error
}

type rssRealtimeEntryScheduler struct {
	verifier service.RSSRealtimeTargetVerifier
	store    rssRealtimeEntryStore
}

func (scheduler *rssRealtimeEntryScheduler) ScheduleRSSDownload(
	ctx context.Context,
	candidate domain.RSSEnqueueCandidate,
) error {
	checkID, err := scheduler.verifier.VerifyEntry(ctx, candidate.EntryID)
	if err != nil {
		return err
	}
	if checkID == uuid.Nil {
		return &service.RSSRealtimeVerificationError{
			Code: "rss_realtime_check_required", Message: "the mapped RSS target was not verified", Retryable: true,
		}
	}
	return scheduler.store.ScheduleRSSDownloadWithRealtimeCheck(ctx, candidate, checkID)
}

func rssRealtimeWorkerFailure(err error) error {
	var verificationErr *service.RSSRealtimeVerificationError
	if errors.As(err, &verificationErr) {
		if verificationErr.Retryable {
			return retryableFailure(verificationErr.Code, verificationErr.Message, verificationErr)
		}
		return permanentFailure(verificationErr.Code, verificationErr.Message, verificationErr)
	}
	return retryableFailure("emby_realtime_check_failed", "real-time Emby target verification failed", err)
}

func (handler *RSSPollHandler) schedulePreacquisitionMapping(
	ctx context.Context,
	preparation domain.RSSPollMappingPreparation,
) (bool, error) {
	if handler.agentResolutions == nil {
		return false, errors.New("automatic Agent resolution service is unavailable")
	}
	for _, entryID := range preparation.AgentCoordinateCandidates {
		result, err := handler.agentResolutions.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{
			Capability: domain.AgentCapabilityRSSCoordinate, ResourceID: entryID,
		})
		if err != nil {
			return false, fmt.Errorf("schedule pre-mapping RSS coordinate Agent: %w", err)
		}
		if service.AutomaticAgentResolutionRetryable(result.Resolution) {
			if _, err := handler.agentResolutions.RetryAutomatic(ctx, result.Resolution.ID, result.Resolution.Version); err != nil && !errors.Is(err, service.ErrStateConflict) {
				return false, fmt.Errorf("resume pre-mapping RSS coordinate Agent: %w", err)
			}
		}
	}
	if preparation.ScopeID != uuid.Nil {
		result, err := handler.agentResolutions.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{
			Capability: domain.AgentCapabilityRSSPreacquisitionMapping, ResourceID: preparation.ScopeID,
		})
		if err != nil {
			var serviceErr *service.Error
			if errors.As(err, &serviceErr) && serviceErr.Code == "agent_resolution_not_required" {
				return true, nil
			}
			return false, fmt.Errorf("schedule RSS pre-acquisition Mapping Agent: %w", err)
		}
		if service.AutomaticAgentResolutionRetryable(result.Resolution) {
			if _, err := handler.agentResolutions.RetryAutomatic(ctx, result.Resolution.ID, result.Resolution.Version); err != nil && !errors.Is(err, service.ErrStateConflict) {
				return false, fmt.Errorf("resume RSS pre-acquisition Mapping Agent: %w", err)
			}
		}
	}
	return false, nil
}

func (handler *RSSPollHandler) schedulePendingEpisodeMappings(ctx context.Context, command domain.RSSPollCommand) error {
	if handler.agentResolutions == nil || !command.AutoEpisodeMapping {
		return nil
	}
	acquisitionIDs, err := handler.store.ListAgentMappingAcquisitions(ctx, command.SubscriptionID)
	if err != nil {
		return retryableFailure("agent_resolution_schedule_failed", "Agent Mapping candidates could not be loaded", err)
	}
	for _, acquisitionID := range acquisitionIDs {
		result, err := handler.agentResolutions.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{
			Capability: domain.AgentCapabilityEpisodeMapping, ResourceID: acquisitionID,
		})
		if err != nil && !errors.Is(err, service.ErrStateConflict) {
			return retryableFailure("agent_resolution_schedule_failed", "Agent Mapping resolution could not be scheduled", err)
		}
		if err == nil && service.AutomaticAgentResolutionRetryable(result.Resolution) {
			if _, retryErr := handler.agentResolutions.RetryAutomatic(ctx, result.Resolution.ID, result.Resolution.Version); retryErr != nil && !errors.Is(retryErr, service.ErrStateConflict) {
				return retryableFailure("agent_resolution_schedule_failed", "Agent Mapping resolution could not be resumed", retryErr)
			}
		}
	}
	return nil
}
