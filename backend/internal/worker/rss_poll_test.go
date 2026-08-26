package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
	"github.com/riverqueue/river"
)

type rssFeedClientStub struct {
	feed  domain.RSSFeed
	err   error
	calls int
}

func (stub *rssFeedClientStub) Fetch(context.Context, string) (domain.RSSFeed, error) {
	stub.calls++
	return stub.feed, stub.err
}

type rssPollStoreStub struct {
	command             domain.RSSPollCommand
	loadErr             error
	persistedFeed       domain.RSSFeed
	persistedOptions    domain.RSSPollPersistOptions
	persistResult       domain.RSSPollPersistResult
	persistErr          error
	ensureMappingCalls  int
	ensureOperationID   uuid.UUID
	ensureMappingFeed   domain.RSSFeed
	ensureMappingReady  bool
	mappingPreparation  *domain.RSSPollMappingPreparation
	ensureMappingErr    error
	callSequence        []string
	failureCalls        int
	failureCode         string
	summary             domain.RSSPollBatchSummary
	summaryCalls        int
	scheduleErrors      map[uuid.UUID]error
	scheduleCalls       map[uuid.UUID]int
	realtimeScheduleIDs map[uuid.UUID]uuid.UUID
	mappingIDs          []uuid.UUID
	mappingListCalls    int
	mappingListErr      error
	mutex               sync.Mutex
}

func (stub *rssPollStoreStub) LoadPollCommand(context.Context, uuid.UUID) (domain.RSSPollCommand, error) {
	return stub.command, stub.loadErr
}

func (stub *rssPollStoreStub) PreparePollMapping(_ context.Context, operationID, _ uuid.UUID, feed domain.RSSFeed) (domain.RSSPollMappingPreparation, error) {
	stub.ensureMappingCalls++
	stub.ensureOperationID = operationID
	stub.ensureMappingFeed = feed
	stub.callSequence = append(stub.callSequence, "ensure_mapping")
	if stub.mappingPreparation != nil {
		return *stub.mappingPreparation, stub.ensureMappingErr
	}
	return domain.RSSPollMappingPreparation{Ready: true}, stub.ensureMappingErr
}

func (stub *rssPollStoreStub) PersistPoll(_ context.Context, _ uuid.UUID, _ uuid.UUID, feed domain.RSSFeed, options domain.RSSPollPersistOptions) (domain.RSSPollPersistResult, error) {
	stub.callSequence = append(stub.callSequence, "persist")
	stub.persistedFeed = feed
	stub.persistedOptions = options
	return stub.persistResult, stub.persistErr
}

func (stub *rssPollStoreStub) RecordPollFailure(_ context.Context, _ uuid.UUID, _ uuid.UUID, code, _ string) error {
	stub.failureCalls++
	stub.failureCode = code
	return nil
}

func (stub *rssPollStoreStub) RecordPollBatch(_ context.Context, _ uuid.UUID, _ uuid.UUID, summary domain.RSSPollBatchSummary) error {
	stub.summary = summary
	stub.summaryCalls++
	return nil
}

func (stub *rssPollStoreStub) ScheduleRSSDownload(_ context.Context, candidate domain.RSSEnqueueCandidate) error {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	if stub.scheduleCalls == nil {
		stub.scheduleCalls = make(map[uuid.UUID]int)
	}
	stub.scheduleCalls[candidate.EntryID]++
	return stub.scheduleErrors[candidate.EntryID]
}

func (stub *rssPollStoreStub) ScheduleRSSDownloadWithRealtimeCheck(_ context.Context, candidate domain.RSSEnqueueCandidate, checkID uuid.UUID) error {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	if stub.scheduleCalls == nil {
		stub.scheduleCalls = make(map[uuid.UUID]int)
	}
	if stub.realtimeScheduleIDs == nil {
		stub.realtimeScheduleIDs = make(map[uuid.UUID]uuid.UUID)
	}
	stub.scheduleCalls[candidate.EntryID]++
	stub.realtimeScheduleIDs[candidate.EntryID] = checkID
	return stub.scheduleErrors[candidate.EntryID]
}

func (stub *rssPollStoreStub) ListAgentMappingAcquisitions(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	stub.mappingListCalls++
	return stub.mappingIDs, stub.mappingListErr
}

type rssAgentResolutionStub struct {
	enabled      bool
	capabilities []domain.AgentCapability
	created      []service.AutomaticAgentResolutionRequest
	createResult service.AgentResolutionCommandResult
	createErr    error
	retried      []uuid.UUID
}

func (stub *rssAgentResolutionStub) CapabilityEnabled(_ context.Context, capability domain.AgentCapability) (bool, error) {
	stub.capabilities = append(stub.capabilities, capability)
	return stub.enabled, nil
}

func (stub *rssAgentResolutionStub) CreateAutomatic(_ context.Context, input service.AutomaticAgentResolutionRequest) (service.AgentResolutionCommandResult, error) {
	stub.capabilities = append(stub.capabilities, input.Capability)
	stub.created = append(stub.created, input)
	return stub.createResult, stub.createErr
}

func (stub *rssAgentResolutionStub) RetryAutomatic(_ context.Context, id uuid.UUID, _ int) (service.AgentResolutionCommandResult, error) {
	stub.retried = append(stub.retried, id)
	return service.AgentResolutionCommandResult{}, nil
}

type rssRealtimeVerifierStub struct {
	subscriptionCheckID uuid.UUID
	entryCheckIDs       map[uuid.UUID]uuid.UUID
	subscriptionErr     error
	entryErrors         map[uuid.UUID]error
	subscriptionCalls   []uuid.UUID
	entryCalls          []uuid.UUID
	callSequence        *[]string
}

func (stub *rssRealtimeVerifierStub) VerifySubscription(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	if stub.callSequence != nil {
		*stub.callSequence = append(*stub.callSequence, "verify_subscription")
	}
	stub.subscriptionCalls = append(stub.subscriptionCalls, id)
	return stub.subscriptionCheckID, stub.subscriptionErr
}

func (stub *rssRealtimeVerifierStub) VerifyEntry(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	stub.entryCalls = append(stub.entryCalls, id)
	return stub.entryCheckIDs[id], stub.entryErrors[id]
}

func (stub *rssRealtimeVerifierStub) VerifyCoordinates(context.Context, uuid.UUID, []domain.EpisodeCoordinate) (uuid.UUID, error) {
	return uuid.Nil, nil
}

func TestRSSPollHandlerPreparesTargetMappingWhenAutomaticFileMappingIsDisabled(t *testing.T) {
	subscriptionID := uuid.MustParse("50000000-0000-0000-0000-000000000091")
	feed := domain.RSSFeed{Title: "First subscription", Entries: []domain.RSSFeedEntry{{
		Title: "First subscription S01E01", DownloadURI: "https://example.test/episode-01.torrent",
	}}}
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true,
			AutoEpisodeMapping: false, SourceSeason: 1, PollInterval: time.Minute,
		},
		ensureMappingReady: true,
	}
	verifier := &rssRealtimeVerifierStub{subscriptionCheckID: uuid.New(), callSequence: &store.callSequence}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: feed}, store, 1).WithRealtimeTargetVerifier(verifier)
	operationID := uuid.New()

	if err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "rss_subscription", ResourceID: subscriptionID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if store.ensureMappingCalls != 1 || store.ensureOperationID != operationID || store.ensureMappingFeed.Title != feed.Title {
		t.Fatalf("mapping preparation calls/operation/feed = %d/%s/%q", store.ensureMappingCalls, store.ensureOperationID, store.ensureMappingFeed.Title)
	}
	wantSequence := []string{"ensure_mapping", "verify_subscription", "persist"}
	if len(store.callSequence) != len(wantSequence) {
		t.Fatalf("call sequence = %v, want %v", store.callSequence, wantSequence)
	}
	for index := range wantSequence {
		if store.callSequence[index] != wantSequence[index] {
			t.Fatalf("call sequence = %v, want %v", store.callSequence, wantSequence)
		}
	}
}

func TestRSSPollHandlerRetriesDeterministicMappingStorageFailureBeforeRealtimeVerification(t *testing.T) {
	subscriptionID := uuid.MustParse("50000000-0000-0000-0000-000000000092")
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true,
			AutoEpisodeMapping: true, SourceSeason: 1, PollInterval: time.Minute,
		},
		ensureMappingErr: errors.New("storage unavailable"),
	}
	verifier := &rssRealtimeVerifierStub{subscriptionCheckID: uuid.New()}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Mapping failure"}}, store, 1).
		WithRealtimeTargetVerifier(verifier)

	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID})
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Retryable || failure.Code != "rss_mapping_storage_unavailable" {
		t.Fatalf("Handle() error = %#v", err)
	}
	if len(verifier.subscriptionCalls) != 0 || store.persistedFeed.Title != "" {
		t.Fatalf("mapping failure reached verifier/persist: calls=%v feed=%q", verifier.subscriptionCalls, store.persistedFeed.Title)
	}
}

func TestRSSPollHandlerPropagatesRealtimeChecksAcrossPollAndEnqueue(t *testing.T) {
	subscriptionID, entryID := uuid.New(), uuid.New()
	pollCheckID, enqueueCheckID := uuid.New(), uuid.New()
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true, SourceSeason: 1, PollInterval: time.Minute,
		},
		persistResult: domain.RSSPollPersistResult{Candidates: []domain.RSSEnqueueCandidate{{
			EntryID: entryID, Status: domain.RSSDiscovered, Downloadable: true,
		}}},
	}
	verifier := &rssRealtimeVerifierStub{
		subscriptionCheckID: pollCheckID,
		entryCheckIDs:       map[uuid.UUID]uuid.UUID{entryID: enqueueCheckID},
	}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Realtime"}}, store, 1).
		WithRealtimeTargetVerifier(verifier)
	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if store.persistedOptions.RealtimeCheckID != pollCheckID {
		t.Fatalf("PersistPoll() check = %s, want %s", store.persistedOptions.RealtimeCheckID, pollCheckID)
	}
	if got := store.realtimeScheduleIDs[entryID]; got != enqueueCheckID {
		t.Fatalf("ScheduleRSSDownload() check = %s, want %s", got, enqueueCheckID)
	}
	if len(verifier.subscriptionCalls) != 1 || verifier.subscriptionCalls[0] != subscriptionID || len(verifier.entryCalls) != 1 || verifier.entryCalls[0] != entryID {
		t.Fatalf("verification calls = subscriptions %v entries %v", verifier.subscriptionCalls, verifier.entryCalls)
	}
}

func TestRSSPollHandlerRetriesRealtimeVerificationFailure(t *testing.T) {
	subscriptionID := uuid.New()
	store := &rssPollStoreStub{command: domain.RSSPollCommand{
		SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true, SourceSeason: 1, PollInterval: time.Minute,
	}}
	verifier := &rssRealtimeVerifierStub{subscriptionErr: &service.RSSRealtimeVerificationError{
		Code: "emby_realtime_request_failed", Message: "real-time Emby target verification failed", Retryable: true,
	}}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Realtime failure"}}, store, 1).
		WithRealtimeTargetVerifier(verifier)
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID})
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Retryable || failure.Code != "emby_realtime_request_failed" {
		t.Fatalf("Handle() error = %#v", err)
	}
	if store.persistedFeed.Title != "" {
		t.Fatalf("PersistPoll() ran after failed verification: %#v", store.persistedFeed)
	}
}

func TestRSSPollHandlerSnoozesContinuousRealtimeFailureWithoutExhaustingPoll(t *testing.T) {
	subscriptionID := uuid.New()
	store := &rssPollStoreStub{command: domain.RSSPollCommand{
		SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true,
		SourceSeason: 1, PollInterval: 7 * time.Minute,
	}}
	verifier := &rssRealtimeVerifierStub{subscriptionErr: &service.RSSRealtimeVerificationError{
		Code: "emby_realtime_request_failed", Message: "real-time Emby target verification failed", Retryable: true,
	}}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Realtime failure"}}, store, 1).
		WithRealtimeTargetVerifier(verifier)
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID,
		Payload: []byte(`{"continuous":true}`),
	})
	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) || snooze.Duration != 7*time.Minute {
		t.Fatalf("Handle() error = %#v, want seven-minute snooze", err)
	}
	if store.failureCalls != 1 || store.failureCode != "emby_realtime_request_failed" {
		t.Fatalf("failure audit = calls %d code %q", store.failureCalls, store.failureCode)
	}
}

func TestRSSPollHandlerRetriesEnqueueBoundaryVerificationFailure(t *testing.T) {
	subscriptionID, entryID := uuid.New(), uuid.New()
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true, SourceSeason: 1, PollInterval: time.Minute,
		},
		persistResult: domain.RSSPollPersistResult{Candidates: []domain.RSSEnqueueCandidate{{
			EntryID: entryID, Status: domain.RSSDiscovered, Downloadable: true,
		}}},
	}
	verifier := &rssRealtimeVerifierStub{
		subscriptionCheckID: uuid.New(),
		entryErrors: map[uuid.UUID]error{entryID: &service.RSSRealtimeVerificationError{
			Code: "emby_realtime_request_failed", Message: "real-time Emby target verification failed", Retryable: true,
		}},
	}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Enqueue failure"}}, store, 1).
		WithRealtimeTargetVerifier(verifier)
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID})
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Retryable || failure.Code != "emby_realtime_request_failed" {
		t.Fatalf("Handle() error = %#v", err)
	}
	if len(store.scheduleCalls) != 0 || store.summaryCalls != 0 {
		t.Fatalf("failed boundary scheduled=%v summaryCalls=%d", store.scheduleCalls, store.summaryCalls)
	}
}

func TestRSSPollHandlerSchedulesOnlyPersistedFallbackAdjudication(t *testing.T) {
	subscriptionID := uuid.MustParse("50000000-0000-0000-0000-000000000101")
	batchID := uuid.MustParse("50000000-0000-0000-0000-000000000102")
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true, SourceSeason: 1, PollInterval: time.Minute,
		},
		persistResult: domain.RSSPollPersistResult{FetchedCount: 2, AgentAdjudicationBatchIDs: []uuid.UUID{batchID}},
	}
	agent := &rssAgentResolutionStub{enabled: true}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Scoped releases"}}, store, 1, agent)
	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !store.persistedOptions.AdjudicateReleases {
		t.Fatal("PersistPoll() adjudicateReleases = false, want true")
	}
	if len(agent.capabilities) != 2 || agent.capabilities[0] != domain.AgentCapabilityRSSReleaseAdjudication || agent.capabilities[1] != domain.AgentCapabilityRSSReleaseAdjudication {
		t.Fatalf("Agent capabilities = %v", agent.capabilities)
	}
}

func TestRSSPollHandlerBackfillsOneEpisodeMappingAnchor(t *testing.T) {
	subscriptionID := uuid.MustParse("50000000-0000-0000-0000-000000000121")
	firstAcquisitionID := uuid.MustParse("50000000-0000-0000-0000-000000000122")
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true, AutoEpisodeMapping: true, SourceSeason: 1, PollInterval: time.Minute,
		},
		mappingIDs: []uuid.UUID{firstAcquisitionID},
	}
	agent := &rssAgentResolutionStub{enabled: true}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Mapping backfill"}}, store, 1, agent)

	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if store.mappingListCalls != 1 {
		t.Fatalf("mapping list calls = %d, want 1", store.mappingListCalls)
	}
	created := make(map[uuid.UUID]int)
	for _, input := range agent.created {
		if input.Capability == domain.AgentCapabilityEpisodeMapping {
			created[input.ResourceID]++
		}
	}
	if len(created) != 1 || created[firstAcquisitionID] != 1 {
		t.Fatalf("Mapping resolutions = %v, want one RSS anchor", created)
	}
}

func TestRSSPollHandlerSkipsEpisodeMappingWhenSubscriptionPolicyIsDisabled(t *testing.T) {
	subscriptionID := uuid.New()
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true, SourceSeason: 1, PollInterval: time.Minute,
		},
		mappingIDs: []uuid.UUID{uuid.New()},
	}
	agent := &rssAgentResolutionStub{enabled: true}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Mapping disabled"}}, store, 1, agent)

	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if store.mappingListCalls != 0 {
		t.Fatalf("mapping list calls = %d, want 0", store.mappingListCalls)
	}
	for _, input := range agent.created {
		if input.Capability == domain.AgentCapabilityEpisodeMapping {
			t.Fatalf("disabled subscription created Mapping resolution: %#v", input)
		}
	}
}

func TestRSSPollHandlerResumesFailedMappingApplyWithoutNewProposal(t *testing.T) {
	subscriptionID := uuid.MustParse("50000000-0000-0000-0000-000000000131")
	acquisitionID := uuid.MustParse("50000000-0000-0000-0000-000000000132")
	resolutionID := uuid.MustParse("50000000-0000-0000-0000-000000000133")
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true, AutoEpisodeMapping: true, SourceSeason: 1, PollInterval: time.Minute,
		},
		mappingIDs: []uuid.UUID{acquisitionID},
	}
	agent := &rssAgentResolutionStub{
		enabled: true,
		createResult: service.AgentResolutionCommandResult{Resolution: domain.AgentResolution{
			ID: resolutionID, Version: 4, Trigger: "automatic", Status: domain.AgentResolutionFailed, Proposal: []byte(`{"decision":"resolved"}`),
		}},
	}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Mapping apply retry"}}, store, 1, agent)

	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(agent.retried) != 1 || agent.retried[0] != resolutionID {
		t.Fatalf("automatic retries = %v, want resolution %s", agent.retried, resolutionID)
	}
}

func TestRSSPollHandlerResumesLegacyFailedAdjudicationWithoutProposal(t *testing.T) {
	subscriptionID := uuid.MustParse("50000000-0000-0000-0000-000000000141")
	batchID := uuid.MustParse("50000000-0000-0000-0000-000000000142")
	resolutionID := uuid.MustParse("50000000-0000-0000-0000-000000000143")
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true, SourceSeason: 1, PollInterval: time.Minute,
		},
		persistResult: domain.RSSPollPersistResult{AgentAdjudicationBatchIDs: []uuid.UUID{batchID}},
	}
	agent := &rssAgentResolutionStub{
		enabled: true,
		createResult: service.AgentResolutionCommandResult{Resolution: domain.AgentResolution{
			ID: resolutionID, Version: 3, Trigger: "automatic", Status: domain.AgentResolutionFailed,
			ErrorCode: "agent_submission_invalid", Proposal: []byte(`{}`),
		}},
	}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Adjudication recovery"}}, store, 1, agent)

	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(agent.retried) != 1 || agent.retried[0] != resolutionID {
		t.Fatalf("automatic retries = %v, want legacy resolution %s", agent.retried, resolutionID)
	}
}

func TestRSSPollHandlerDoesNotRebuildNewExhaustedAdjudication(t *testing.T) {
	subscriptionID, batchID, resolutionID := uuid.New(), uuid.New(), uuid.New()
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true, SourceSeason: 1, PollInterval: time.Minute,
		},
		persistResult: domain.RSSPollPersistResult{AgentAdjudicationBatchIDs: []uuid.UUID{batchID}},
	}
	agent := &rssAgentResolutionStub{
		enabled: true,
		createResult: service.AgentResolutionCommandResult{Resolution: domain.AgentResolution{
			ID: resolutionID, Version: 5, Trigger: "automatic", Status: domain.AgentResolutionFailed,
			ErrorCode: "agent_submission_exhausted", Proposal: []byte(`{}`),
		}},
	}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Bounded adjudication"}}, store, 1, agent)

	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(agent.retried) != 0 {
		t.Fatalf("automatic retries = %v, want no retry storm", agent.retried)
	}
}

func TestRSSPollHandlerPersistsAndSchedulesEveryEligibleEntry(t *testing.T) {
	subscriptionID := uuid.MustParse("50000000-0000-0000-0000-000000000001")
	operationID := uuid.MustParse("50000000-0000-0000-0000-000000000002")
	// Fixed IDs keep expected scheduling independent from production identity logic.
	candidates := []domain.RSSEnqueueCandidate{
		{EntryID: uuid.MustParse("50000000-0000-0000-0000-000000000011"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("50000000-0000-0000-0000-000000000012"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("50000000-0000-0000-0000-000000000013"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("50000000-0000-0000-0000-000000000014"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("50000000-0000-0000-0000-000000000015"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("50000000-0000-0000-0000-000000000016"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("50000000-0000-0000-0000-000000000017"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("50000000-0000-0000-0000-000000000018"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("50000000-0000-0000-0000-000000000019"), Status: domain.RSSDiscovered, Downloadable: true},
		{EntryID: uuid.MustParse("50000000-0000-0000-0000-000000000020"), Status: domain.RSSDiscovered, Downloadable: true},
	}
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID,
			FeedURL:        "https://example.test/feed.xml",
			Enabled:        true,
			SourceSeason:   1,
			PollInterval:   5 * time.Minute,
		},
		persistResult: domain.RSSPollPersistResult{FetchedCount: 12, Candidates: candidates},
		scheduleErrors: map[uuid.UUID]error{
			candidates[1].EntryID: errors.New("first failure"),
			candidates[5].EntryID: errors.New("second failure"),
			candidates[8].EntryID: errors.New("third failure"),
		},
	}
	feedClient := &rssFeedClientStub{feed: domain.RSSFeed{Title: "Show"}}
	handler := NewRSSPollHandler(feedClient, store, 4)

	err := handler.Handle(context.Background(), domain.Operation{
		ID:           operationID,
		ResourceType: "rss_subscription",
		ResourceID:   subscriptionID,
		Payload:      []byte(`{"continuous":true}`),
	})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) || snoozeErr.Duration != 5*time.Minute {
		t.Fatalf("Handle() error = %v, want 5m snooze", err)
	}
	if len(store.scheduleCalls) != 10 {
		t.Fatalf("scheduled entries = %d, want 10", len(store.scheduleCalls))
	}
	if store.summaryCalls != 1 || store.summary.FetchedCount != 12 || store.summary.EligibleCount != 10 || store.summary.ScheduledCount != 7 || store.summary.FailedCount != 3 {
		t.Fatalf("summary = %#v calls=%d", store.summary, store.summaryCalls)
	}
}

func TestRSSPollHandlerStopsCompletedSubscriptionBeforeFetching(t *testing.T) {
	subscriptionID := uuid.MustParse("50000000-0000-0000-0000-000000000022")
	store := &rssPollStoreStub{command: domain.RSSPollCommand{
		SubscriptionID: subscriptionID,
		FeedURL:        "https://example.test/feed.xml",
		Enabled:        true,
		Completed:      true,
		PollInterval:   time.Hour,
	}}
	client := &rssFeedClientStub{}
	handler := NewRSSPollHandler(client, store, 2)

	if err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID,
	}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if client.calls != 0 || store.summaryCalls != 0 || store.persistedFeed.Title != "" {
		t.Fatalf("completed poll side effects = fetch %d summaries %d feed %#v", client.calls, store.summaryCalls, store.persistedFeed)
	}
}

func TestConfiguredRSSPollHandlerCreatesClientForEachRunnableOperation(t *testing.T) {
	subscriptionID := uuid.MustParse("50000000-0000-0000-0000-000000000021")
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID,
			FeedURL:        "https://example.test/feed.xml",
			Enabled:        true,
			PollInterval:   time.Hour,
		},
		persistResult: domain.RSSPollPersistResult{},
	}
	client := &rssFeedClientStub{feed: domain.RSSFeed{Title: "Show"}}
	factoryCalls := 0
	handler := NewConfiguredRSSPollHandler(func(context.Context) (RSSFeedClient, error) {
		factoryCalls++
		return client, nil
	}, store, 2)

	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if factoryCalls != 1 || client.calls != 1 {
		t.Fatalf("factory calls = %d, fetch calls = %d", factoryCalls, client.calls)
	}
}

func TestRSSPollHandlerRecordsContinuousFetchFailureAndSnoozes(t *testing.T) {
	subscriptionID := uuid.MustParse("50000000-0000-0000-0000-000000000003")
	store := &rssPollStoreStub{command: domain.RSSPollCommand{
		SubscriptionID: subscriptionID,
		FeedURL:        "https://example.test/feed.xml",
		Enabled:        true,
		SourceSeason:   1,
		PollInterval:   10 * time.Minute,
	}}
	feedClient := &rssFeedClientStub{err: errors.New("HTTP 503")}
	handler := NewRSSPollHandler(feedClient, store, 2)

	err := handler.Handle(context.Background(), domain.Operation{
		ID:           uuid.MustParse("50000000-0000-0000-0000-000000000004"),
		ResourceType: "rss_subscription",
		ResourceID:   subscriptionID,
		Payload:      []byte(`{"continuous":true}`),
	})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) || snoozeErr.Duration != 10*time.Minute {
		t.Fatalf("Handle() error = %v, want 10m snooze", err)
	}
	if store.failureCalls != 1 || store.failureCode != "rss_fetch_failed" {
		t.Fatalf("failure audit = calls %d code %q", store.failureCalls, store.failureCode)
	}
}

func TestRSSPollHandlerManualFailureUsesOperationRetry(t *testing.T) {
	subscriptionID := uuid.MustParse("50000000-0000-0000-0000-000000000005")
	store := &rssPollStoreStub{command: domain.RSSPollCommand{
		SubscriptionID: subscriptionID,
		FeedURL:        "https://example.test/feed.xml",
		Enabled:        true,
		SourceSeason:   1,
		PollInterval:   time.Hour,
	}}
	handler := NewRSSPollHandler(&rssFeedClientStub{err: errors.New("timeout")}, store, 2)

	err := handler.Handle(context.Background(), domain.Operation{
		ResourceType: "rss_subscription",
		ResourceID:   subscriptionID,
		Payload:      []byte(`{"continuous":false}`),
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "rss_fetch_failed" || !failure.Retryable {
		t.Fatalf("Handle() error = %#v, want retryable rss_fetch_failed", err)
	}
	if store.failureCalls != 1 {
		t.Fatalf("failure audit calls = %d, want 1", store.failureCalls)
	}
}

func TestRSSPollHandlerStopsStaleContinuousSubscriptionVersion(t *testing.T) {
	subscriptionID := uuid.MustParse("50000000-0000-0000-0000-000000000007")
	store := &rssPollStoreStub{command: domain.RSSPollCommand{
		SubscriptionID: subscriptionID,
		FeedURL:        "https://example.test/feed.xml",
		Enabled:        true,
		Version:        3,
		PollInterval:   time.Hour,
	}}
	feedClient := &rssFeedClientStub{}
	handler := NewRSSPollHandler(feedClient, store, 2)

	if err := handler.Handle(context.Background(), domain.Operation{
		ResourceType: "rss_subscription",
		ResourceID:   subscriptionID,
		Payload:      []byte(`{"continuous":true,"subscriptionVersion":2}`),
	}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if feedClient.calls != 0 {
		t.Fatalf("feed calls = %d, want 0", feedClient.calls)
	}
}

func TestRSSPollHandlerStopsWhenSubscriptionIsDisabled(t *testing.T) {
	subscriptionID := uuid.MustParse("50000000-0000-0000-0000-000000000006")
	store := &rssPollStoreStub{command: domain.RSSPollCommand{
		SubscriptionID: subscriptionID,
		FeedURL:        "https://example.test/feed.xml",
		Enabled:        false,
		PollInterval:   time.Hour,
	}}
	feedClient := &rssFeedClientStub{}
	handler := NewRSSPollHandler(feedClient, store, 2)

	if err := handler.Handle(context.Background(), domain.Operation{ResourceType: "rss_subscription", ResourceID: subscriptionID, Payload: []byte(`{"continuous":true}`)}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if feedClient.calls != 0 {
		t.Fatalf("feed calls = %d, want 0", feedClient.calls)
	}
}

func TestRSSPollHandlerSchedulesPreacquisitionMappingAgentBeforeVerifier(t *testing.T) {
	subscriptionID, scopeID := uuid.New(), uuid.New()
	preparation := domain.RSSPollMappingPreparation{ScopeID: scopeID}
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true,
			AutoEpisodeMapping: true, SourceSeason: 1, PollInterval: 3 * time.Minute, Version: 1,
		},
		mappingPreparation: &preparation,
	}
	agent := &rssAgentResolutionStub{enabled: true}
	verifier := &rssRealtimeVerifierStub{}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Offset feed"}}, store, 1, agent).
		WithRealtimeTargetVerifier(verifier)
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID,
		Payload: []byte(`{"continuous":true,"subscriptionVersion":1}`),
	})
	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) || snooze.Duration != 3*time.Minute {
		t.Fatalf("Handle() error = %v, want Mapping wait snooze", err)
	}
	if len(agent.created) != 1 || agent.created[0].Capability != domain.AgentCapabilityRSSPreacquisitionMapping || agent.created[0].ResourceID != scopeID {
		t.Fatalf("Agent requests = %#v", agent.created)
	}
	if len(verifier.subscriptionCalls) != 0 || store.persistedFeed.Title != "" || len(store.scheduleCalls) != 0 {
		t.Fatalf("pending Mapping reached verifier/persist/enqueue: %v/%q/%v", verifier.subscriptionCalls, store.persistedFeed.Title, store.scheduleCalls)
	}
}

func TestRSSPollHandlerSchedulesCoordinateAgentBeforePreacquisitionMapping(t *testing.T) {
	subscriptionID, entryID := uuid.New(), uuid.New()
	preparation := domain.RSSPollMappingPreparation{AgentCoordinateCandidates: []uuid.UUID{entryID}}
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true,
			AutoEpisodeMapping: true, SourceSeason: 1, PollInterval: time.Minute,
		},
		mappingPreparation: &preparation,
	}
	agent := &rssAgentResolutionStub{enabled: true}
	verifier := &rssRealtimeVerifierStub{}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Ambiguous coordinate"}}, store, 1, agent).
		WithRealtimeTargetVerifier(verifier)
	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(agent.created) != 1 || agent.created[0].Capability != domain.AgentCapabilityRSSCoordinate || agent.created[0].ResourceID != entryID {
		t.Fatalf("Agent requests = %#v", agent.created)
	}
	if len(verifier.subscriptionCalls) != 0 || store.persistedFeed.Title != "" || len(store.scheduleCalls) != 0 {
		t.Fatalf("coordinate fallback reached verifier/persist/enqueue: %v/%q/%v", verifier.subscriptionCalls, store.persistedFeed.Title, store.scheduleCalls)
	}
}

func TestRSSPollHandlerStopsOldPollAfterMappingProfileApply(t *testing.T) {
	subscriptionID := uuid.New()
	preparation := domain.RSSPollMappingPreparation{Ready: true, Applied: true}
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true,
			AutoEpisodeMapping: true, SourceSeason: 1, PollInterval: time.Minute,
		},
		mappingPreparation: &preparation,
	}
	verifier := &rssRealtimeVerifierStub{}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Mapped"}}, store, 1).
		WithRealtimeTargetVerifier(verifier)
	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(verifier.subscriptionCalls) != 0 || store.persistedFeed.Title != "" || store.summaryCalls != 0 {
		t.Fatalf("old-version poll continued after Mapping apply: verifier=%v feed=%q summaries=%d", verifier.subscriptionCalls, store.persistedFeed.Title, store.summaryCalls)
	}
}

func TestRSSPollHandlerStopsOldPollWhenScopeDeterministicRetryApplies(t *testing.T) {
	subscriptionID, scopeID := uuid.New(), uuid.New()
	preparation := domain.RSSPollMappingPreparation{ScopeID: scopeID}
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true,
			AutoEpisodeMapping: true, SourceSeason: 1, PollInterval: 2 * time.Minute, Version: 1,
		},
		mappingPreparation: &preparation,
	}
	agent := &rssAgentResolutionStub{createErr: service.NewError(
		"agent_resolution_not_required", "deterministic Mapping applied", service.ErrStateConflict, nil,
	)}
	verifier := &rssRealtimeVerifierStub{}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Mapped after catalog"}}, store, 1, agent).
		WithRealtimeTargetVerifier(verifier)
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID,
		Payload: []byte(`{"continuous":true,"subscriptionVersion":1}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v, want old poll exit", err)
	}
	if store.failureCalls != 0 || len(verifier.subscriptionCalls) != 0 || store.persistedFeed.Title != "" || store.summaryCalls != 0 {
		t.Fatalf("deterministic retry continued old poll: failures=%d verifier=%v feed=%q summaries=%d",
			store.failureCalls, verifier.subscriptionCalls, store.persistedFeed.Title, store.summaryCalls)
	}
}

func TestRSSPollHandlerRecordsCatalogPendingBeforeVerifier(t *testing.T) {
	subscriptionID, scopeID := uuid.New(), uuid.New()
	preparation := domain.RSSPollMappingPreparation{ScopeID: scopeID}
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true,
			AutoEpisodeMapping: true, SourceSeason: 1, PollInterval: 2 * time.Minute,
		},
		mappingPreparation: &preparation,
	}
	agent := &rssAgentResolutionStub{createErr: service.NewError(
		"tmdb_catalog_missing", "the TMDb series catalog has not been synchronized", service.ErrStateConflict, nil,
	)}
	verifier := &rssRealtimeVerifierStub{}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Catalog pending"}}, store, 1, agent).
		WithRealtimeTargetVerifier(verifier)
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID, Payload: []byte(`{"continuous":true}`),
	})
	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) || snooze.Duration != 2*time.Minute {
		t.Fatalf("Handle() error = %v, want safe Mapping snooze", err)
	}
	if store.failureCalls != 1 || store.failureCode != "tmdb_catalog_missing" || len(verifier.subscriptionCalls) != 0 || store.persistedFeed.Title != "" {
		t.Fatalf("catalog wait audit/verifier/persist = %d/%q/%v/%q", store.failureCalls, store.failureCode, verifier.subscriptionCalls, store.persistedFeed.Title)
	}
}

func TestRSSPollHandlerKeepsAgentUnavailableMappingBeforeVerifier(t *testing.T) {
	subscriptionID, scopeID := uuid.New(), uuid.New()
	preparation := domain.RSSPollMappingPreparation{ScopeID: scopeID}
	store := &rssPollStoreStub{
		command: domain.RSSPollCommand{
			SubscriptionID: subscriptionID, FeedURL: "https://example.test/feed.xml", Enabled: true,
			AutoEpisodeMapping: true, SourceSeason: 1, PollInterval: 2 * time.Minute,
		},
		mappingPreparation: &preparation,
	}
	verifier := &rssRealtimeVerifierStub{}
	handler := NewRSSPollHandler(&rssFeedClientStub{feed: domain.RSSFeed{Title: "Agent unavailable"}}, store, 1).
		WithRealtimeTargetVerifier(verifier)
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "rss_subscription", ResourceID: subscriptionID, Payload: []byte(`{"continuous":true}`),
	})
	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) || snooze.Duration != 2*time.Minute {
		t.Fatalf("Handle() error = %v, want safe Mapping snooze", err)
	}
	if store.failureCalls != 1 || store.failureCode != "rss_preacquisition_mapping_pending" || len(verifier.subscriptionCalls) != 0 || store.persistedFeed.Title != "" {
		t.Fatalf("safe wait audit/verifier/persist = %d/%q/%v/%q", store.failureCalls, store.failureCode, verifier.subscriptionCalls, store.persistedFeed.Title)
	}
}
