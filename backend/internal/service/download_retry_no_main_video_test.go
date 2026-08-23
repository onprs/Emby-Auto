package service

import (
	"testing"
	"time"
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
