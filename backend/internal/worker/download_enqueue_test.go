package worker

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/onprs/emby-auto/backend/internal/service"
)

const workerTorrentHash = "0123456789abcdef0123456789abcdef01234567"

var validTorrentFixture = []byte("d8:announce35:http://tracker.example.test:8080/announce4:infod6:lengthi12345e4:name10:testvideoseee")

func stubTorrentFetcher(bytes []byte, err error) torrentSourceFetcher {
	return func(context.Context, string, domain.NetworkProxySettings) ([]byte, error) {
		if err != nil {
			return nil, err
		}
		if bytes != nil {
			return bytes, nil
		}
		return validTorrentFixture, nil
	}
}

type downloadConfigurationStub struct {
	configuration domain.Configuration
	password      string
	loadErr       error
	secretErr     error
}

func (stub *downloadConfigurationStub) Load(context.Context) (domain.Configuration, error) {
	return stub.configuration, stub.loadErr
}

func (stub *downloadConfigurationStub) ResolveSecret(_ context.Context, name string) (string, error) {
	if name != domain.SecretQBittorrentPassword {
		return "", errors.New("unexpected secret name")
	}
	return stub.password, stub.secretErr
}

type downloadStoreStub struct {
	command         domain.DownloadEnqueueCommand
	loadErr         error
	completion      domain.DownloadEnqueueCompletion
	completeErr     error
	completed       bool
	legacyCompleted bool
}

func (stub *downloadStoreStub) LoadEnqueueCommand(context.Context, uuid.UUID) (domain.DownloadEnqueueCommand, error) {
	return stub.command, stub.loadErr
}

func (stub *downloadStoreStub) CompleteEnqueue(_ context.Context, completion domain.DownloadEnqueueCompletion) error {
	stub.completion = completion
	stub.completed = true
	return stub.completeErr
}

func (stub *downloadStoreStub) CompleteLegacyEnqueue(_ context.Context, completion domain.DownloadEnqueueCompletion) error {
	stub.completion = completion
	stub.legacyCompleted = true
	return stub.completeErr
}

type downloadAgentResolutionStub struct {
	created []service.AutomaticAgentResolutionRequest
}

func (stub *downloadAgentResolutionStub) CreateAutomatic(_ context.Context, input service.AutomaticAgentResolutionRequest) (service.AgentResolutionCommandResult, error) {
	stub.created = append(stub.created, input)
	return service.AgentResolutionCommandResult{}, nil
}

type torrentClientStub struct {
	resolution        qbittorrent.HashResolution
	files             []qbittorrent.TorrentFile
	fileResponses     [][]qbittorrent.TorrentFile
	torrents          []qbittorrent.Torrent
	loginErr          error
	addErr            error
	filesErr          error
	listErr           error
	priorityErr       error
	rateLimitErr      error
	ensureCategoryErr error
	categoryErr       error
	deleteCategoryErr error
	resumeErr         error
	deleteErr         error
	addRequest        qbittorrent.AddRequest
	priorityCalls     []priorityCall
	rateLimitCalls    []rateLimitCall
	ensuredCategories []string
	categoryCalls     []string
	deletedCategories []string
	deletedHashes     []string
	calls             []string
}

type priorityCall struct {
	indexes  []int
	priority int
}

type rateLimitCall struct {
	downloadBytesPerSecond int64
	uploadBytesPerSecond   int64
}

func (stub *torrentClientStub) Login(context.Context) error {
	stub.calls = append(stub.calls, "login")
	return stub.loginErr
}

func (stub *torrentClientStub) AddAndConfirm(_ context.Context, request qbittorrent.AddRequest) (qbittorrent.HashResolution, error) {
	stub.calls = append(stub.calls, "add")
	stub.addRequest = request
	return stub.resolution, stub.addErr
}

func (stub *torrentClientStub) TorrentFiles(context.Context, string) ([]qbittorrent.TorrentFile, error) {
	stub.calls = append(stub.calls, "files")
	if len(stub.fileResponses) > 0 {
		files := stub.fileResponses[0]
		stub.fileResponses = stub.fileResponses[1:]
		return files, stub.filesErr
	}
	return stub.files, stub.filesErr
}

func (stub *torrentClientStub) SetFilePriority(_ context.Context, _ string, indexes []int, priority int) error {
	stub.calls = append(stub.calls, "priority")
	stub.priorityCalls = append(stub.priorityCalls, priorityCall{indexes: append([]int(nil), indexes...), priority: priority})
	return stub.priorityErr
}

func (stub *torrentClientStub) SetTorrentRateLimits(_ context.Context, _ string, downloadBytesPerSecond, uploadBytesPerSecond int64) error {
	stub.calls = append(stub.calls, "rate-limits")
	stub.rateLimitCalls = append(stub.rateLimitCalls, rateLimitCall{
		downloadBytesPerSecond: downloadBytesPerSecond,
		uploadBytesPerSecond:   uploadBytesPerSecond,
	})
	return stub.rateLimitErr
}

func (stub *torrentClientStub) EnsureCategory(_ context.Context, category string) error {
	stub.calls = append(stub.calls, "ensure-category")
	stub.ensuredCategories = append(stub.ensuredCategories, category)
	return stub.ensureCategoryErr
}

func (stub *torrentClientStub) SetTorrentCategory(_ context.Context, hash, category string) error {
	stub.calls = append(stub.calls, "set-category")
	stub.categoryCalls = append(stub.categoryCalls, hash+"|"+category)
	return stub.categoryErr
}

func (stub *torrentClientStub) DeleteCategory(_ context.Context, category string) error {
	stub.calls = append(stub.calls, "delete-category")
	stub.deletedCategories = append(stub.deletedCategories, category)
	return stub.deleteCategoryErr
}

func (stub *torrentClientStub) ResumeTorrent(context.Context, string) error {
	stub.calls = append(stub.calls, "resume")
	return stub.resumeErr
}

func (stub *torrentClientStub) DeleteTorrent(_ context.Context, hash string, _ bool) error {
	stub.calls = append(stub.calls, "delete")
	stub.deletedHashes = append(stub.deletedHashes, hash)
	return stub.deleteErr
}

func TestRateLimitBytesPerSecondEnforcesQBittorrentRange(t *testing.T) {
	tests := []struct {
		name    string
		limit   int64
		want    int64
		wantErr bool
	}{
		{name: "unlimited", limit: 0, want: 0},
		{name: "largest representable KiB value", limit: 2097151, want: 2147482624},
		{name: "above qBittorrent range", limit: 2097152, wantErr: true},
		{name: "negative", limit: -1, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := rateLimitBytesPerSecond(test.limit)
			if test.wantErr {
				if err == nil {
					t.Fatalf("rateLimitBytesPerSecond(%d) accepted an invalid limit", test.limit)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("rateLimitBytesPerSecond(%d) = %d, %v; want %d, nil", test.limit, got, err, test.want)
			}
		})
	}
}

func TestDownloadEnqueueHandlerConfirmsHashSelectsFilesAndPersistsCompletion(t *testing.T) {
	downloadID := uuid.MustParse("30000000-0000-0000-0000-000000000001")
	operationID := uuid.MustParse("30000000-0000-0000-0000-000000000002")
	configuration := &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{
				URL: "http://qb:8080", Username: "admin",
				DownloadRateLimitKibPerSecond: 2048, UploadRateLimitKibPerSecond: 512,
			},
			Paths: domain.PathSettings{DownloadRoot: "/downloads"},
			Agent: domain.AgentSettings{Enabled: true, DownloadFileSelectionMode: domain.AgentResolutionValidatedAuto},
		}},
		password: "secret",
	}
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID: downloadID,
		Status:     domain.DownloadEnqueuePending,
		SourceURI:  "https://example.test/show.torrent",
	}}
	client := &torrentClientStub{
		resolution: qbittorrent.HashResolution{Hash: workerTorrentHash, Reason: qbittorrent.HashResolutionNew},
		files: []qbittorrent.TorrentFile{
			{Index: 0, Name: "Show/Show - 01.mkv", Size: 1_000},
			{Index: 1, Name: "Show/Show - 01.zh-Hans.ass", Size: 100},
			{Index: 2, Name: "Show/Show - 01.zh-Hant.ass", Size: 100},
			{Index: 3, Name: "Show/NCOP.mkv", Size: 4_000},
			{Index: 4, Name: "Show/readme.txt", Size: 10},
		},
	}
	factoryCalls := 0
	agent := &downloadAgentResolutionStub{}
	handler := NewDownloadEnqueueHandler(configuration, store, func(options qbittorrent.ClientOptions) (TorrentClient, error) {
		factoryCalls++
		if options.BaseURL != "http://qb:8080" || options.Username != "admin" || options.Password != "secret" {
			t.Fatalf("client options = %#v", options)
		}
		return client, nil
	}, agent).WithTorrentSourceFetcher(stubTorrentFetcher(validTorrentFixture, nil))

	err := handler.Handle(context.Background(), domain.Operation{
		ID:           operationID,
		Kind:         "download.enqueue",
		ResourceType: "download",
		ResourceID:   downloadID,
		Payload:      []byte(`{"defaultSeason":1,"singleEpisode":false}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if factoryCalls != 1 || !store.completed {
		t.Fatalf("factory calls/completed = %d/%t, want 1/true", factoryCalls, store.completed)
	}
	if len(client.addRequest.Torrent) == 0 || string(client.addRequest.Torrent) != string(validTorrentFixture) || client.addRequest.TorrentFilename != "source.torrent" || client.addRequest.Source != "" || client.addRequest.SavePath != "/downloads/30000000-0000-0000-0000-000000000001" || client.addRequest.Category != "emby-auto-30000000-0000-0000-0000-000000000001" {
		t.Fatalf("add request = %#v", client.addRequest)
	}
	if !reflect.DeepEqual(client.calls, []string{"login", "ensure-category", "ensure-category", "list", "add", "files"}) {
		t.Fatalf("client calls = %v", client.calls)
	}
	if len(client.priorityCalls) != 0 || len(client.rateLimitCalls) != 0 || len(client.categoryCalls) != 0 || len(client.deletedCategories) != 0 {
		t.Fatalf("enqueue performed selection side effects: priorities=%v rates=%v categories=%v deletions=%v", client.priorityCalls, client.rateLimitCalls, client.categoryCalls, client.deletedCategories)
	}
	if store.completion.DownloadID != downloadID || store.completion.OperationID != operationID || store.completion.TorrentHash != workerTorrentHash || store.completion.SavePath != client.addRequest.SavePath {
		t.Fatalf("completion identity = %#v", store.completion)
	}
	if store.completion.Outcome != domain.DownloadManifestResolved {
		t.Fatalf("completion outcome = %q", store.completion.Outcome)
	}
	if len(agent.created) != 0 {
		t.Fatalf("resolved manifest created Agent resolutions = %#v", agent.created)
	}
	if len(store.completion.Files) != 5 || store.completion.Files[0].Kind != domain.MediaVideo || !store.completion.Files[0].Selected || store.completion.Files[1].Kind != domain.MediaSubtitle || !store.completion.Files[1].Selected || store.completion.Files[2].Language != "zh-Hant" || !store.completion.Files[2].Selected || store.completion.Files[3].Kind != domain.MediaExtra || store.completion.Files[3].Selected {
		t.Fatalf("completion files = %#v", store.completion.Files)
	}
}

func TestDownloadEnqueueHandlerWaitsForTorrentMetadata(t *testing.T) {
	downloadID := uuid.MustParse("30000000-0000-0000-0000-000000000011")
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
		Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
	}}}
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID: downloadID,
		Status:     domain.DownloadEnqueuePending,
		SourceURI:  "magnet:?xt=urn:btih:" + workerTorrentHash,
	}}
	client := &torrentClientStub{
		resolution: qbittorrent.HashResolution{Hash: workerTorrentHash, Reason: qbittorrent.HashResolutionMagnet},
		fileResponses: [][]qbittorrent.TorrentFile{
			{},
			{{Index: 0, Name: "Show - S01E01.mkv", Size: 1_000}},
		},
	}
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	})
	handler.metadataPollInterval = time.Nanosecond
	handler.metadataTimeout = time.Second

	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "download", ResourceID: downloadID,
		Payload: []byte(`{"defaultSeason":1,"defaultEpisode":1,"singleEpisode":true}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !store.completed || countCalls(client.calls, "files") != 2 {
		t.Fatalf("completed/files calls = %t/%v, want true/two metadata reads", store.completed, client.calls)
	}
}

func TestDownloadEnqueueHandlerTreatsMissingMetadataAsRetryable(t *testing.T) {
	downloadID := uuid.MustParse("30000000-0000-0000-0000-000000000012")
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
		Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
	}}}
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID: downloadID,
		Status:     domain.DownloadEnqueuePending,
		SourceURI:  "magnet:?xt=urn:btih:" + workerTorrentHash,
	}}
	client := &torrentClientStub{resolution: qbittorrent.HashResolution{Hash: workerTorrentHash, Reason: qbittorrent.HashResolutionMagnet}}
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	})
	handler.metadataPollInterval = time.Nanosecond
	handler.metadataTimeout = time.Millisecond

	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "download", ResourceID: downloadID,
		Payload: []byte(`{"defaultSeason":1,"defaultEpisode":1,"singleEpisode":true}`),
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "qbittorrent_files_unavailable" || !failure.Retryable || !errors.Is(failure, errTorrentMetadataUnavailable) {
		t.Fatalf("Handle() error = %#v, want retryable metadata failure", err)
	}
	if store.completed || !reflect.DeepEqual(client.deletedHashes, []string{workerTorrentHash}) {
		t.Fatalf("completed/deleted hashes = %t/%v", store.completed, client.deletedHashes)
	}
}

func TestWaitForTorrentFilesHonorsContextCancellation(t *testing.T) {
	client := &torrentClientStub{}
	handler := &DownloadEnqueueHandler{metadataPollInterval: time.Hour, metadataTimeout: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := handler.waitForTorrentFiles(ctx, client, workerTorrentHash)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForTorrentFiles() error = %v, want context cancellation", err)
	}
}

func countCalls(calls []string, target string) int {
	count := 0
	for _, call := range calls {
		if call == target {
			count++
		}
	}
	return count
}

func TestDownloadEnqueueHandlerCompensatesOnlyNewTorrentAfterPersistenceFailure(t *testing.T) {
	downloadID := uuid.MustParse("30000000-0000-0000-0000-000000000009")
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
		Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
	}}}
	for _, test := range []struct {
		name       string
		reason     qbittorrent.HashResolutionReason
		wantDelete bool
	}{
		{name: "new torrent is removed", reason: qbittorrent.HashResolutionNew, wantDelete: true},
		{name: "existing torrent is retained", reason: qbittorrent.HashResolutionExisting, wantDelete: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &downloadStoreStub{
				command:     domain.DownloadEnqueueCommand{DownloadID: downloadID, Status: domain.DownloadEnqueuePending, SourceURI: "magnet:?xt=urn:btih:" + workerTorrentHash},
				completeErr: domain.ErrDuplicateTorrent,
			}
			client := &torrentClientStub{
				resolution: qbittorrent.HashResolution{Hash: workerTorrentHash, Reason: test.reason},
				files:      []qbittorrent.TorrentFile{{Index: 0, Name: "Show - S01E01.mkv", Size: 1_000}},
			}
			handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
				return client, nil
			})

			err := handler.Handle(context.Background(), domain.Operation{
				ID: uuid.New(), ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{"defaultSeason":1,"defaultEpisode":1,"singleEpisode":true}`),
			})
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != "duplicate_torrent" || failure.Retryable {
				t.Fatalf("Handle() error = %#v, want permanent duplicate_torrent", err)
			}
			if got := len(client.deletedHashes) == 1; got != test.wantDelete {
				t.Fatalf("DeleteTorrent calls = %v, wantDelete=%t", client.deletedHashes, test.wantDelete)
			}
		})
	}
}

func TestDownloadEnqueueHandlerTreatsPersistedDownloadAsIdempotentSuccess(t *testing.T) {
	downloadID := uuid.MustParse("30000000-0000-0000-0000-000000000003")
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		SourceURI:   "magnet:?xt=urn:btih:" + workerTorrentHash,
		TorrentHash: workerTorrentHash,
	}}
	factoryCalled := false
	handler := NewDownloadEnqueueHandler(&downloadConfigurationStub{}, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		factoryCalled = true
		return nil, nil
	})

	err := handler.Handle(context.Background(), domain.Operation{ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if factoryCalled || store.completed {
		t.Fatalf("idempotent replay called factory/completion = %t/%t", factoryCalled, store.completed)
	}
}

func TestDownloadEnqueueHandlerPersistsUnresolvedAndHardRejectedManifests(t *testing.T) {
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
		Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
		Agent:       domain.AgentSettings{Enabled: true, DownloadFileSelectionMode: domain.AgentResolutionValidatedAuto},
	}}}
	tests := []struct {
		name           string
		file           qbittorrent.TorrentFile
		outcome        domain.DownloadManifestOutcome
		reason         string
		wantAgentCalls int
	}{
		{name: "supported video without coordinate waits for resolution", file: qbittorrent.TorrentFile{Index: 0, Name: "Show/unknown.mkv", Size: 1000}, outcome: domain.DownloadManifestUnresolved, reason: "download_file_resolution_required", wantAgentCalls: 1},
		{name: "extra-only manifest is hard rejected after persistence", file: qbittorrent.TorrentFile{Index: 0, Name: "Show/NCOP.mkv", Size: 1000}, outcome: domain.DownloadManifestHardRejected, reason: "download_no_main_video"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			downloadID := uuid.New()
			store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{DownloadID: downloadID, Status: domain.DownloadEnqueuePending, SourceURI: "https://example.test/show.torrent"}}
			client := &torrentClientStub{resolution: qbittorrent.HashResolution{Hash: workerTorrentHash}, files: []qbittorrent.TorrentFile{test.file}}
			agent := &downloadAgentResolutionStub{}
			handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) { return client, nil }, agent).WithTorrentSourceFetcher(stubTorrentFetcher(validTorrentFixture, nil))
			err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{"defaultSeason":1}`)})
			if err != nil {
				t.Fatalf("Handle() error = %v", err)
			}
			if !store.completed || store.completion.Outcome != test.outcome || store.completion.ReasonCode != test.reason {
				t.Fatalf("completion = %#v", store.completion)
			}
			if len(agent.created) != test.wantAgentCalls {
				t.Fatalf("Agent resolutions = %d, want %d", len(agent.created), test.wantAgentCalls)
			}
			if len(client.deletedHashes) != 0 {
				t.Fatalf("persisted torrent was compensated: %v", client.deletedHashes)
			}
		})
	}
}

func TestDownloadEnqueueHandlerUsesLegacyFlowAndImmediatelyTriggersEpisodeMapping(t *testing.T) {
	downloadID := uuid.New()
	acquisitionID := uuid.New()
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080", DownloadRateLimitKibPerSecond: 2, UploadRateLimitKibPerSecond: 1},
		Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
	}}}
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID: downloadID, AcquisitionID: acquisitionID, Status: domain.DownloadEnqueuePending, SourceURI: "https://example.test/show.torrent",
	}}
	client := &torrentClientStub{
		resolution: qbittorrent.HashResolution{Hash: workerTorrentHash, Reason: qbittorrent.HashResolutionNew},
		files:      []qbittorrent.TorrentFile{{Index: 0, Name: "Show - S01E01.mkv", Size: 1000}},
	}
	agent := &downloadAgentResolutionStub{}
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, agent).WithManifestResolutionEnabled(false).WithTorrentSourceFetcher(stubTorrentFetcher(validTorrentFixture, nil))

	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "download", ResourceID: downloadID,
		Payload: []byte(`{"defaultSeason":1,"defaultEpisode":1,"singleEpisode":true}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !store.legacyCompleted || store.completed {
		t.Fatalf("legacy/new completion = %t/%t, want true/false", store.legacyCompleted, store.completed)
	}
	if len(client.priorityCalls) != 2 || len(client.rateLimitCalls) != 1 || len(client.categoryCalls) != 1 || countCalls(client.calls, "resume") != 1 {
		t.Fatalf("legacy qB side effects = calls %v priorities %v rates %v categories %v", client.calls, client.priorityCalls, client.rateLimitCalls, client.categoryCalls)
	}
	if len(agent.created) != 1 || agent.created[0].Capability != domain.AgentCapabilityEpisodeMapping || agent.created[0].ResourceID != acquisitionID {
		t.Fatalf("legacy Mapping trigger = %#v", agent.created)
	}
	if len(client.deletedHashes) != 0 || store.completion.Outcome != "" {
		t.Fatalf("legacy completion unexpectedly compensated or used manifest outcome: deleted=%v completion=%#v", client.deletedHashes, store.completion)
	}
}

func TestDownloadEnqueueHandlerClassifiesMediaAndConfigurationFailures(t *testing.T) {
	downloadID := uuid.MustParse("30000000-0000-0000-0000-000000000004")
	validConfiguration := domain.Configuration{Settings: domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
		Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
	}}
	tests := []struct {
		name          string
		configuration *downloadConfigurationStub
		client        *torrentClientStub
		wantCode      string
		wantRetryable bool
	}{
		{
			name:          "missing qB URL is permanent",
			configuration: &downloadConfigurationStub{configuration: domain.Configuration{}},
			client:        &torrentClientStub{},
			wantCode:      "qbittorrent_not_configured",
			wantRetryable: false,
		},
		{
			name:          "qB login failure retries",
			configuration: &downloadConfigurationStub{configuration: validConfiguration, password: "secret"},
			client:        &torrentClientStub{loginErr: errors.New("connection refused")},
			wantCode:      "qbittorrent_unavailable",
			wantRetryable: true,
		},
		{
			name:          "category preparation failure retries",
			configuration: &downloadConfigurationStub{configuration: validConfiguration, password: "secret"},
			client:        &torrentClientStub{ensureCategoryErr: errors.New("category unavailable")},
			wantCode:      "qbittorrent_category_failed",
			wantRetryable: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
				DownloadID: downloadID,
				Status:     domain.DownloadEnqueuePending,
				SourceURI:  "https://example.test/show.torrent",
			}}
			handler := NewDownloadEnqueueHandler(test.configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
				return test.client, nil
			}).WithTorrentSourceFetcher(stubTorrentFetcher(validTorrentFixture, nil))
			err := handler.Handle(context.Background(), domain.Operation{
				ResourceType: "download",
				ResourceID:   downloadID,
				Payload:      []byte(`{"defaultSeason":1}`),
			})
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != test.wantCode || failure.Retryable != test.wantRetryable {
				t.Fatalf("Handle() error = %#v, want Failure %q retryable=%t", err, test.wantCode, test.wantRetryable)
			}
			if store.completed {
				t.Fatal("failed handler persisted completion")
			}
		})
	}
}
