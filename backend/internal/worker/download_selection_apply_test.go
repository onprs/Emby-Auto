package worker

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
)

type selectionApplyStoreStub struct {
	command     domain.DownloadSelectionApplyCommand
	loadErr     error
	completeErr error
	completedID uuid.UUID
	operationID uuid.UUID
}

func (stub *selectionApplyStoreStub) LoadSelectionApplyCommand(context.Context, uuid.UUID) (domain.DownloadSelectionApplyCommand, error) {
	return stub.command, stub.loadErr
}

func (stub *selectionApplyStoreStub) CompleteSelectionApply(_ context.Context, downloadID, operationID uuid.UUID) error {
	stub.completedID, stub.operationID = downloadID, operationID
	return stub.completeErr
}

func TestDownloadSelectionApplyHandlerAppliesQBittorrentStateBeforeDatabaseTransition(t *testing.T) {
	downloadID := uuid.MustParse("31000000-0000-0000-0000-000000000001")
	operationID := uuid.MustParse("31000000-0000-0000-0000-000000000002")
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080", DownloadRateLimitKibPerSecond: 1024, UploadRateLimitKibPerSecond: 256},
	}}}
	store := &selectionApplyStoreStub{command: domain.DownloadSelectionApplyCommand{
		DownloadID: downloadID, Status: domain.DownloadFileResolutionPending, TorrentHash: workerTorrentHash,
		AllFileIndexes: []int{0, 1, 2}, SelectedFileIndexes: []int{0, 1},
	}}
	client := &torrentClientStub{}
	handler := NewDownloadSelectionApplyHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) { return client, nil })

	err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	wantCalls := []string{"login", "priority", "priority", "rate-limits", "ensure-category", "set-category", "delete-category", "resume"}
	if !reflect.DeepEqual(client.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", client.calls, wantCalls)
	}
	if len(client.priorityCalls) != 2 || !reflect.DeepEqual(client.priorityCalls[0], priorityCall{indexes: []int{0, 1, 2}, priority: 0}) || !reflect.DeepEqual(client.priorityCalls[1], priorityCall{indexes: []int{0, 1}, priority: 1}) {
		t.Fatalf("priority calls = %#v", client.priorityCalls)
	}
	if len(client.rateLimitCalls) != 1 || client.rateLimitCalls[0].downloadBytesPerSecond != 1048576 || client.rateLimitCalls[0].uploadBytesPerSecond != 262144 {
		t.Fatalf("rate limit calls = %#v, want 1048576/262144 bytes/s", client.rateLimitCalls)
	}
	if store.completedID != downloadID || store.operationID != operationID {
		t.Fatalf("completion = %s/%s", store.completedID, store.operationID)
	}
}

func TestDownloadSelectionApplyHandlerDoesNotAdvanceDatabaseAfterQBittorrentFailure(t *testing.T) {
	downloadID := uuid.New()
	store := &selectionApplyStoreStub{command: domain.DownloadSelectionApplyCommand{
		DownloadID: downloadID, Status: domain.DownloadFileResolutionPending, TorrentHash: workerTorrentHash,
		AllFileIndexes: []int{0}, SelectedFileIndexes: []int{0},
	}}
	client := &torrentClientStub{rateLimitErr: errors.New("unavailable")}
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"}}}}
	handler := NewDownloadSelectionApplyHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) { return client, nil })

	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "download", ResourceID: downloadID})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "qbittorrent_rate_limit_failed" || !failure.Retryable {
		t.Fatalf("Handle() error = %#v", err)
	}
	if store.completedID != uuid.Nil {
		t.Fatalf("database completion called for %s", store.completedID)
	}
}

func TestDownloadSelectionApplyHandlerTreatsAdvancedStateAsIdempotentReplay(t *testing.T) {
	downloadID := uuid.New()
	store := &selectionApplyStoreStub{command: domain.DownloadSelectionApplyCommand{DownloadID: downloadID, Status: domain.DownloadDownloading, TorrentHash: workerTorrentHash}}
	factoryCalled := false
	handler := NewDownloadSelectionApplyHandler(&downloadConfigurationStub{}, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		factoryCalled = true
		return nil, nil
	})
	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "download", ResourceID: downloadID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if factoryCalled || store.completedID != uuid.Nil {
		t.Fatalf("replay invoked side effects: factory=%t completion=%s", factoryCalled, store.completedID)
	}
}
