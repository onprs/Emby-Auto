package worker

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
)

type downloadCancelStoreStub struct {
	command       domain.DownloadSyncCommand
	ready         bool
	loadCalls     int
	completeCalls int
	completedID   uuid.UUID
	completedOpID uuid.UUID
}

func (stub *downloadCancelStoreStub) LoadSyncCommand(context.Context, uuid.UUID) (domain.DownloadSyncCommand, error) {
	stub.loadCalls++
	return stub.command, nil
}

func (stub *downloadCancelStoreStub) DownloadCancellationReady(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return stub.ready, nil
}

func (stub *downloadCancelStoreStub) CompleteRemoval(_ context.Context, downloadID, operationID uuid.UUID) error {
	stub.completeCalls++
	stub.completedID = downloadID
	stub.completedOpID = operationID
	return nil
}

type downloadCancelTorrentStub struct {
	loginCalls      int
	deleteCalls     int
	hash            string
	deleteFiles     bool
	deletedCategory string
}

func (stub *downloadCancelTorrentStub) Login(context.Context) error {
	stub.loginCalls++
	return nil
}

func (stub *downloadCancelTorrentStub) DeleteTorrent(_ context.Context, hash string, deleteFiles bool) error {
	stub.deleteCalls++
	stub.hash = hash
	stub.deleteFiles = deleteFiles
	return nil
}

func (stub *downloadCancelTorrentStub) DeleteCategory(_ context.Context, category string) error {
	stub.deletedCategory = category
	return nil
}

func TestDownloadCancelHandlerRemovalDeletesTorrentFilesAndCompletesRemoval(t *testing.T) {
	downloadID := uuid.MustParse("31000000-0000-0000-0000-000000000001")
	operationID := uuid.MustParse("31000000-0000-0000-0000-000000000002")
	store := &downloadCancelStoreStub{
		ready:   true,
		command: domain.DownloadSyncCommand{DownloadID: downloadID, TorrentHash: workerTorrentHash},
	}
	client := &downloadCancelTorrentStub{}
	configuration := &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080", Username: "admin"},
		}},
		password: "secret",
	}
	handler := NewDownloadCancelHandler(configuration, store, func(qbittorrent.ClientOptions) (DownloadCancelTorrentClient, error) {
		return client, nil
	})

	err := handler.Handle(context.Background(), domain.Operation{
		ID: operationID, ResourceType: "download", ResourceID: downloadID,
		Payload: []byte(`{"command":"remove","deleteFiles":true}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if client.loginCalls != 1 || client.deleteCalls != 1 || client.hash != workerTorrentHash || !client.deleteFiles {
		t.Fatalf("torrent calls = %#v", client)
	}
	if store.completeCalls != 1 || store.completedID != downloadID || store.completedOpID != operationID {
		t.Fatalf("completion = %#v", store)
	}
}

func TestDownloadCancelHandlerCancellationKeepsDownloadedFiles(t *testing.T) {
	downloadID := uuid.MustParse("31000000-0000-0000-0000-000000000003")
	store := &downloadCancelStoreStub{
		ready:   true,
		command: domain.DownloadSyncCommand{DownloadID: downloadID, TorrentHash: workerTorrentHash},
	}
	client := &downloadCancelTorrentStub{}
	configuration := &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080", Username: "admin"},
		}},
	}
	handler := NewDownloadCancelHandler(configuration, store, func(qbittorrent.ClientOptions) (DownloadCancelTorrentClient, error) {
		return client, nil
	})

	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "download", ResourceID: downloadID,
		Payload: []byte(`{"command":"cancel"}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if client.deleteCalls != 1 || client.deleteFiles {
		t.Fatalf("DeleteTorrent calls = %#v", client)
	}
	if store.completeCalls != 0 {
		t.Fatalf("ordinary cancellation completed removal %d times", store.completeCalls)
	}
}

func TestDownloadCancelHandlerPreservesTorrentOwnedByAnotherDownload(t *testing.T) {
	downloadID := uuid.MustParse("31000000-0000-0000-0000-000000000006")
	operationID := uuid.MustParse("31000000-0000-0000-0000-000000000007")
	store := &downloadCancelStoreStub{
		ready:   true,
		command: domain.DownloadSyncCommand{DownloadID: downloadID, TorrentHash: workerTorrentHash},
	}
	factoryCalled := false
	handler := NewDownloadCancelHandler(&downloadConfigurationStub{}, store, func(qbittorrent.ClientOptions) (DownloadCancelTorrentClient, error) {
		factoryCalled = true
		return &downloadCancelTorrentStub{}, nil
	})

	err := handler.Handle(context.Background(), domain.Operation{
		ID: operationID, ResourceType: "download", ResourceID: downloadID,
		Payload: []byte(`{"command":"remove","deleteFiles":false,"preserveTorrent":true}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if factoryCalled || store.loadCalls != 0 || store.completeCalls != 1 {
		t.Fatalf("factoryCalled=%t loadCalls=%d completeCalls=%d", factoryCalled, store.loadCalls, store.completeCalls)
	}
}

func TestDownloadCancelHandlerRemovalWithoutTorrentCompletesImmediately(t *testing.T) {
	downloadID := uuid.MustParse("31000000-0000-0000-0000-000000000004")
	operationID := uuid.MustParse("31000000-0000-0000-0000-000000000005")
	store := &downloadCancelStoreStub{ready: true, command: domain.DownloadSyncCommand{DownloadID: downloadID}}
	factoryCalled := false
	handler := NewDownloadCancelHandler(&downloadConfigurationStub{}, store, func(qbittorrent.ClientOptions) (DownloadCancelTorrentClient, error) {
		factoryCalled = true
		return &downloadCancelTorrentStub{}, nil
	})

	err := handler.Handle(context.Background(), domain.Operation{
		ID: operationID, ResourceType: "download", ResourceID: downloadID,
		Payload: []byte(`{"command":"remove","deleteFiles":true}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if factoryCalled || store.completeCalls != 1 {
		t.Fatalf("factoryCalled=%t completeCalls=%d", factoryCalled, store.completeCalls)
	}
}
