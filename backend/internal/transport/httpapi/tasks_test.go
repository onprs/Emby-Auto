package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type taskServiceStub struct {
	page        domain.EpisodeTaskPage
	listState   *domain.TaskState
	reviewInput domain.ReviewTask
	reviewed    domain.EpisodeTask
	importInput domain.QueueTaskImport
	imported    domain.TaskImportResult
}

func (stub *taskServiceStub) ListTasks(_ context.Context, _ *uuid.UUID, _ int, state *domain.TaskState, _ *string) (domain.EpisodeTaskPage, error) {
	stub.listState = state
	return stub.page, nil
}
func (stub *taskServiceStub) GetTask(context.Context, uuid.UUID) (domain.EpisodeTask, error) {
	return stub.reviewed, nil
}
func (stub *taskServiceStub) ReviewTask(_ context.Context, input domain.ReviewTask) (domain.EpisodeTask, error) {
	stub.reviewInput = input
	return stub.reviewed, nil
}
func (stub *taskServiceStub) QueueImport(_ context.Context, input domain.QueueTaskImport) (domain.TaskImportResult, error) {
	stub.importInput = input
	return stub.imported, nil
}

func TestReviewTaskForwardsAuthenticatedActorVersionAndIdempotencyKey(t *testing.T) {
	userID := uuid.MustParse("79000000-0000-0000-0000-000000000001")
	taskID := uuid.MustParse("79000000-0000-0000-0000-000000000002")
	now := time.Date(2026, time.July, 21, 20, 0, 0, 0, time.UTC)
	stub := &taskServiceStub{reviewed: taskResponseFixture(taskID, domain.TaskApproved, 8, now)}
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: userID, Username: "admin"}, ExpiresAt: now.Add(time.Hour)}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithTasks(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+taskID.String()+"/review", strings.NewReader(`{"expectedVersion":7,"decision":"approved","notes":"checked files"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "review-browser-retry-1")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.reviewInput.TaskID != taskID || stub.reviewInput.ActorUserID != userID || stub.reviewInput.ExpectedVersion != 7 || stub.reviewInput.Decision != domain.TaskApproved || stub.reviewInput.IdempotencyKey != "review-browser-retry-1" {
		t.Fatalf("review input = %#v", stub.reviewInput)
	}
	var body Task
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Id != taskID || body.State != TaskStateApproved || body.Version != 8 {
		t.Fatalf("response = %#v", body)
	}
}

func TestImportTaskReturnsGeneratedAcceptedResource(t *testing.T) {
	userID := uuid.MustParse("79000000-0000-0000-0000-000000000003")
	taskID := uuid.MustParse("79000000-0000-0000-0000-000000000004")
	operationID := uuid.MustParse("79000000-0000-0000-0000-000000000005")
	now := time.Date(2026, time.July, 21, 20, 30, 0, 0, time.UTC)
	task := taskResponseFixture(taskID, domain.TaskImportQueued, 9, now)
	stub := &taskServiceStub{imported: domain.TaskImportResult{Task: task, Operation: domain.Operation{ID: operationID, Status: "queued"}}}
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: userID, Username: "admin"}, ExpiresAt: now.Add(time.Hour)}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithTasks(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+taskID.String()+"/import", strings.NewReader(`{"expectedVersion":8}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "import-click-1")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.importInput.TaskID != taskID || stub.importInput.ActorUserID != userID || stub.importInput.ExpectedVersion != 8 || stub.importInput.IdempotencyKey != "import-click-1" {
		t.Fatalf("import input = %#v", stub.importInput)
	}
	var body TaskCommandAccepted
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.OperationId != operationID || body.Status != TaskCommandAcceptedStatusQueued || body.Task.State != TaskStateImportQueued {
		t.Fatalf("response = %#v", body)
	}
}

func TestListTasksForwardsStateFilter(t *testing.T) {
	now := time.Now().UTC()
	taskID := uuid.MustParse("79000000-0000-0000-0000-000000000006")
	stub := &taskServiceStub{page: domain.EpisodeTaskPage{Items: []domain.EpisodeTask{taskResponseFixture(taskID, domain.TaskAwaitingReview, 3, now)}}}
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: uuid.New(), Username: "admin"}, ExpiresAt: now.Add(time.Hour)}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithTasks(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks?state=awaiting_review", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.listState == nil || *stub.listState != domain.TaskAwaitingReview {
		t.Fatalf("state filter = %#v", stub.listState)
	}
}

func TestTaskResponseMapsMovieMetadataWithoutEpisodeCoordinates(t *testing.T) {
	now := time.Date(2026, time.July, 24, 2, 0, 0, 0, time.UTC)
	taskID := uuid.MustParse("79000000-0000-0000-0000-000000000016")
	stub := &taskServiceStub{reviewed: domain.EpisodeTask{
		ID: taskID, AcquisitionID: uuid.New(), DownloadID: uuid.New(), MediaType: domain.TaskMediaMovie,
		MovieTitle: "Fixture Movie", ReleaseYear: 2024, State: domain.TaskAwaitingReview,
		VideoState: domain.VideoReady, SubtitleState: domain.SubtitleASSReady, Version: 3,
		Operations: []domain.OperationSummary{}, Actions: domain.TaskActions{}, CreatedAt: now, UpdatedAt: now,
	}}
	handler := NewHandler(NewServer(readinessStub{}, WithTasks(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+taskID.String(), nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body Task
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.MediaType != TaskMediaTypeMovie || body.MovieTitle == nil || *body.MovieTitle != "Fixture Movie" || body.ReleaseYear == nil || *body.ReleaseYear != 2024 {
		t.Fatalf("movie task response = %#v", body)
	}
	if body.SeriesTitle != nil || body.SourceSeason != nil || body.SourceEpisode != nil || body.TargetSeason != nil || body.TargetEpisode != nil || body.TargetEpisodeTitle != nil {
		t.Fatalf("movie task exposed episode metadata = %#v", body)
	}
}

func TestTaskResponseIncludesOperationAttemptSummary(t *testing.T) {
	now := time.Date(2026, time.July, 25, 9, 30, 0, 0, time.UTC)
	startedAt := now.Add(-2 * time.Minute)
	finishedAt := now.Add(-time.Minute)
	response := taskResponse(domain.EpisodeTask{
		ID: uuid.New(), AcquisitionID: uuid.New(), DownloadID: uuid.New(), MediaType: domain.TaskMediaEpisode,
		State: domain.TaskFailed, VideoState: domain.VideoFailed, SubtitleState: domain.SubtitleASSReady, Version: 2,
		Operations: []domain.OperationSummary{{
			ID: uuid.New(), Kind: "transcode.run", Status: "failed", MaxAttempts: 3, AttemptCount: 2,
			ErrorCode: "ffmpeg_transcode_failed", StartedAt: &startedAt, FinishedAt: &finishedAt, UpdatedAt: now,
		}},
		Actions: domain.TaskActions{CanRetry: true}, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	})

	if len(response.Operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(response.Operations))
	}
	operation := response.Operations[0]
	if operation.MaxAttempts != 3 || operation.AttemptCount != 2 || operation.StartedAt == nil || operation.FinishedAt == nil {
		t.Fatalf("operation summary = %#v", operation)
	}
}

func taskResponseFixture(id uuid.UUID, state domain.TaskState, version int32, now time.Time) domain.EpisodeTask {
	return domain.EpisodeTask{
		ID: id, AcquisitionID: uuid.New(), DownloadID: uuid.New(), MediaType: domain.TaskMediaEpisode, SeriesTitle: "Canonical Show", SourceSeason: 1, SourceEpisode: 1,
		TargetSeason: 1, TargetEpisode: 1, TargetEpisodeTitle: "Pilot", State: state,
		VideoState: domain.VideoReady, SubtitleState: domain.SubtitleASSReady, Version: version,
		Operations: []domain.OperationSummary{}, Actions: domain.TaskActions{}, CreatedAt: now, UpdatedAt: now,
	}
}
