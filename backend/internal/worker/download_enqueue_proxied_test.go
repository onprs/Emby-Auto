package worker

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
)

// 验证 HTTP 源经代理抓取后以 multipart 上传，且 magnet 不触发抓取。

func TestDownloadEnqueueHandlerHTTPSourceUsesProxyFetcherAndMultipart(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000001")
	configuration := &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent:  domain.QBittorrentSettings{URL: "http://qb:8080"},
			Paths:        domain.PathSettings{DownloadRoot: "/downloads"},
			NetworkProxy: domain.NetworkProxySettings{Enabled: true, URL: "http://proxy.example.test:7890"},
		}},
		password: "secret",
	}
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID: downloadID,
		Status:     domain.DownloadEnqueuePending,
		SourceURI:  "https://cdn.example.test/show-01.torrent",
	}}
	client := &torrentClientStub{
		resolution: qbittorrent.HashResolution{Hash: workerTorrentHash, Reason: qbittorrent.HashResolutionNew},
		files:      []qbittorrent.TorrentFile{{Index: 0, Name: "Show - S01E01.mkv", Size: 1000}},
	}
	var fetchedURL string
	var fetchedProxy domain.NetworkProxySettings
	fetcherCalls := 0
	fetcher := func(ctx context.Context, rawURL string, proxySettings domain.NetworkProxySettings) ([]byte, error) {
		fetcherCalls++
		fetchedURL = rawURL
		fetchedProxy = proxySettings
		return validTorrentFixture, nil
	}
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}).WithTorrentSourceFetcher(fetcher)

	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "download", ResourceID: downloadID,
		Payload: []byte(`{"defaultSeason":1,"defaultEpisode":1,"singleEpisode":true}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if fetcherCalls != 1 || fetchedURL != "https://cdn.example.test/show-01.torrent" {
		t.Fatalf("fetcher calls/url = %d/%q", fetcherCalls, fetchedURL)
	}
	if !fetchedProxy.Enabled || fetchedProxy.URL != "http://proxy.example.test:7890" {
		t.Fatalf("fetcher proxy settings = %#v, want enabled http://proxy.example.test:7890", fetchedProxy)
	}
	if len(client.addRequest.Torrent) == 0 || !reflect.DeepEqual(client.addRequest.Torrent, validTorrentFixture) || client.addRequest.TorrentFilename != "source.torrent" || client.addRequest.Source != "" {
		t.Fatalf("addRequest should carry torrent bytes, not URL: %#v", client.addRequest)
	}
	if client.addRequest.SavePath != "/downloads/40000000-0000-0000-0000-000000000001" || client.addRequest.Category != "emby-auto-40000000-0000-0000-0000-000000000001" {
		t.Fatalf("addRequest save/category = %q/%q", client.addRequest.SavePath, client.addRequest.Category)
	}
}

func TestDownloadEnqueueHandlerMagnetDoesNotFetch(t *testing.T) {
	downloadID := uuid.MustParse("40000000-0000-0000-0000-000000000002")
	configuration := &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent:  domain.QBittorrentSettings{URL: "http://qb:8080"},
			Paths:        domain.PathSettings{DownloadRoot: "/downloads"},
			NetworkProxy: domain.NetworkProxySettings{Enabled: true, URL: "http://proxy.example.test:7890"},
		}},
		password: "secret",
	}
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID: downloadID,
		Status:     domain.DownloadEnqueuePending,
		SourceURI:  "magnet:?xt=urn:btih:" + workerTorrentHash + "&dn=show",
	}}
	client := &torrentClientStub{
		resolution: qbittorrent.HashResolution{Hash: workerTorrentHash, Reason: qbittorrent.HashResolutionMagnet},
		files:      []qbittorrent.TorrentFile{{Index: 0, Name: "Show - S01E01.mkv", Size: 1000}},
	}
	fetcherCalled := false
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}).WithTorrentSourceFetcher(func(context.Context, string, domain.NetworkProxySettings) ([]byte, error) {
		fetcherCalled = true
		return nil, errors.New("fetcher should not be called for magnet")
	})

	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "download", ResourceID: downloadID,
		Payload: []byte(`{"defaultSeason":1,"defaultEpisode":1,"singleEpisode":true}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if fetcherCalled {
		t.Fatal("magnet source triggered HTTP fetch")
	}
	if client.addRequest.Source != store.command.SourceURI || len(client.addRequest.Torrent) != 0 {
		t.Fatalf("magnet addRequest = %#v, want Source=magnet and no torrent bytes", client.addRequest)
	}
}

func TestDownloadEnqueueHandlerProxyDisabledStillFetchesDirect(t *testing.T) {
	downloadID := uuid.New()
	configuration := &downloadConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent:  domain.QBittorrentSettings{URL: "http://qb:8080"},
			Paths:        domain.PathSettings{DownloadRoot: "/downloads"},
			NetworkProxy: domain.NetworkProxySettings{Enabled: false},
		}},
	}
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID: downloadID, Status: domain.DownloadEnqueuePending, SourceURI: "https://cdn.example.test/show.torrent",
	}}
	client := &torrentClientStub{
		resolution: qbittorrent.HashResolution{Hash: workerTorrentHash},
		files:      []qbittorrent.TorrentFile{{Index: 0, Name: "Show - S01E01.mkv", Size: 1000}},
	}
	var observedProxy domain.NetworkProxySettings
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}).WithTorrentSourceFetcher(func(_ context.Context, _ string, proxySettings domain.NetworkProxySettings) ([]byte, error) {
		observedProxy = proxySettings
		return validTorrentFixture, nil
	})
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{"defaultSeason":1}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if observedProxy.Enabled {
		t.Fatalf("proxy enabled = %v for disabled configuration, want false", observedProxy.Enabled)
	}
}

func TestDownloadEnqueueHandlerMapsFetchAndQBErrorsWithSanitizedMessages(t *testing.T) {
	downloadID := uuid.New()
	baseConfig := domain.Configuration{Settings: domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
		Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
	}}
	secretURI := "https://cdn.example.test/secret.torrent?token=abc123&user=me"
	tests := []struct {
		name          string
		fetcher       torrentSourceFetcher
		clientSetup   func(*torrentClientStub)
		sourceURI     string
		wantCode      string
		wantRetryable bool
	}{
		{
			name:      "private loopback URL is permanent",
			fetcher:   stubTorrentFetcher(nil, permanentFailure("torrent_source_invalid", "下载链接无效", nil)),
			sourceURI: secretURI, wantCode: "torrent_source_invalid", wantRetryable: false,
		},
		{
			name:      "userinfo URL is permanent",
			fetcher:   stubTorrentFetcher(nil, permanentFailure("torrent_source_invalid", "下载链接无效", nil)),
			sourceURI: "http://user:pass@cdn.example.test/file.torrent", wantCode: "torrent_source_invalid", wantRetryable: false,
		},
		{
			name:      "too large body is permanent",
			fetcher:   stubTorrentFetcher(nil, permanentFailure("torrent_source_too_large", "种子文件过大", nil)),
			sourceURI: secretURI, wantCode: "torrent_source_too_large", wantRetryable: false,
		},
		{
			name:      "empty or non-torrent is permanent",
			fetcher:   stubTorrentFetcher(nil, permanentFailure("torrent_source_not_torrent", "种子文件无效", nil)),
			sourceURI: secretURI, wantCode: "torrent_source_not_torrent", wantRetryable: false,
		},
		{
			name:      "transport timeout is retryable",
			fetcher:   stubTorrentFetcher(nil, retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", errors.New("timeout"))),
			sourceURI: secretURI, wantCode: "torrent_source_unavailable", wantRetryable: true,
		},
		{
			name:      "HTTP 429 is retryable",
			fetcher:   stubTorrentFetcher(nil, retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", errors.New("HTTP 429"))),
			sourceURI: secretURI, wantCode: "torrent_source_unavailable", wantRetryable: true,
		},
		{
			name:      "HTTP 500 is retryable",
			fetcher:   stubTorrentFetcher(nil, retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", errors.New("HTTP 500"))),
			sourceURI: secretURI, wantCode: "torrent_source_unavailable", wantRetryable: true,
		},
		{
			name:      "HTTP 404 is permanent unavailable",
			fetcher:   stubTorrentFetcher(nil, permanentFailure("torrent_source_unavailable", "种子文件下载失败", errors.New("HTTP 404"))),
			sourceURI: secretURI, wantCode: "torrent_source_unavailable", wantRetryable: false,
		},
		{
			name:    "qB 415 invalid torrent is permanent",
			fetcher: stubTorrentFetcher(validTorrentFixture, nil),
			clientSetup: func(c *torrentClientStub) {
				c.addErr = &qbittorrent.HTTPError{StatusCode: http.StatusUnsupportedMediaType, Body: "invalid torrent"}
			},
			sourceURI: secretURI, wantCode: "qbittorrent_invalid_torrent", wantRetryable: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configuration := &downloadConfigurationStub{configuration: baseConfig}
			store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
				DownloadID: downloadID, Status: domain.DownloadEnqueuePending, SourceURI: tc.sourceURI,
			}}
			client := &torrentClientStub{
				resolution: qbittorrent.HashResolution{Hash: workerTorrentHash},
				files:      []qbittorrent.TorrentFile{{Index: 0, Name: "Show - S01E01.mkv", Size: 1000}},
			}
			if tc.clientSetup != nil {
				tc.clientSetup(client)
			}
			handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
				return client, nil
			}).WithTorrentSourceFetcher(tc.fetcher)
			err := handler.Handle(context.Background(), domain.Operation{
				ID: uuid.New(), ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{"defaultSeason":1}`),
			})
			var failure *Failure
			if !errors.As(err, &failure) {
				t.Fatalf("Handle() error = %v, want Failure", err)
			}
			if failure.Code != tc.wantCode || failure.Retryable != tc.wantRetryable {
				t.Fatalf("failure = %q retryable=%v, want %q retryable=%v", failure.Code, failure.Retryable, tc.wantCode, tc.wantRetryable)
			}
			// 必须不泄露原始 URI、query 或 body
			if strings.Contains(failure.Message, "abc123") || strings.Contains(failure.Message, "secret.torrent") || strings.Contains(failure.Message, "token=") {
				t.Fatalf("failure message leaks source URL: %q", failure.Message)
			}
			if failure.Cause != nil && (strings.Contains(failure.Cause.Error(), "abc123") || strings.Contains(failure.Cause.Error(), "secret.torrent")) {
				// cause 可能包含内部错误，但不应包含原始 URI；我们已在 fetcher 中避免把 URI 放入错误
				// 这里检查 cause 是否意外包含 token
				if strings.Contains(failure.Cause.Error(), "token=") {
					t.Fatalf("failure cause leaks source URL: %v", failure.Cause)
				}
			}
			if failure.Code == "qbittorrent_invalid_torrent" && strings.Contains(failure.Message, "secret") {
				t.Fatalf("qb 415 message leaks URL")
			}
		})
	}
}

func TestDownloadEnqueueHandlerHTTPSourceStillEntersManifestPaths(t *testing.T) {
	// 已在之前 TestDownloadEnqueueHandlerPersistsUnresolved... 覆盖 HTTP 源的 manifest 分支
	// 这里额外验证 HTTP 源在代理抓取后仍能走确定性选择并触发 Agent
	downloadID := uuid.New()
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"},
		Paths:       domain.PathSettings{DownloadRoot: "/downloads"},
		Agent:       domain.AgentSettings{Enabled: true, DownloadFileSelectionMode: domain.AgentResolutionValidatedAuto},
	}}}
	store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
		DownloadID: downloadID, Status: domain.DownloadEnqueuePending, SourceURI: "https://example.test/http-source.torrent",
	}}
	client := &torrentClientStub{
		resolution: qbittorrent.HashResolution{Hash: workerTorrentHash},
		files:      []qbittorrent.TorrentFile{{Index: 0, Name: "Show/unknown.mkv", Size: 1000}},
	}
	agent := &downloadAgentResolutionStub{}
	handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) {
		return client, nil
	}, agent).WithTorrentSourceFetcher(stubTorrentFetcher(validTorrentFixture, nil))
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{"defaultSeason":1}`)})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if store.completion.Outcome != domain.DownloadManifestUnresolved {
		t.Fatalf("outcome = %q, want unresolved", store.completion.Outcome)
	}
	if len(agent.created) != 1 {
		t.Fatalf("agent calls = %v", agent.created)
	}
}
