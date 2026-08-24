package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type readModelStub struct {
	downloads       domain.DownloadPage
	download        domain.DownloadView
	acquisitions    domain.AcquisitionPage
	acquisition     domain.AcquisitionView
	searches        domain.SearchRunSummaryPage
	operations      domain.OperationPage
	operation       domain.OperationView
	entries         domain.RSSEntryPage
	events          domain.EventRecordPage
	summary         domain.DashboardSummary
	err             error
	listState       *string
	listPhase       *string
	listQuery       *string
	listSortBy      *string
	listSortOrder   *string
	rssGroup        *string
	rssQuery        *string
	rssRejectReason *string
	operationKind   *string
}

func (stub *readModelStub) ListSearches(context.Context, *uuid.UUID, int, *string, *string) (domain.SearchRunSummaryPage, error) {
	return stub.searches, stub.err
}
func (stub *readModelStub) ListDownloads(_ context.Context, _ *uuid.UUID, _ int, status, phase, query, sortBy, sortOrder *string) (domain.DownloadPage, error) {
	stub.listState = status
	stub.listPhase = phase
	stub.listQuery = query
	stub.listSortBy = sortBy
	stub.listSortOrder = sortOrder
	return stub.downloads, stub.err
}
func (stub *readModelStub) GetDownload(context.Context, uuid.UUID) (domain.DownloadView, error) {
	return stub.download, stub.err
}
func (stub *readModelStub) ListAcquisitions(_ context.Context, _ *uuid.UUID, _ int, _ *string, _ *int64, _ *string, sortBy, sortOrder *string) (domain.AcquisitionPage, error) {
	stub.listSortBy, stub.listSortOrder = sortBy, sortOrder
	return stub.acquisitions, stub.err
}
func (stub *readModelStub) GetAcquisition(context.Context, uuid.UUID) (domain.AcquisitionView, error) {
	return stub.acquisition, stub.err
}
func (stub *readModelStub) ListRSSEntries(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ int, _ *string, group, query, rejectReason, sortBy, sortOrder *string) (domain.RSSEntryPage, error) {
	stub.rssGroup = group
	stub.rssQuery = query
	stub.rssRejectReason = rejectReason
	stub.listSortBy, stub.listSortOrder = sortBy, sortOrder
	return stub.entries, stub.err
}
func (stub *readModelStub) ListOperations(_ context.Context, _ *uuid.UUID, _ int, _ *string, _ *uuid.UUID, status *string) (domain.OperationPage, error) {
	stub.operationKind = status
	return stub.operations, stub.err
}
func (stub *readModelStub) GetOperation(context.Context, uuid.UUID) (domain.OperationView, error) {
	return stub.operation, stub.err
}
func (stub *readModelStub) ListResourceEvents(context.Context, string, uuid.UUID, *uuid.UUID, int) (domain.EventRecordPage, error) {
	return stub.events, stub.err
}
func (stub *readModelStub) DashboardSummary(context.Context) (domain.DashboardSummary, error) {
	return stub.summary, stub.err
}

func TestListDownloadsMapsStatusFilterAndPage(t *testing.T) {
	now := time.Date(2026, time.July, 22, 1, 0, 0, 0, time.UTC)
	downloadID := uuid.MustParse("88000000-0000-0000-0000-000000000001")
	stub := &readModelStub{downloads: domain.DownloadPage{Items: []domain.DownloadView{{
		ID: downloadID, AcquisitionID: uuid.New(), Attempt: 1, ClientName: "qbittorrent",
		Status: "downloading", Progress: 0.5, Version: 2, ClientState: "downloading", LastSyncedAt: &now,
		Actions: domain.DownloadActions{CanCancel: true}, CreatedAt: now, UpdatedAt: now,
	}}}}
	handler := NewHandler(NewServer(readinessStub{}, WithReadModels(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/downloads?status=downloading&phase=paused&query=Frieren&sortBy=progress&sortOrder=asc&limit=25", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.listState == nil || *stub.listState != "downloading" {
		t.Fatalf("status filter = %#v", stub.listState)
	}
	if stub.listPhase == nil || *stub.listPhase != "paused" {
		t.Fatalf("phase filter = %#v", stub.listPhase)
	}
	if stub.listQuery == nil || *stub.listQuery != "Frieren" {
		t.Fatalf("query filter = %#v", stub.listQuery)
	}
	if stub.listSortBy == nil || *stub.listSortBy != "progress" || stub.listSortOrder == nil || *stub.listSortOrder != "asc" {
		t.Fatalf("sort = %#v/%#v", stub.listSortBy, stub.listSortOrder)
	}
	var body DownloadPage
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].Id != downloadID || body.Items[0].Status != DownloadStatusDownloading || body.Items[0].Version != 2 ||
		body.Items[0].ClientState == nil || *body.Items[0].ClientState != "downloading" || !body.Items[0].Actions.CanCancel {
		t.Fatalf("response = %#v", body)
	}
}

func TestListAcquisitionsForwardsColumnSort(t *testing.T) {
	stub := &readModelStub{acquisitions: domain.AcquisitionPage{Items: []domain.AcquisitionView{}}}
	handler := NewHandler(NewServer(readinessStub{}, WithReadModels(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/acquisitions?sortBy=source_kind&sortOrder=desc", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.listSortBy == nil || *stub.listSortBy != "source_kind" || stub.listSortOrder == nil || *stub.listSortOrder != "desc" {
		t.Fatalf("sort = %#v/%#v", stub.listSortBy, stub.listSortOrder)
	}
}

func TestListRSSEntriesForwardsColumnSort(t *testing.T) {
	stub := &readModelStub{entries: domain.RSSEntryPage{Items: []domain.RSSEntryView{}}}
	handler := NewHandler(NewServer(readinessStub{}, WithReadModels(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/rss/subscriptions/"+uuid.NewString()+"/entries?group=confirmed&sortBy=episode&sortOrder=asc&query=ep&rejectReason=target_episode_in_library", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.rssGroup == nil || *stub.rssGroup != "confirmed" || stub.rssQuery == nil || *stub.rssQuery != "ep" || stub.rssRejectReason == nil || *stub.rssRejectReason != "target_episode_in_library" || stub.listSortBy == nil || *stub.listSortBy != "episode" || stub.listSortOrder == nil || *stub.listSortOrder != "asc" {
		t.Fatalf("group/query/rejectReason/sort = %#v/%#v/%#v/%#v/%#v", stub.rssGroup, stub.rssQuery, stub.rssRejectReason, stub.listSortBy, stub.listSortOrder)
	}
}

func TestListRSSEntriesValidatesFilterUnicodeLengths(t *testing.T) {
	cases := []struct {
		name        string
		parameter   string
		value       string
		wantStatus  int
		wantForward bool
	}{
		{name: "empty query", parameter: "query", value: "", wantStatus: http.StatusBadRequest},
		{name: "one-character query", parameter: "query", value: "集", wantStatus: http.StatusOK, wantForward: true},
		{name: "maximum query", parameter: "query", value: strings.Repeat("集", 256), wantStatus: http.StatusOK, wantForward: true},
		{name: "query too long", parameter: "query", value: strings.Repeat("集", 257), wantStatus: http.StatusBadRequest},
		{name: "empty reject reason", parameter: "rejectReason", value: "", wantStatus: http.StatusBadRequest},
		{name: "one-character reject reason", parameter: "rejectReason", value: "因", wantStatus: http.StatusOK, wantForward: true},
		{name: "maximum reject reason", parameter: "rejectReason", value: strings.Repeat("因", 128), wantStatus: http.StatusOK, wantForward: true},
		{name: "reject reason too long", parameter: "rejectReason", value: strings.Repeat("因", 129), wantStatus: http.StatusBadRequest},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			stub := &readModelStub{entries: domain.RSSEntryPage{Items: []domain.RSSEntryView{}}}
			handler := NewHandler(NewServer(readinessStub{}, WithReadModels(stub)))
			request := httptest.NewRequest(
				http.MethodGet,
				"/api/v1/rss/subscriptions/"+uuid.NewString()+"/entries?"+test.parameter+"="+url.QueryEscape(test.value),
				nil,
			)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("%s=%d characters status = %d, want %d", test.parameter, len([]rune(test.value)), response.Code, test.wantStatus)
			}
			if !test.wantForward {
				if stub.rssQuery != nil || stub.rssRejectReason != nil {
					t.Fatalf("invalid filters reached the service: %#v/%#v", stub.rssQuery, stub.rssRejectReason)
				}
				return
			}
			if test.parameter == "query" {
				if stub.rssQuery == nil || *stub.rssQuery != test.value || stub.rssRejectReason != nil {
					t.Fatalf("forwarded query/rejectReason = %#v/%#v", stub.rssQuery, stub.rssRejectReason)
				}
			} else if stub.rssRejectReason == nil || *stub.rssRejectReason != test.value || stub.rssQuery != nil {
				t.Fatalf("forwarded query/rejectReason = %#v/%#v", stub.rssQuery, stub.rssRejectReason)
			}
		})
	}
}

func TestRSSEntryResponseIncludesAcquisitionProgress(t *testing.T) {
	acquisitionID := uuid.MustParse("88000000-0000-0000-0000-000000000030")
	importedAt := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	response := rssEntryResponse(domain.RSSEntryView{
		ID: uuid.New(), SubscriptionID: uuid.New(), AcquisitionID: &acquisitionID, ImportedAt: &importedAt,
		AcquisitionProgress: &domain.AcquisitionProgressView{
			AggregateStatus: "processing",
			CurrentStage:    "transcode",
			OverallProgress: 0.473,
		},
		Title: "Progress E02", Status: "enqueued", Classification: "enqueued",
		RejectReason: "title_excluded,target_episode_processing",
	})

	if response.AcquisitionId == nil || *response.AcquisitionId != acquisitionID || response.AcquisitionProgress == nil || response.ImportedAt == nil || !response.ImportedAt.Equal(importedAt) {
		t.Fatalf("RSS acquisition progress = %#v", response)
	}
	if response.RejectReason == nil || *response.RejectReason != "title_excluded,target_episode_processing" {
		t.Fatalf("RSS rejection reasons = %#v, want the complete reason list", response.RejectReason)
	}
	if response.AcquisitionProgress.AggregateStatus != AcquisitionAggregateStatusProcessing ||
		response.AcquisitionProgress.CurrentStage != AcquisitionStageKeyTranscode ||
		response.AcquisitionProgress.OverallProgress != 0.473 {
		t.Fatalf("RSS acquisition progress = %#v", response.AcquisitionProgress)
	}
}

func TestAcquisitionResponseIncludesArchivedLifecycleMetadata(t *testing.T) {
	archivedAt := time.Date(2026, 7, 29, 6, 10, 0, 0, time.UTC)
	response := acquisitionResponse(domain.AcquisitionView{
		ID: uuid.New(), Archived: true, ArchivedAt: &archivedAt, MediaType: domain.TaskMediaEpisode,
		SeriesID: uuid.New(), SourceKind: "rss", AggregateStatus: "completed", CurrentStage: "import", OverallProgress: 1,
		Stages: []domain.AcquisitionStageView{}, Tasks: []domain.AcquisitionTaskSummary{}, CreatedAt: archivedAt.Add(-time.Hour), UpdatedAt: archivedAt,
	})
	if response.Archived == nil || !*response.Archived || response.ArchivedAt == nil || !response.ArchivedAt.Equal(archivedAt) {
		t.Fatalf("archived acquisition response = %#v", response)
	}
}

func TestGetDownloadNotFound(t *testing.T) {
	stub := &readModelStub{err: domain.ErrNotFound}
	handler := NewHandler(NewServer(readinessStub{}, WithReadModels(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/"+uuid.NewString(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestAcquisitionResponseIncludesDownloadFailureDetails(t *testing.T) {
	now := time.Date(2026, time.July, 25, 9, 45, 0, 0, time.UTC)
	response := acquisitionResponse(domain.AcquisitionView{
		ID: uuid.New(), MediaType: domain.TaskMediaEpisode, SeriesID: uuid.New(), SourceKind: "rss",
		AggregateStatus: "failed", CurrentStage: "download", OverallProgress: 0.15,
		Stages: []domain.AcquisitionStageView{
			{Key: "source", Status: "completed", Progress: 1, CompletedItems: 1, TotalItems: 1, UpdatedAt: &now},
			{Key: "download", Status: "failed", Progress: 0.35, TotalItems: 1, UpdatedAt: &now},
		},
		Tasks: []domain.AcquisitionTaskSummary{},
		Download: &domain.AcquisitionDownloadSummary{
			ID: uuid.New(), Attempt: 2, Status: "failed", FailureStage: "sync",
			ErrorCode: "download_storage_unavailable", ErrorMessage: "no space left on device", UpdatedAt: now,
		},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	})

	if response.Download == nil {
		t.Fatal("download summary = nil")
	}
	if response.Download.Attempt != 2 || response.Download.ErrorCode == nil || *response.Download.ErrorCode != "download_storage_unavailable" || response.Download.ErrorMessage == nil {
		t.Fatalf("download summary = %#v", response.Download)
	}
	if response.CurrentStage != AcquisitionStageKeyDownload || response.OverallProgress != 0.15 || len(response.Stages) != 2 || response.Stages[1].Status != AcquisitionStageStatusFailed {
		t.Fatalf("pipeline summary = current %q progress %f stages %#v", response.CurrentStage, response.OverallProgress, response.Stages)
	}
}

func TestAcquisitionResponseMapsFileResolutionFailureStage(t *testing.T) {
	now := time.Date(2026, time.July, 25, 9, 45, 0, 0, time.UTC)
	response := acquisitionResponse(domain.AcquisitionView{
		ID: uuid.New(), MediaType: domain.TaskMediaEpisode, SeriesID: uuid.New(), SourceKind: "rss",
		AggregateStatus: "failed", CurrentStage: "download", OverallProgress: 0.15,
		Stages: []domain.AcquisitionStageView{},
		Tasks:  []domain.AcquisitionTaskSummary{},
		Download: &domain.AcquisitionDownloadSummary{
			ID: uuid.New(), Attempt: 1, Status: "failed", FailureStage: "file_resolution",
			ErrorCode: "download_no_main_video", ErrorMessage: "the torrent contains no selectable main video", UpdatedAt: now,
		},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	})
	if response.Download == nil || response.Download.FailureStage == nil {
		t.Fatalf("download failureStage = %#v, want file_resolution", response.Download)
	}
	if string(*response.Download.FailureStage) != "file_resolution" {
		t.Fatalf("download failureStage = %q, want %q", string(*response.Download.FailureStage), "file_resolution")
	}
	if *response.Download.FailureStage != AcquisitionDownloadSummaryFailureStageFileResolution {
		t.Fatalf("download failureStage enum = %#v, want file_resolution", *response.Download.FailureStage)
	}
	if !response.Download.FailureStage.Valid() {
		t.Fatalf("file_resolution should be valid, got Valid()=false")
	}
	// 完整 Download 模型也必须接受同一持久值，保持双模型契约一致
	downloadView := downloadResponse(domain.DownloadView{
		ID: uuid.New(), AcquisitionID: uuid.New(), Attempt: 1, ClientName: "qbittorrent",
		Status: "failed", Progress: 1, Version: 1, FailureStage: "file_resolution",
		ErrorCode: "download_no_main_video", CreatedAt: now, UpdatedAt: now,
	})
	if downloadView.FailureStage == nil || string(*downloadView.FailureStage) != "file_resolution" || !downloadView.FailureStage.Valid() {
		t.Fatalf("download view failureStage = %#v", downloadView.FailureStage)
	}
	// 通过 HTTP 序列化验证契约响应合法且包含正确枚举值
	stub := &readModelStub{acquisition: domain.AcquisitionView{
		ID: uuid.New(), MediaType: domain.TaskMediaEpisode, SeriesID: uuid.New(), SourceKind: "rss",
		AggregateStatus: "failed", CurrentStage: "download", OverallProgress: 0.15,
		Stages: []domain.AcquisitionStageView{},
		Tasks:  []domain.AcquisitionTaskSummary{},
		Download: &domain.AcquisitionDownloadSummary{
			ID: uuid.New(), Attempt: 1, Status: "failed", FailureStage: "file_resolution",
			ErrorCode: "download_no_main_video", UpdatedAt: now,
		},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}}
	handler := NewHandler(NewServer(readinessStub{}, WithReadModels(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/acquisitions/"+stub.acquisition.ID.String(), nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body Acquisition
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Download == nil || body.Download.FailureStage == nil || string(*body.Download.FailureStage) != "file_resolution" || !body.Download.FailureStage.Valid() {
		t.Fatalf("HTTP acquisition download failureStage = %#v", body.Download)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"file_resolution"`) {
		t.Fatalf("JSON should contain file_resolution, got %s", string(encoded))
	}
}

func TestGetMovieAcquisitionMapsMovieMetadataAndNoEpisodeFields(t *testing.T) {
	acquisitionID := uuid.MustParse("88000000-0000-0000-0000-000000000020")
	now := time.Date(2026, time.July, 24, 2, 15, 0, 0, time.UTC)
	stub := &readModelStub{acquisition: domain.AcquisitionView{
		ID: acquisitionID, MediaType: domain.TaskMediaMovie, SeriesID: uuid.New(),
		TMDbMovieID: 12345, MovieTitle: "Fixture Movie", ReleaseYear: 2024,
		SourceKind: "search", AggregateStatus: "processing", CurrentStage: "transcode", OverallProgress: 0.4,
		Stages: []domain.AcquisitionStageView{}, Tasks: []domain.AcquisitionTaskSummary{{
			ID: uuid.New(), MediaType: domain.TaskMediaMovie, DownloadID: uuid.New(), State: string(domain.TaskProcessing),
			VideoState: string(domain.VideoTranscoding), SubtitleState: string(domain.SubtitleExtractingConverting), UpdatedAt: now,
		}}, CreatedAt: now, UpdatedAt: now,
	}}
	handler := NewHandler(NewServer(readinessStub{}, WithReadModels(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/acquisitions/"+acquisitionID.String(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body Acquisition
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.MediaType != TaskMediaTypeMovie || body.MovieTitle == nil || *body.MovieTitle != "Fixture Movie" || body.TmdbMovieId == nil || *body.TmdbMovieId != 12345 || body.ReleaseYear == nil || *body.ReleaseYear != 2024 || len(body.Tasks) != 1 || body.Tasks[0].MediaType != TaskMediaTypeMovie {
		t.Fatalf("movie acquisition response = %#v", body)
	}
	if body.SeriesTitle != nil || body.TmdbSeriesId != nil || body.SourceSeason != nil || body.SourceEpisode != nil || body.Tasks[0].SourceSeason != nil || body.Tasks[0].TargetSeason != nil {
		t.Fatalf("movie acquisition exposed episode metadata = %#v", body)
	}
}

func TestGetOperationIncludesAttempts(t *testing.T) {
	operationID := uuid.MustParse("88000000-0000-0000-0000-000000000010")
	now := time.Date(2026, time.July, 22, 2, 0, 0, 0, time.UTC)
	stub := &readModelStub{operation: domain.OperationView{
		ID: operationID, Kind: "transcode.run", Status: "failed", IdempotencyKey: "k", MaxAttempts: 3, AttemptCount: 2,
		CreatedAt: now, UpdatedAt: now,
		Attempts: []domain.OperationAttemptView{{ID: uuid.New(), Attempt: 1, Status: "failed", ErrorMessage: "boom", StartedAt: now}},
	}}
	handler := NewHandler(NewServer(readinessStub{}, WithReadModels(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/operations/"+operationID.String(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body Operation
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Id != operationID || len(body.Attempts) != 1 || body.Attempts[0].ErrorMessage == nil {
		t.Fatalf("response = %#v", body)
	}
}

func TestDashboardSummaryMapsCounts(t *testing.T) {
	testedAt := time.Date(2026, time.July, 24, 9, 0, 0, 0, time.UTC)
	acquisitionID := uuid.New()
	stub := &readModelStub{summary: domain.DashboardSummary{
		Counts: domain.DashboardStatusCounts{Downloading: 2, AwaitingReview: 3, Attention: 1, Failed: 1},
		AttentionItems: []domain.DashboardAttentionItem{{
			Acquisition: domain.AcquisitionView{
				ID: acquisitionID, MediaType: domain.TaskMediaEpisode, SeriesID: uuid.New(), SeriesTitle: "待映射作品",
				SourceKind: "rss", AggregateStatus: "mapping_pending", CurrentStage: "mapping", Stages: []domain.AcquisitionStageView{}, Tasks: []domain.AcquisitionTaskSummary{},
				Mapping: domain.AcquisitionMappingCompleteness{SelectedVideoCount: 1}, CreatedAt: testedAt, UpdatedAt: testedAt,
			},
			Reason: "mapping_required",
		}},
		Dependencies: domain.DashboardDependencies{NetworkProxy: domain.DashboardDependencyStatus{
			HasTest:  true,
			Success:  true,
			Code:     "ok",
			Message:  "connection succeeded",
			TestedAt: testedAt,
		}},
	}}
	handler := NewHandler(NewServer(readinessStub{}, WithReadModels(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/summary", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body DashboardSummary
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Counts.Downloading != 2 || body.Counts.AwaitingReview != 3 || body.Counts.Attention != 1 || body.Counts.Failed != 1 {
		t.Fatalf("response = %#v", body)
	}
	if len(body.AttentionItems) != 1 || body.AttentionItems[0].Acquisition.Id != acquisitionID || body.AttentionItems[0].Reason != DashboardAttentionItemReasonMappingRequired {
		t.Fatalf("attention items = %#v", body.AttentionItems)
	}
	if body.Dependencies.NetworkProxy.LastTestSuccess == nil || !*body.Dependencies.NetworkProxy.LastTestSuccess || body.Dependencies.NetworkProxy.LastTestedAt == nil || !body.Dependencies.NetworkProxy.LastTestedAt.Equal(testedAt) {
		t.Fatalf("network proxy dependency = %#v", body.Dependencies.NetworkProxy)
	}
}

var _ = service.ErrInvalidInput
