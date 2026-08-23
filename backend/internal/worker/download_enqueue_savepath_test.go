package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
)

// P1 必修的 HTTP savePath 复用覆盖。

func TestDownloadEnqueueHandlerHTTPSourceReusesExistingInTemporaryCategoryWithoutFetching(t *testing.T) {
	downloadID := uuid.MustParse("50000000-0000-0000-0000-000000000001")
	operationID := uuid.MustParse("50000000-0000-0000-0000-000000000002")
	savePath := "/downloads/" + downloadID.String()
	correlationCategory := "emby-auto-" + downloadID.String()
	configuration := &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
			Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
		}},
		password: "secret",
	}
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID: downloadID,
		Status:     domain.DownloadEnqueuePending,
		SourceURI:  "https://cdn.example.test/show.torrent",
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{
			{Hash: workerTorrentHash, SavePath: savePath, Category: correlationCategory},
		},
		files: []qbittorrent.TorrentFile{{Index: 0, Name: "Show - S01E01.mkv", Size: 1000}},
	}
	fetcherCalls := 0
	fetcher := func(context.Context, string, domain.NetworkProxySettings) ([]byte, error) {
		fetcherCalls++
		return nil, permanentFailure("torrent_source_unavailable", "种子文件下载失败", errors.New("HTTP 404"))
	}
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}).WithTorrentSourceFetcher(fetcher)

	err := handler.Handle(context.Background(), domain.Operation{
		ID:           operationID,
		ResourceType: "download",
		ResourceID:   downloadID,
		Payload:      []byte(`{"defaultSeason":1,"defaultEpisode":1,"singleEpisode":true}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v, want reuse existing without fetch", err)
	}
	if fetcherCalls != 0 {
		t.Fatalf("fetcher calls = %d, want 0 when existing at temporary category", fetcherCalls)
	}
	if countCalls(client.calls, "add") != 0 {
		t.Fatalf("AddAndConfirm calls = %d, want 0 when existing reused", countCalls(client.calls, "add"))
	}
	if !store.completed || store.completion.TorrentHash != workerTorrentHash || store.completion.SavePath != savePath {
		t.Fatalf("completion = %#v, want hash %s and savePath %s", store.completion, workerTorrentHash, savePath)
	}
	if len(client.deletedHashes) != 0 {
		t.Fatalf("reused existing should not trigger compensation deletion, got %v", client.deletedHashes)
	}
}

func TestDownloadEnqueueHandlerHTTPSourceReusesExistingInManagedCategoryDespiteMissingTemp(t *testing.T) {
	downloadID := uuid.MustParse("50000000-0000-0000-0000-000000000011")
	savePath := "/downloads/" + downloadID.String()
	configuration := &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
			Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
		}},
		password: "secret",
	}
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID: downloadID,
		Status:     domain.DownloadEnqueuePending,
		SourceURI:  "https://cdn.example.test/show-moved.torrent",
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{
			{Hash: workerTorrentHash, SavePath: savePath, Category: qbittorrent.ManagedCategory},
		},
		files: []qbittorrent.TorrentFile{{Index: 0, Name: "Show - S01E01.mkv", Size: 1000}},
	}
	fetcherCalls := 0
	fetcher := func(context.Context, string, domain.NetworkProxySettings) ([]byte, error) {
		fetcherCalls++
		return nil, permanentFailure("torrent_source_unavailable", "种子文件下载失败", errors.New("HTTP 404"))
	}
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}).WithTorrentSourceFetcher(fetcher)

	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{"defaultSeason":1}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v, want reuse managed existing", err)
	}
	if fetcherCalls != 0 || countCalls(client.calls, "add") != 0 {
		t.Fatalf("fetcher/add calls = %d/%d, want 0/0 for managed existing", fetcherCalls, countCalls(client.calls, "add"))
	}
	if !store.completed {
		t.Fatal("managed existing should still complete enqueue")
	}
}

func TestDownloadEnqueueHandlerHTTPSourceAmbiguousSavePathIsRetryable(t *testing.T) {
	downloadID := uuid.New()
	savePath := "/downloads/" + downloadID.String()
	configuration := &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
			Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
		}},
	}
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID: downloadID, Status: domain.DownloadEnqueuePending, SourceURI: "https://cdn.example.test/ambiguous.torrent",
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{
			{Hash: workerTorrentHash, SavePath: savePath},
			{Hash: "abcdef0123456789abcdef0123456789abcdef01", SavePath: savePath},
		},
	}
	fetcherCalls := 0
	addCallsBefore := 0
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}).WithTorrentSourceFetcher(func(context.Context, string, domain.NetworkProxySettings) ([]byte, error) {
		fetcherCalls++
		return validTorrentFixture, nil
	})
	// ensure we count add calls after handle
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{"defaultSeason":1}`),
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "qbittorrent_correlation_ambiguous" || !failure.Retryable {
		t.Fatalf("Handle() error = %#v, want retryable qbittorrent_correlation_ambiguous", err)
	}
	if fetcherCalls != 0 {
		t.Fatalf("ambiguous correlation should not fetch, fetcher calls = %d", fetcherCalls)
	}
	if countCalls(client.calls, "add") != addCallsBefore {
		t.Fatalf("ambiguous correlation should not add, calls = %v", client.calls)
	}
	if len(client.deletedHashes) != 0 {
		t.Fatalf("ambiguous should not delete torrent, got %v", client.deletedHashes)
	}
	if store.completed {
		t.Fatal("ambiguous should not persist completion")
	}
}

func TestDownloadEnqueueHandlerHTTPSourceSavePathMismatchFallsBackToFetch(t *testing.T) {
	downloadID := uuid.New()
	savePath := "/downloads/" + downloadID.String()
	configuration := &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
			Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
		}},
	}
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID: downloadID, Status: domain.DownloadEnqueuePending, SourceURI: "https://cdn.example.test/normal.torrent",
	}}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{
			{Hash: "abcdef0123456789abcdef0123456789abcdef01", SavePath: "/downloads/other-id"},
		},
		resolution: qbittorrent.HashResolution{Hash: workerTorrentHash, Reason: qbittorrent.HashResolutionNew},
		files:      []qbittorrent.TorrentFile{{Index: 0, Name: "Show - S01E01.mkv", Size: 1000}},
	}
	fetcherCalls := 0
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}).WithTorrentSourceFetcher(func(_ context.Context, raw string, _ domain.NetworkProxySettings) ([]byte, error) {
		fetcherCalls++
		if raw != "https://cdn.example.test/normal.torrent" {
			t.Fatalf("fetcher url = %q", raw)
		}
		return validTorrentFixture, nil
	})
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{"defaultSeason":1}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v, want successful fetch fallback", err)
	}
	if fetcherCalls != 1 {
		t.Fatalf("fetcher calls = %d, want 1 for mismatch", fetcherCalls)
	}
	if countCalls(client.calls, "add") != 1 || len(client.addRequest.Torrent) == 0 || client.addRequest.TorrentFilename != "source.torrent" {
		t.Fatalf("addRequest should be multipart upload, calls=%v request=%#v", client.calls, client.addRequest)
	}
	if client.addRequest.SavePath != savePath {
		t.Fatalf("savePath = %q, want %q", client.addRequest.SavePath, savePath)
	}
	if !store.completed {
		t.Fatal("mismatch fallback should complete")
	}
}

func TestDownloadEnqueueHandlerHTTPSourceListFailureIsRetryableWithoutFetch(t *testing.T) {
	downloadID := uuid.New()
	configuration := &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
			Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
		}},
	}
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID: downloadID, Status: domain.DownloadEnqueuePending, SourceURI: "https://cdn.example.test/fail-list.torrent",
	}}
	client := &torrentClientStub{listErr: errors.New("qB unavailable")}
	fetcherCalls := 0
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}).WithTorrentSourceFetcher(func(context.Context, string, domain.NetworkProxySettings) ([]byte, error) {
		fetcherCalls++
		return validTorrentFixture, nil
	})
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{"defaultSeason":1}`),
	})
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Retryable {
		t.Fatalf("Handle() error = %#v, want retryable list failure", err)
	}
	if fetcherCalls != 0 {
		t.Fatalf("list failure should not fetch, calls=%d", fetcherCalls)
	}
	if countCalls(client.calls, "add") != 0 {
		t.Fatalf("list failure should not add, calls=%v", client.calls)
	}
}

func TestDownloadEnqueueHandlerExistingDoesNotDeleteOnDownstreamFailure(t *testing.T) {
	downloadID := uuid.New()
	savePath := "/downloads/" + downloadID.String()
	configuration := &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
			Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
		}},
	}
	store := &downloadStoreStub{
		command:     domain.DownloadEnqueueCommand{DownloadID: downloadID, Status: domain.DownloadEnqueuePending, SourceURI: "https://cdn.example.test/existing-fail.torrent"},
		completeErr: errors.New("db failed"),
	}
	client := &torrentClientStub{
		torrents: []qbittorrent.Torrent{{Hash: workerTorrentHash, SavePath: savePath}},
		files:    []qbittorrent.TorrentFile{{Index: 0, Name: "Show - S01E01.mkv", Size: 1000}},
	}
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}).WithTorrentSourceFetcher(func(context.Context, string, domain.NetworkProxySettings) ([]byte, error) {
		t.Fatal("existing should not fetch")
		return nil, nil
	})
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{"defaultSeason":1}`),
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "download_storage_unavailable" || !failure.Retryable {
		t.Fatalf("Handle() error = %#v, want retryable storage failure", err)
	}
	if len(client.deletedHashes) != 0 {
		t.Fatalf("existing downstream failure should not delete, got %v", client.deletedHashes)
	}
}

func TestDownloadEnqueueHandlerNewTorrentStillCompensatesOnDownstreamFailure(t *testing.T) {
	downloadID := uuid.New()
	configuration := &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
			Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
		}},
	}
	store := &downloadStoreStub{
		command:     domain.DownloadEnqueueCommand{DownloadID: downloadID, Status: domain.DownloadEnqueuePending, SourceURI: "https://cdn.example.test/new-fail.torrent"},
		completeErr: errors.New("db failed"),
	}
	client := &torrentClientStub{
		torrents:   []qbittorrent.Torrent{},
		resolution: qbittorrent.HashResolution{Hash: workerTorrentHash, Reason: qbittorrent.HashResolutionNew},
		files:      []qbittorrent.TorrentFile{{Index: 0, Name: "Show - S01E01.mkv", Size: 1000}},
	}
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}).WithTorrentSourceFetcher(stubTorrentFetcher(validTorrentFixture, nil))
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{"defaultSeason":1}`),
	})
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Retryable {
		t.Fatalf("Handle() error = %#v, want retryable", err)
	}
	if len(client.deletedHashes) != 1 || client.deletedHashes[0] != workerTorrentHash {
		t.Fatalf("new torrent downstream failure should compensation delete, got %v", client.deletedHashes)
	}
}
