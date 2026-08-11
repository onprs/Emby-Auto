package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type downloadCommandStub struct {
	download    domain.DownloadView
	operation   domain.Operation
	err         error
	gotID       uuid.UUID
	gotVersion  int32
	gotKey      string
	gotSelected map[uuid.UUID]bool
	gotResolved []domain.DownloadFileResolutionItem
}

func (stub *downloadCommandStub) Retry(_ context.Context, id uuid.UUID, version int32, key string, _ uuid.UUID) (domain.DownloadView, domain.Operation, error) {
	stub.gotID, stub.gotVersion, stub.gotKey = id, version, key
	return stub.download, stub.operation, stub.err
}
func (stub *downloadCommandStub) Cancel(_ context.Context, id uuid.UUID, version int32, key string, _ uuid.UUID) (domain.DownloadView, domain.Operation, error) {
	stub.gotID, stub.gotVersion, stub.gotKey = id, version, key
	return stub.download, stub.operation, stub.err
}
func (stub *downloadCommandStub) Remove(_ context.Context, id uuid.UUID, version int32, key string, _ uuid.UUID) (domain.DownloadView, domain.Operation, error) {
	stub.gotID, stub.gotVersion, stub.gotKey = id, version, key
	return stub.download, stub.operation, stub.err
}
func (stub *downloadCommandStub) SaveFileResolution(_ context.Context, id uuid.UUID, version int32, items []domain.DownloadFileResolutionItem, key string, _ uuid.UUID) (domain.DownloadView, domain.Operation, error) {
	stub.gotID, stub.gotVersion, stub.gotKey, stub.gotResolved = id, version, key, items
	return stub.download, stub.operation, stub.err
}
func (stub *downloadCommandStub) SaveFileSelection(_ context.Context, id uuid.UUID, version int32, selections map[uuid.UUID]bool, key string, _ uuid.UUID) (domain.DownloadView, domain.Operation, error) {
	stub.gotID, stub.gotVersion, stub.gotKey, stub.gotSelected = id, version, key, selections
	return stub.download, stub.operation, stub.err
}

type acquisitionCommandStub struct {
	operation  domain.Operation
	err        error
	gotID      uuid.UUID
	gotVersion int32
	gotKey     string
	gotActor   uuid.UUID
}

func (stub *acquisitionCommandStub) RequestDeletion(_ context.Context, id uuid.UUID, key string, actor uuid.UUID) (domain.Operation, error) {
	stub.gotID, stub.gotKey, stub.gotActor = id, key, actor
	return stub.operation, stub.err
}
func (stub *acquisitionCommandStub) RequestDownloadDeletion(_ context.Context, id uuid.UUID, version int32, key string, actor uuid.UUID) (domain.Operation, error) {
	stub.gotID, stub.gotVersion, stub.gotKey, stub.gotActor = id, version, key, actor
	return stub.operation, stub.err
}

type taskCommandStub struct {
	task       domain.EpisodeTask
	operation  domain.Operation
	err        error
	gotID      uuid.UUID
	gotVersion int32
	gotKey     string
}

func (stub *taskCommandStub) Retry(_ context.Context, id uuid.UUID, version int32, key string, _ uuid.UUID) (domain.EpisodeTask, domain.Operation, error) {
	stub.gotID, stub.gotVersion, stub.gotKey = id, version, key
	return stub.task, stub.operation, stub.err
}
func (stub *taskCommandStub) Cancel(_ context.Context, id uuid.UUID, version int32, key string, _ uuid.UUID) (domain.EpisodeTask, domain.Operation, error) {
	stub.gotID, stub.gotVersion, stub.gotKey = id, version, key
	return stub.task, stub.operation, stub.err
}

func authenticatedServer() (AuthenticationService, uuid.UUID) {
	userID := uuid.MustParse("99000000-0000-0000-0000-000000000099")
	authentication := &authenticationStub{authenticated: domain.Session{
		User:      domain.AdminUser{ID: userID, Username: "admin"},
		ExpiresAt: time.Now().Add(time.Hour),
	}}
	return authentication, userID
}

func TestDeleteAcquisitionForwardsIdentityAndReturnsOperation(t *testing.T) {
	acquisitionID := uuid.MustParse("99000000-0000-0000-0000-000000000090")
	operationID := uuid.MustParse("99000000-0000-0000-0000-000000000091")
	authentication, actorID := authenticatedServer()
	stub := &acquisitionCommandStub{operation: domain.Operation{ID: operationID, Status: "queued"}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithAcquisitionCommands(stub)))
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/acquisitions/"+acquisitionID.String(), nil)
	request.Header.Set("Idempotency-Key", "delete-acquisition-key")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.gotID != acquisitionID || stub.gotKey != "delete-acquisition-key" || stub.gotActor != actorID {
		t.Fatalf("forwarded = %#v", stub)
	}
	var body CommandAccepted
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.OperationId != operationID || body.Status != "queued" {
		t.Fatalf("response = %#v", body)
	}
}

func TestRetryDownloadForwardsVersionAndIdempotencyKey(t *testing.T) {
	downloadID := uuid.MustParse("99000000-0000-0000-0000-000000000001")
	operationID := uuid.MustParse("99000000-0000-0000-0000-000000000002")
	authentication, _ := authenticatedServer()
	stub := &downloadCommandStub{
		download:  domain.DownloadView{ID: downloadID, AcquisitionID: uuid.New(), Status: "enqueue_pending", Version: 3, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		operation: domain.Operation{ID: operationID, Status: "queued"},
	}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithDownloadCommands(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/downloads/"+downloadID.String()+"/retry", strings.NewReader(`{"expectedVersion":2}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "retry-key-1")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.gotID != downloadID || stub.gotVersion != 2 || stub.gotKey != "retry-key-1" {
		t.Fatalf("forwarded = %#v", stub)
	}
	var body DownloadCommandAccepted
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.OperationId != operationID || body.Download.Version != 3 {
		t.Fatalf("response = %#v", body)
	}
}

func TestDeleteDownloadForwardsVersionAndIdempotencyKey(t *testing.T) {
	downloadID := uuid.MustParse("99000000-0000-0000-0000-000000000010")
	operationID := uuid.MustParse("99000000-0000-0000-0000-000000000011")
	authentication, actorID := authenticatedServer()
	stub := &acquisitionCommandStub{operation: domain.Operation{ID: operationID, Status: "queued"}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithAcquisitionCommands(stub)))
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/downloads/"+downloadID.String()+"?expectedVersion=3", nil)
	request.Header.Set("Idempotency-Key", "delete-key-1")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.gotID != downloadID || stub.gotVersion != 3 || stub.gotKey != "delete-key-1" || stub.gotActor != actorID {
		t.Fatalf("forwarded = %#v", stub)
	}
	var body CommandAccepted
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.OperationId != operationID || body.Status != "queued" {
		t.Fatalf("response = %#v", body)
	}
}

func TestRetryDownloadStateConflictReturns409(t *testing.T) {
	downloadID := uuid.MustParse("99000000-0000-0000-0000-000000000003")
	authentication, _ := authenticatedServer()
	stub := &downloadCommandStub{err: service.NewError("state_conflict", "modified", service.ErrStateConflict, map[string]any{})}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithDownloadCommands(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/downloads/"+downloadID.String()+"/retry", strings.NewReader(`{"expectedVersion":1}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "k")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestSaveFileSelectionBuildsSelectionMap(t *testing.T) {
	downloadID := uuid.MustParse("99000000-0000-0000-0000-000000000004")
	fileID := uuid.MustParse("99000000-0000-0000-0000-000000000005")
	authentication, _ := authenticatedServer()
	stub := &downloadCommandStub{
		download:  domain.DownloadView{ID: downloadID, AcquisitionID: uuid.New(), Status: "selecting_files", Version: 4, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		operation: domain.Operation{ID: uuid.New(), Status: "queued"},
	}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithDownloadCommands(stub)))
	request := httptest.NewRequest(http.MethodPut, "/api/v1/downloads/"+downloadID.String()+"/file-selection", strings.NewReader(`{"expectedVersion":3,"files":[{"fileId":"`+fileID.String()+`","selected":true}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "sel-key")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.gotVersion != 3 || stub.gotKey != "sel-key" || stub.gotSelected[fileID] != true {
		t.Fatalf("selection = %#v", stub.gotSelected)
	}
}

func TestSaveFileResolutionForwardsCompleteCoordinates(t *testing.T) {
	downloadID := uuid.MustParse("99000000-0000-0000-0000-000000000014")
	videoID := uuid.MustParse("99000000-0000-0000-0000-000000000015")
	subtitleID := uuid.MustParse("99000000-0000-0000-0000-000000000016")
	authentication, _ := authenticatedServer()
	stub := &downloadCommandStub{
		download: domain.DownloadView{
			ID: downloadID, AcquisitionID: uuid.New(), Status: "file_resolution_pending", Version: 5,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
		operation: domain.Operation{ID: uuid.New(), Status: "queued"},
	}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithDownloadCommands(stub)))
	request := httptest.NewRequest(http.MethodPut, "/api/v1/downloads/"+downloadID.String()+"/file-resolution", strings.NewReader(
		`{"expectedVersion":4,"files":[{"fileId":"`+videoID.String()+`","selected":true,"sourceSeason":2,"sourceEpisode":3},{"fileId":"`+subtitleID.String()+`","selected":false}]}`,
	))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "resolve-key")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.gotID != downloadID || stub.gotVersion != 4 || stub.gotKey != "resolve-key" || len(stub.gotResolved) != 2 {
		t.Fatalf("forwarded = %#v", stub)
	}
	video := stub.gotResolved[0]
	if video.FileID != videoID || !video.Selected || video.SourceSeason == nil || *video.SourceSeason != 2 || video.SourceEpisode == nil || *video.SourceEpisode != 3 {
		t.Fatalf("video resolution = %#v", video)
	}
	if stub.gotResolved[1].FileID != subtitleID || stub.gotResolved[1].Selected {
		t.Fatalf("subtitle resolution = %#v", stub.gotResolved[1])
	}
}

func TestCancelDownloadForwardsVersionAndIdempotencyKey(t *testing.T) {
	downloadID := uuid.MustParse("99000000-0000-0000-0000-000000000017")
	authentication, _ := authenticatedServer()
	stub := &downloadCommandStub{
		download:  domain.DownloadView{ID: downloadID, AcquisitionID: uuid.New(), Status: "cancelled", Version: 6, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		operation: domain.Operation{ID: uuid.New(), Status: "queued"},
	}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithDownloadCommands(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/downloads/"+downloadID.String()+"/cancel", strings.NewReader(`{"expectedVersion":5}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "cancel-download-key")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.gotID != downloadID || stub.gotVersion != 5 || stub.gotKey != "cancel-download-key" {
		t.Fatalf("forwarded = %#v", stub)
	}
}

func TestTaskCommandsForwardVersionAndIdempotencyKey(t *testing.T) {
	authentication, _ := authenticatedServer()
	for _, test := range []struct {
		name string
		path string
		key  string
	}{
		{name: "retry", path: "retry", key: "retry-task-key"},
		{name: "cancel", path: "cancel", key: "cancel-task-key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			taskID := uuid.New()
			stub := &taskCommandStub{
				task:      domain.EpisodeTask{ID: taskID, AcquisitionID: uuid.New(), DownloadID: uuid.New(), State: domain.TaskProcessing, Version: 8, CreatedAt: time.Now(), UpdatedAt: time.Now()},
				operation: domain.Operation{ID: uuid.New(), Status: "queued"},
			}
			handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithTaskCommands(stub)))
			request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+taskID.String()+"/"+test.path, strings.NewReader(`{"expectedVersion":7}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", test.key)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusAccepted {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if stub.gotID != taskID || stub.gotVersion != 7 || stub.gotKey != test.key {
				t.Fatalf("forwarded = %#v", stub)
			}
		})
	}
}

func TestCancelTaskRequiresAuthentication(t *testing.T) {
	taskID := uuid.MustParse("99000000-0000-0000-0000-000000000006")
	handler := NewHandler(NewServer(readinessStub{}, WithTaskCommands(&taskCommandStub{})))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+taskID.String()+"/cancel", strings.NewReader(`{"expectedVersion":1}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "k")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestRetryTaskNotFoundReturns404(t *testing.T) {
	taskID := uuid.MustParse("99000000-0000-0000-0000-000000000007")
	authentication, _ := authenticatedServer()
	stub := &taskCommandStub{err: domain.ErrNotFound}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithTaskCommands(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+taskID.String()+"/retry", strings.NewReader(`{"expectedVersion":1}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "k")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

var _ = errors.New
