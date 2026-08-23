package service

import (
	"testing"
	"time"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

// 校验 download_no_main_video 重试走 enqueue 的判定条件与参数
func TestShouldRetryViaEnqueueForNoMainVideo(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef01234567"
	empty := ""
	cases := []struct {
		name      string
		errorCode string
		hash      *string
		count     int
		want      bool
	}{
		{name: "no hash not enqueue", errorCode: "download_no_main_video", hash: nil, count: 5, want: false},
		{name: "empty hash not enqueue", errorCode: "download_no_main_video", hash: &empty, count: 5, want: false},
		{name: "no manifest not enqueue", errorCode: "download_no_main_video", hash: &hash, count: 0, want: false},
		{name: "has hash and manifest enqueue", errorCode: "download_no_main_video", hash: &hash, count: 1, want: true},
		{name: "has hash and many manifest enqueue", errorCode: "download_no_main_video", hash: &hash, count: 265, want: true},
		{name: "other file_resolution keeps selection", errorCode: "download_file_resolution_invalid", hash: &hash, count: 5, want: false},
		{name: "user selection invalid keeps selection", errorCode: "download_file_scope_violation", hash: &hash, count: 5, want: false},
		{name: "qb apply failed keeps selection", errorCode: "qbittorrent_file_priority_failed", hash: &hash, count: 5, want: false},
		{name: "empty error not enqueue", errorCode: "", hash: &hash, count: 5, want: false},
	}
	for _, tc := range cases {
		if got := shouldRetryViaEnqueueForNoMainVideo(tc.errorCode, tc.hash, tc.count); got != tc.want {
			t.Fatalf("%s: got %t want %t", tc.name, got, tc.want)
		}
	}
}

func TestDownloadEnqueueTimeoutIsThreeMinutes(t *testing.T) {
	if DownloadEnqueueTimeout != 3*time.Minute {
		t.Fatalf("DownloadEnqueueTimeout = %v, want 3m", DownloadEnqueueTimeout)
	}
}

func TestHasReclassifiableVideo(t *testing.T) {
	top := "SyntheticPack S02 SP Limited Edition 1080p"
	staleExtraFiles := []db.DownloadFile{
		{FileIndex: 0, RelativePath: top + "/Synthetic - 01.mkv", SizeBytes: 2048},
		{FileIndex: 1, RelativePath: top + "/Synthetic - 02.mkv", SizeBytes: 2048},
		{FileIndex: 2, RelativePath: top + "/Synthetic - 01.ass", SizeBytes: 80},
	}
	if has, err := hasReclassifiableVideo(staleExtraFiles, domain.FileSelectionOptions{DefaultSeason: 2}); err != nil || !has {
		t.Fatalf("stale extra should be reclassifiable as video, has=%t err=%v", has, err)
	}
	extraOnlyFiles := []db.DownloadFile{
		{FileIndex: 0, RelativePath: "Show S01 Trailer.mkv", SizeBytes: 2048},
		{FileIndex: 1, RelativePath: "Show S01 NCOP.mkv", SizeBytes: 2048},
		{FileIndex: 2, RelativePath: "notes.txt", SizeBytes: 100},
	}
	if has, err := hasReclassifiableVideo(extraOnlyFiles, domain.FileSelectionOptions{DefaultSeason: 1, DefaultEpisode: 1, SingleEpisode: true}); err != nil || has {
		t.Fatalf("extra-only should not be reclassifiable, has=%t err=%v", has, err)
	}
	otherOnlyFiles := []db.DownloadFile{
		{FileIndex: 0, RelativePath: "readme.txt", SizeBytes: 100},
	}
	if has, err := hasReclassifiableVideo(otherOnlyFiles, domain.FileSelectionOptions{DefaultSeason: 1}); err != nil || has {
		t.Fatalf("other-only should not be reclassifiable, has=%t err=%v", has, err)
	}
}

func TestHasReclassifiableVideoClassificationError(t *testing.T) {
	unsafeFiles := []db.DownloadFile{
		{FileIndex: 0, RelativePath: "../evil.mkv", SizeBytes: 1024},
	}
	if _, err := hasReclassifiableVideo(unsafeFiles, domain.FileSelectionOptions{DefaultSeason: 1}); err == nil {
		t.Fatal("unsafe path should return classification error")
	}
	duplicateFiles := []db.DownloadFile{
		{FileIndex: 0, RelativePath: "Show.S01E01.mkv", SizeBytes: 1024},
		{FileIndex: 0, RelativePath: "Show.S01E02.mkv", SizeBytes: 1024},
	}
	if _, err := hasReclassifiableVideo(duplicateFiles, domain.FileSelectionOptions{DefaultSeason: 1}); err == nil {
		t.Fatal("duplicate index should return classification error")
	}
	invalidSeason := []db.DownloadFile{
		{FileIndex: 0, RelativePath: "Show.S01E01.mkv", SizeBytes: 1024},
	}
	if _, err := hasReclassifiableVideo(invalidSeason, domain.FileSelectionOptions{DefaultSeason: 0}); err == nil {
		t.Fatal("invalid default season should return classification error")
	}
}
