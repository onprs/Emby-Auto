package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func TestDownloadRetryManifestChangeStopsAndOnlyCompensatesNewTorrents(t *testing.T) {
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"}, Paths: domain.PathSettings{DownloadRoot: "/downloads"},
	}}}
	for _, reason := range []qbittorrent.HashResolutionReason{qbittorrent.HashResolutionNew, qbittorrent.HashResolutionExisting} {
		t.Run(string(reason), func(t *testing.T) {
			downloadID := uuid.New()
			store := &downloadStoreStub{
				command:     domain.DownloadEnqueueCommand{DownloadID: downloadID, Status: domain.DownloadEnqueuePending, SourceURI: "magnet:?xt=urn:btih:" + workerTorrentHash, HasResolvedManifest: true},
				completeErr: service.NewError("download_manifest_changed", "文件清单已变化", service.ErrStateConflict, map[string]any{}),
			}
			client := &torrentClientStub{
				resolution: qbittorrent.HashResolution{Hash: workerTorrentHash, Reason: reason},
				files:      []qbittorrent.TorrentFile{{Index: 0, Name: "Series.S01E01.mkv", Size: 1024}},
			}
			handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) { return client, nil })
			err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{"defaultSeason":1}`)})
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != "download_manifest_changed" || failure.Retryable {
				t.Fatalf("清单变化未终止恢复: %#v", err)
			}
			wantDeleted := 0
			if reason == qbittorrent.HashResolutionNew {
				wantDeleted = 1
			}
			if len(client.deletedHashes) != wantDeleted {
				t.Fatalf("补偿处理越过本次新增种子的边界: %v", client.deletedHashes)
			}
		})
	}
}

func TestDownloadRetryRetainsResolvedManifestWithoutNewFileAdjudication(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		downloadID := uuid.New()
		configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{
			QBittorrent: domain.QBittorrentSettings{URL: "http://qb:8080"}, Paths: domain.PathSettings{DownloadRoot: "/downloads"},
			Agent: domain.AgentSettings{Enabled: true, DownloadFileSelectionMode: domain.AgentResolutionValidatedAuto},
		}}}
		store := &downloadStoreStub{command: domain.DownloadEnqueueCommand{
			DownloadID: downloadID, AcquisitionID: uuid.New(), Status: domain.DownloadEnqueuePending,
			SourceURI: "magnet:?xt=urn:btih:" + workerTorrentHash, HasResolvedManifest: true, FileResolutionSource: "agent",
		}}
		client := &torrentClientStub{resolution: qbittorrent.HashResolution{Hash: workerTorrentHash, Reason: qbittorrent.HashResolutionNew}, files: []qbittorrent.TorrentFile{{Index: 0, Name: "Unnumbered.mkv", Size: 1024}}}
		agent := &downloadAgentResolutionStub{}
		handler := NewDownloadEnqueueHandler(configuration, store, func(qbittorrent.ClientOptions) (TorrentClient, error) { return client, nil }, agent).WithManifestResolutionEnabled(enabled)
		if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "download", ResourceID: downloadID, Payload: []byte(`{"defaultSeason":1}`)}); err != nil {
			t.Fatal(err)
		}
		if !store.completed || store.legacyCompleted {
			t.Fatal("恢复没有进入保留已确认文件清单的流程")
		}
		for _, created := range agent.created {
			if created.Capability == domain.AgentCapabilityDownloadFileResolution {
				t.Fatal("已确认清单在恢复后重复进入文件裁决")
			}
		}
	}
}
