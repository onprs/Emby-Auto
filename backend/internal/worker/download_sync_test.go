package worker

import (
	"context"
	"errors"
	"math"
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
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 0, SizeBytes: 100},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{
			Hash:       workerTorrentHash,
			Progress:   0.42,
			AmountLeft: 58,
			TotalSize:  100,
			State:      "downloading",
		}},
		files: []qbittorrent.TorrentFile{
			{Index: 0, Progress: 0.42, Priority: 1, Size: 100},
		},
	}
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
		SelectedFiles: []domain.DownloadSyncFile{{FileIndex: 0, SizeBytes: 100}},
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
		SelectedFiles: []domain.DownloadSyncFile{{FileIndex: 0, SizeBytes: 100}},
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
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 0, SizeBytes: 100},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{
			Hash:       workerTorrentHash,
			Progress:   1,
			AmountLeft: 0,
			TotalSize:  100,
			State:      "uploading",
		}},
		files: []qbittorrent.TorrentFile{
			{Index: 0, Progress: 1, Priority: 1, Size: 100},
		},
	}
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
		SelectedFiles: []domain.DownloadSyncFile{{FileIndex: 0, SizeBytes: 100}},
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

func TestDownloadSyncHandlerFailsWhenSelectedFilesEmpty(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000030")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000031")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 1, AmountLeft: 0, TotalSize: 100, State: "stoppedUP"}},
	}
	factoryCalled := false
	factory := func(qbittorrent.ClientOptions) (TorrentClient, error) {
		factoryCalled = true
		return client, nil
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, factory, time.Minute)
	err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "download_state_conflict" || failure.Retryable {
		t.Fatalf("Handle() error = %#v, want permanent download_state_conflict for empty selected", err)
	}
	if store.completed {
		t.Fatalf("empty selected must not complete")
	}
	if store.progressCalls != 0 {
		t.Fatalf("empty selected must not persist progress, got %d calls", store.progressCalls)
	}
	if factoryCalled {
		t.Fatalf("empty selected must not create qB client")
	}
	if len(client.calls) != 0 {
		t.Fatalf("empty selected must not call qB login/list, got %v", client.calls)
	}
	if len(client.rateLimitCalls) != 0 {
		t.Fatalf("empty selected must not call qB rate-limit, got %v", client.rateLimitCalls)
	}
	for _, call := range client.calls {
		if call == "files" {
			t.Fatalf("empty selected must not call qB files")
		}
	}
}

func TestDownloadSyncHandlerSnoozesWhenTorrentCompleteButSelectedPriorityZero(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000010")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000011")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 0, SizeBytes: 1000},
			{FileIndex: 1, SizeBytes: 2000},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 1, AmountLeft: 0, TotalSize: 3000, State: "pausedUP"}},
		files: []qbittorrent.TorrentFile{
			{Index: 0, Progress: 1, Priority: 1, Size: 1000},
			{Index: 1, Progress: 0.2, Priority: 0, Size: 2000},
		},
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, 15*time.Second)
	err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) || snoozeErr.Duration != 15*time.Second {
		t.Fatalf("Handle() error = %v, want 15s JobSnoozeError", err)
	}
	if store.completed {
		t.Fatalf("store completed = true, want false")
	}
	if store.progressCalls != 1 {
		t.Fatalf("progress calls = %d, want 1", store.progressCalls)
	}
	// priority=0 文件按 0 进度处理，避免瞬态聚合污染
	wantProgress := float64(1000) / float64(3000)
	if store.progress < wantProgress-1e-9 || store.progress > wantProgress+1e-9 {
		t.Fatalf("progress = %v, want %v (priority 0 contributes 0)", store.progress, wantProgress)
	}
	if store.progress >= 1-1e-9 {
		t.Fatalf("progress polluted to 1, got %v", store.progress)
	}
}

func TestDownloadSyncHandlerSnoozesWhenSingleSelectedPriorityZeroWithProgressOne(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000040")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000041")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 7, SizeBytes: 2000},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 1, AmountLeft: 0, TotalSize: 2000, State: "stoppedUP"}},
		files: []qbittorrent.TorrentFile{
			{Index: 7, Progress: 1, Priority: 0, Size: 2000},
		},
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, 15*time.Second)
	err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) {
		t.Fatalf("Handle() error = %v, want snooze for priority 0 single file", err)
	}
	if store.completed {
		t.Fatalf("single priority 0 file must not complete even though torrent progress is 1")
	}
	if store.progressCalls != 1 {
		t.Fatalf("progress calls = %d, want 1", store.progressCalls)
	}
	if store.progress != 0 {
		t.Fatalf("progress = %v, want 0 for priority 0 single file with progress 1", store.progress)
	}
}

func TestDownloadSyncHandlerSnoozesWhenSingleSelectedPriorityZeroIsSeed(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000042")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000043")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 9, SizeBytes: 1500},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 1, AmountLeft: 0, TotalSize: 1500, State: "stoppedUP"}},
		files: []qbittorrent.TorrentFile{
			{Index: 9, Progress: 1, Priority: 0, Size: 1500, IsSeed: true},
		},
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, 15*time.Second)
	err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) {
		t.Fatalf("Handle() error = %v, want snooze for priority 0 IsSeed file", err)
	}
	if store.completed {
		t.Fatalf("priority 0 IsSeed file must not complete")
	}
	if store.progress != 0 {
		t.Fatalf("progress = %v, want 0 for priority 0 IsSeed file", store.progress)
	}
}

func TestDownloadSyncHandlerSnoozesWhenOneSelectedIncompleteWeightedProgress(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000012")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000013")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 2, SizeBytes: 1000},
			{FileIndex: 3, SizeBytes: 1000},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 1, AmountLeft: 0, TotalSize: 2000, State: "uploading"}},
		files: []qbittorrent.TorrentFile{
			{Index: 2, Progress: 1, Priority: 1, Size: 1000},
			{Index: 3, Progress: 0.5, Priority: 1, Size: 1000},
		},
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, 15*time.Second)
	err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) {
		t.Fatalf("Handle() error = %v, want snooze", err)
	}
	if store.completed {
		t.Fatalf("should not complete when one selected is at 0.5")
	}
	want := 0.75
	if store.progress < want-1e-9 || store.progress > want+1e-9 {
		t.Fatalf("progress = %v, want %v", store.progress, want)
	}
}

func TestDownloadSyncHandlerCompletesWhenAllSelectedFilesDone(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000014")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000015")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 4, SizeBytes: 1500},
			{FileIndex: 5, SizeBytes: 500},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 0.6, State: "downloading"}},
		files: []qbittorrent.TorrentFile{
			{Index: 4, Progress: 1, Priority: 1, Size: 1500},
			{Index: 5, Progress: 1, Priority: 6, Size: 500},
		},
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, time.Minute)
	if err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !store.completed || store.progressCalls != 1 || store.progress != 1 {
		t.Fatalf("store progress/completed = %d/%v/%t, want 1/1/true", store.progressCalls, store.progress, store.completed)
	}
}

func TestDownloadSyncHandlerIgnoresUnselectedExtra(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000016")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000017")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 0, SizeBytes: 2000},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 0.3, State: "downloading"}},
		files: []qbittorrent.TorrentFile{
			{Index: 0, Progress: 1, Priority: 1, Size: 2000},
			{Index: 1, Progress: 0, Priority: 0, Size: 5000},
			{Index: 2, Progress: 0.1, Priority: 0, Size: 800},
		},
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, time.Minute)
	if err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !store.completed {
		t.Fatalf("should complete even though unselected extra files are incomplete")
	}
	if store.progress != 1 {
		t.Fatalf("progress = %v, want 1 based on selected weighted", store.progress)
	}
}

func TestDownloadSyncHandlerSnoozesWhenSelectedIndexMissing(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000018")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000019")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 5, SizeBytes: 1000},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 1, State: "pausedUP"}},
		files: []qbittorrent.TorrentFile{
			{Index: 0, Progress: 1, Priority: 1, Size: 1000},
		},
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, time.Minute)
	err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) {
		t.Fatalf("Handle() error = %v, want snooze for missing index", err)
	}
	if store.completed {
		t.Fatalf("should not complete when selected index is missing")
	}
	if store.progress != 0 {
		t.Fatalf("progress = %v, want 0 for missing selected", store.progress)
	}
}

func TestDownloadSyncHandlerSnoozesWhenDuplicateQBIndex(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000020")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000021")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 2, SizeBytes: 1000},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 1, State: "pausedUP"}},
		files: []qbittorrent.TorrentFile{
			{Index: 2, Progress: 1, Priority: 1, Size: 1000},
			{Index: 2, Progress: 1, Priority: 1, Size: 1000},
		},
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, time.Minute)
	err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) {
		t.Fatalf("Handle() error = %v, want snooze for duplicate index", err)
	}
	if store.completed {
		t.Fatalf("should not complete when qB has duplicate index")
	}
}

func TestDownloadSyncHandlerRetriesWhenTorrentFilesAPIError(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000022")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 0, SizeBytes: 100},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 1, State: "pausedUP"}},
		filesErr: errors.New("qB unavailable"),
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, time.Minute)
	err := handler.Handle(context.Background(), domain.Operation{ResourceType: "download", ResourceID: downloadID})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "qbittorrent_unavailable" || !failure.Retryable {
		t.Fatalf("Handle() error = %#v, want retryable qbittorrent_unavailable", err)
	}
	if store.progressCalls != 0 || store.completed {
		t.Fatalf("should not persist progress or complete on qB files error: progress %d completed %t", store.progressCalls, store.completed)
	}
}

func TestDownloadSyncHandlerHandlesZeroSizeSelectedFiles(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000023")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000024")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 0, SizeBytes: 0},
			{FileIndex: 1, SizeBytes: 0},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 1, State: "pausedUP"}},
		files: []qbittorrent.TorrentFile{
			{Index: 0, Progress: 1, Priority: 1, Size: 0},
			{Index: 1, Progress: 1, Priority: 7, Size: 0},
		},
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, time.Minute)
	if err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !store.completed || store.progress != 1 {
		t.Fatalf("zero-size all complete: progress=%v completed=%t, want 1/true", store.progress, store.completed)
	}
	// 第二场景：零大小但其中一个未完成
	downloadID2 := uuid.MustParse("40000000-0000-0000-0000-000000000025")
	operationID2 := uuid.MustParse("40000000-0000-0000-0000-000000000026")
	store2 := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID2,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 0, SizeBytes: 0},
			{FileIndex: 1, SizeBytes: 0},
		},
	}}
	client2 := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 1, State: "pausedUP"}},
		files: []qbittorrent.TorrentFile{
			{Index: 0, Progress: 1, Priority: 1, Size: 0},
			{Index: 1, Progress: 0.3, Priority: 1, Size: 0},
		},
	}
	handler2 := NewDownloadSyncHandler(configuredDownloadTestStub(), store2, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client2, nil
	}, time.Minute)
	err := handler2.Handle(context.Background(), domain.Operation{ID: operationID2, ResourceType: "download", ResourceID: downloadID2})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) {
		t.Fatalf("Handle() error = %v, want snooze for incomplete zero-size", err)
	}
	if store2.completed {
		t.Fatalf("should not complete when one zero-size file is incomplete")
	}
	want := 0.65
	if store2.progress < want-1e-9 || store2.progress > want+1e-9 {
		t.Fatalf("progress = %v, want %v", store2.progress, want)
	}
}

func TestDownloadSyncHandlerSnoozesWhenSelectedFileProgressIsNaN(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000050")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000051")
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 0, SizeBytes: 1000},
		},
	}}
	nan := math.NaN()
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 1, AmountLeft: 0, TotalSize: 1000, State: "uploading"}},
		files: []qbittorrent.TorrentFile{
			{Index: 0, Progress: nan, Priority: 1, Size: 1000},
		},
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, 15*time.Second)
	err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) {
		t.Fatalf("Handle() error = %v, want snooze for NaN progress", err)
	}
	if store.completed {
		t.Fatalf("NaN progress must not complete")
	}
	if store.progressCalls != 1 {
		t.Fatalf("progress calls = %d, want 1", store.progressCalls)
	}
	if store.progress != 0 {
		t.Fatalf("progress = %v, want 0 for NaN progress treated as 0", store.progress)
	}
	if math.IsNaN(store.progress) || math.IsInf(store.progress, 0) {
		t.Fatalf("progress must be finite, got %v", store.progress)
	}
}

func TestEvaluateSelectedFilesCompletionHandlesNaN(t *testing.T) {
	nan := math.NaN()
	selected := []domain.DownloadSyncFile{{FileIndex: 0, SizeBytes: 1000}}
	qbFiles := []qbittorrent.TorrentFile{{Index: 0, Progress: nan, Priority: 1, Size: 1000}}
	progress, allComplete := evaluateSelectedFilesCompletion(selected, qbFiles)
	if math.IsNaN(progress) || math.IsInf(progress, 0) {
		t.Fatalf("progress must be finite, got %v", progress)
	}
	if progress != 0 {
		t.Fatalf("progress = %v, want 0 for NaN", progress)
	}
	if allComplete {
		t.Fatalf("allComplete = true, want false for NaN")
	}
	if progress < 0 || progress > 1 {
		t.Fatalf("progress out of bounds: %v", progress)
	}
}

func TestEvaluateSelectedFilesCompletionHugeWeightedProgressPartial(t *testing.T) {
	huge := int64(4611686018427387903) // 约 MaxInt64/2
	selected := []domain.DownloadSyncFile{
		{FileIndex: 0, SizeBytes: huge},
		{FileIndex: 1, SizeBytes: huge},
	}
	qbFiles := []qbittorrent.TorrentFile{
		{Index: 0, Progress: 1, Priority: 1, Size: huge},
		{Index: 1, Progress: 0.5, Priority: 1, Size: huge},
	}
	progress, allComplete := evaluateSelectedFilesCompletion(selected, qbFiles)
	if math.IsNaN(progress) || math.IsInf(progress, 0) {
		t.Fatalf("progress must be finite, got %v", progress)
	}
	if progress < 0 || progress > 1 {
		t.Fatalf("progress out of bounds: %v", progress)
	}
	// 加权期望：(huge*1 + huge*0.5) / (2*huge) = 0.75
	want := 0.75
	if progress < want-1e-9 || progress > want+1e-9 {
		t.Fatalf("progress = %v, want %v for huge weighted partial", progress, want)
	}
	if allComplete {
		t.Fatalf("allComplete = true, want false for partial huge weighted")
	}
}

func TestEvaluateSelectedFilesCompletionHugeWeightedProgressComplete(t *testing.T) {
	huge := int64(9223372036854775807) // MaxInt64
	selected := []domain.DownloadSyncFile{
		{FileIndex: 0, SizeBytes: huge},
		{FileIndex: 1, SizeBytes: huge},
	}
	qbFiles := []qbittorrent.TorrentFile{
		{Index: 0, Progress: 1, Priority: 1, Size: huge},
		{Index: 1, Progress: 1, Priority: 1, Size: huge},
	}
	progress, allComplete := evaluateSelectedFilesCompletion(selected, qbFiles)
	if math.IsNaN(progress) || math.IsInf(progress, 0) {
		t.Fatalf("progress must be finite, got %v", progress)
	}
	if progress != 1 {
		t.Fatalf("progress = %v, want 1 for huge weighted complete", progress)
	}
	if !allComplete {
		t.Fatalf("allComplete = false, want true for huge weighted complete")
	}
}

func TestDownloadSyncHandlerHugeWeightedPartialSnoozes(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000060")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000061")
	huge := int64(4611686018427387903)
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 0, SizeBytes: huge},
			{FileIndex: 1, SizeBytes: huge},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 1, State: "uploading"}},
		files: []qbittorrent.TorrentFile{
			{Index: 0, Progress: 1, Priority: 1, Size: huge},
			{Index: 1, Progress: 0.5, Priority: 1, Size: huge},
		},
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, 15*time.Second)
	err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) {
		t.Fatalf("Handle() error = %v, want snooze for huge weighted partial", err)
	}
	if store.completed {
		t.Fatalf("huge weighted partial must not complete")
	}
	if math.IsNaN(store.progress) || math.IsInf(store.progress, 0) {
		t.Fatalf("progress must be finite, got %v", store.progress)
	}
	want := 0.75
	if store.progress < want-1e-9 || store.progress > want+1e-9 {
		t.Fatalf("progress = %v, want %v", store.progress, want)
	}
}

func TestDownloadSyncHandlerHugeWeightedCompleteSucceeds(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000062")
	operationID := uuid.MustParse("40000000-0000-0000-0000-000000000063")
	huge := int64(9223372036854775807)
	store := &downloadSyncStoreStub{command: domain.DownloadSyncCommand{
		DownloadID:  downloadID,
		Status:      domain.DownloadDownloading,
		TorrentHash: workerTorrentHash,
		SelectedFiles: []domain.DownloadSyncFile{
			{FileIndex: 0, SizeBytes: huge},
			{FileIndex: 1, SizeBytes: huge},
		},
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, Progress: 1, State: "uploading"}},
		files: []qbittorrent.TorrentFile{
			{Index: 0, Progress: 1, Priority: 1, Size: huge},
			{Index: 1, Progress: 1, Priority: 1, Size: huge},
		},
	}
	handler := NewDownloadSyncHandler(configuredDownloadTestStub(), store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, 15*time.Second)
	if err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "download", ResourceID: downloadID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !store.completed {
		t.Fatalf("huge weighted complete must complete")
	}
	if store.progress != 1 {
		t.Fatalf("progress = %v, want 1", store.progress)
	}
	if math.IsNaN(store.progress) || math.IsInf(store.progress, 0) {
		t.Fatalf("progress must be finite, got %v", store.progress)
	}
}

func TestEvaluateSelectedFilesCompletionNegativeSizeSafe(t *testing.T) {
	selected := []domain.DownloadSyncFile{
		{FileIndex: 0, SizeBytes: -100},
		{FileIndex: 1, SizeBytes: 1000},
	}
	qbFiles := []qbittorrent.TorrentFile{
		{Index: 0, Progress: 1, Priority: 1, Size: 100},
		{Index: 1, Progress: 0.5, Priority: 1, Size: 1000},
	}
	progress, _ := evaluateSelectedFilesCompletion(selected, qbFiles)
	if math.IsNaN(progress) || math.IsInf(progress, 0) {
		t.Fatalf("progress must be finite for negative size, got %v", progress)
	}
	if progress < 0 || progress > 1 {
		t.Fatalf("progress out of bounds for negative size: %v", progress)
	}
	// 负值按安全边界回退为均值：(1 + 0.5)/2 = 0.75
	want := 0.75
	if progress < want-1e-9 || progress > want+1e-9 {
		t.Fatalf("progress = %v, want %v for negative size fallback", progress, want)
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
