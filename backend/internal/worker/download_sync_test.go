package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/riverqueue/river"
)

type downloadSyncStoreStub struct {
	command       domain.DownloadSyncCommand
	loadErr       error
	progress      float64
	clientState   string
	progressCalls int
	progressErr   error
	completed     bool
	completeErr   error
}

func (stub *downloadSyncStoreStub) LoadSyncCommand(context.Context, uuid.UUID) (domain.DownloadSyncCommand, error) {
	return stub.command, stub.loadErr
}

func (stub *downloadSyncStoreStub) RecordProgress(_ context.Context, _ uuid.UUID, _ uuid.UUID, progress float64, clientState string) error {
	stub.progress = progress
	stub.clientState = clientState
	stub.progressCalls++
	return stub.progressErr
}

func (stub *downloadSyncStoreStub) CompleteDownload(context.Context, uuid.UUID, uuid.UUID, string) error {
	stub.completed = true
	return stub.completeErr
}

func (stub *torrentClientStub) ListTorrents(context.Context, string) ([]qbittorrent.Torrent, error) {
	stub.calls = append(stub.calls, "list")
	return stub.torrents, stub.listErr
}

func TestDownloadSyncHandlerRecordsProgressAndSnoozesWithoutRetry(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000001")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000002")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
	}}
	client := &torrentClientStub{torrents: []qbittorrent.Torrent{{
		Hash:       workerTorrentHash,
		Progress:   0.42,
		AmountLeft: 58,
		TotalSize:  100,
		State:      "downloading",
	}}}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, 15*time.Second)

	err := handler.Handle(context.Background(), domain.Operation{
		ID:           operationID,
		ResourceType: "download",
		ResourceID:   downloadID,
	})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) || snoozeErr.Duration != 15*time.Second {
		t.Fatalf("Handle() error = %v, want 15s JobSnoozeError", err)
	}
	if store.progressCalls != 1 || store.progress != 0.42 || store.clientState != "downloading" || store.completed {
		t.Fatalf("store progress/state/completed = %d/%v/%q/%t", store.progressCalls, store.progress, store.clientState, store.completed)
	}
	if len(client.rateLimitCalls) != 1 || client.rateLimitCalls[0].downloadBytesPerSecond != 2*1024*1024 || client.rateLimitCalls[0].uploadBytesPerSecond != 512*1024 {
		t.Fatalf("rate limit calls = %#v, want updated settings", client.rateLimitCalls)
	}
}

func TestDownloadSyncHandlerRejectsUnrepresentableRateLimits(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000009")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID: downloadID, Status: domain.DownloadDownloading, TorrentHash: workerTorrentHash,
	}}
	client := &torrentClientStub{}
	configuration := configuredDownloadTestStub()
	configuration.configuration.Settings.QBittorrent.DownloadRateLimitKibPerSecond = 2097152
	handler := NewDownloadSyncHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, time.Minute)

	err := handler.Handle(context.Background(), domain.Operation{ResourceType: "download", ResourceID: downloadID})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "qbittorrent_configuration_invalid" || failure.Retryable {
		t.Fatalf("Handle() error = %#v, want permanent qbittorrent_configuration_invalid", err)
	}
	if len(client.calls) != 1 || client.calls[0] != "login" || len(client.rateLimitCalls) != 0 || store.progressCalls != 0 {
		t.Fatalf("invalid configuration side effects = calls %v rates %v progress %d", client.calls, client.rateLimitCalls, store.progressCalls)
	}
}

func TestDownloadSyncHandlerRetriesWhenUpdatedRateLimitsAreRejected(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000008")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID: downloadID, Status: domain.DownloadDownloading, TorrentHash: workerTorrentHash,
	}}
	client := &torrentClientStub{
		torrents:     []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 0.5, State: "downloading"}},
		rateLimitErr: errors.New("rate limit rejected"),
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, time.Minute)

	err := handler.Handle(context.Background(), domain.Operation{ResourceType: "download", ResourceID: downloadID})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "qbittorrent_rate_limit_failed" || !failure.Retryable {
		t.Fatalf("Handle() error = %#v, want retryable qbittorrent_rate_limit_failed", err)
	}
	if store.progressCalls != 0 {
		t.Fatalf("progress calls = %d, want no progress persisted before limits are applied", store.progressCalls)
	}
}

func TestDownloadSyncHandlerCompletesFinishedTorrent(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000003")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
	}}
	client := &torrentClientStub{torrents: []qbittorrent.Torrent{{
		Hash:       workerTorrentHash,
		Progress:   1,
		AmountLeft: 0,
		TotalSize:  100,
		State:      "uploading",
	}}}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, time.Minute)

	err := handler.Handle(context.Background(), domain.Operation{
		ID:           uuid.MustParse("40000000-0000-0000-0000-000000000004"),
		ResourceType: "download",
		ResourceID:   downloadID,
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if store.progressCalls != 1 || store.progress != 1 || !store.completed {
		t.Fatalf("store progress/completed = %d/%v/%t", store.progressCalls, store.progress, store.completed)
	}
}

func TestDownloadSyncHandlerTreatsCompletedStateAsIdempotentSuccess(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000005")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadCompleted,
		TorrentHash: workerTorrentHash,
	}}
	factoryCalled := false
	handler := NewDownloadSyncHandler(&downloadConfigurationStub{}, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		factoryCalled = true
		return nil, nil
	}, time.Minute)

	if err := handler.Handle(context.Background(), domain.Operation{ResourceType: "download", ResourceID: downloadID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if factoryCalled || store.progressCalls != 0 || store.completed {
		t.Fatalf("idempotent replay used dependencies: factory=%t progress=%d completed=%t", factoryCalled, store.progressCalls, store.completed)
	}
}

func TestDownloadSyncHandlerRetriesWhenTorrentIsTemporarilyMissing(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000006")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
	}}
	client := &torrentClientStub{torrents: []qbittorrent.Torrent{}}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, time.Minute)

	err := handler.Handle(context.Background(), domain.Operation{ResourceType: "download", ResourceID: downloadID})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "qbittorrent_torrent_not_found" || !failure.Retryable {
		t.Fatalf("Handle() error = %#v, want retryable qbittorrent_torrent_not_found", err)
	}
}

func configuredDownloadTestStub() *downloadConfigurationStub {
	return &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{
				URL: "http://qb:8080", Username: "admin",
				DownloadRateLimitKibPerSecond: 2048, UploadRateLimitKibPerSecond: 512,
			},
			Paths: domain.PathSettings{DownloadRoot: "/downloads"},
		}},
		password: "secret",
	}
}
