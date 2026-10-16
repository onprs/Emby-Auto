package service

import (
	"context"
	"strings"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
)

type changingBackgroundTorrents struct {
	*backgroundTorrentClientStub
	after []qbittorrent.Torrent
}

func (client *changingBackgroundTorrents) ListTorrents(ctx context.Context, category string) ([]qbittorrent.Torrent, error) {
	if len(client.resumed) > 0 || len(client.stopped) > 0 {
		return client.after, nil
	}
	return client.backgroundTorrentClientStub.ListTorrents(ctx, category)
}

func TestBackgroundTorrentResumeRetainsMarksWhenEvidenceDisappearsOrFails(t *testing.T) {
	for _, state := range []string{"", "error", "missingFiles", "unrecognized"} {
		t.Run("state="+state, func(t *testing.T) {
			original := &backgroundTorrentClientStub{torrents: []qbittorrent.Torrent{{Hash: backgroundTorrentHashA, State: "stoppedDL", Tags: qbittorrent.RuntimePausedTag}}}
			client := &changingBackgroundTorrents{backgroundTorrentClientStub: original}
			if state != "" {
				client.after = []qbittorrent.Torrent{{Hash: backgroundTorrentHashA, State: state}}
			}
			transfers := configuredBackgroundTransfers(original)
			transfers.newClient = func(qbittorrent.ClientOptions) (BackgroundTorrentClient, error) { return client, nil }
			if err := transfers.Resume(context.Background()); err == nil {
				t.Fatal("下载证据丢失或失败时仍确认恢复")
			}
			if len(original.removed) != 0 || !qbittorrent.TorrentHasTag(original.torrents[0], qbittorrent.RuntimePausedTag) {
				t.Fatal("未确认恢复时移除了恢复标记")
			}
		})
	}
}

func TestBackgroundTorrentPauseRejectsDisappearedTorrent(t *testing.T) {
	original := &backgroundTorrentClientStub{torrents: []qbittorrent.Torrent{{Hash: backgroundTorrentHashA, State: "downloading"}}}
	client := &changingBackgroundTorrents{backgroundTorrentClientStub: original}
	transfers := configuredBackgroundTransfers(original)
	transfers.newClient = func(qbittorrent.ClientOptions) (BackgroundTorrentClient, error) { return client, nil }
	if err := transfers.Pause(context.Background()); err == nil {
		t.Fatal("下载证据丢失时仍确认停止")
	}
}

func TestBackgroundTorrentTransitionMatchesNormalizedHashes(t *testing.T) {
	client := &backgroundTorrentClientStub{torrents: []qbittorrent.Torrent{{Hash: strings.ToUpper(backgroundTorrentHashA), State: "stoppedDL"}}}
	if err := waitForBackgroundTorrents(context.Background(), client, []string{backgroundTorrentHashA}, true); err != nil {
		t.Fatalf("规范化后的下载引用未匹配：%v", err)
	}
}
