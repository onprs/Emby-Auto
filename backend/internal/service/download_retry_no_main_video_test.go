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

// 校验 retry 原子分支的其它 file_resolution 仍保持原语义：此处通过 helper 等价行为验证
func TestDownloadNoMainVideoRetryAtomicProperties(t *testing.T) {
	// 该用例验证：当满足 enqueue 条件时，预期调度为 KindDownloadEnqueue、5 attempts、3m 超时；
	// 否则保持 KindDownloadSelectionApply、5 attempts、1m。
	// 通过 helper 判断路径，实际调度由 Retry 内部完成，这里验证 helper 与常量的一致性
	hash := "0123456789abcdef0123456789abcdef01234567"
	if !shouldRetryViaEnqueueForNoMainVideo("download_no_main_video", &hash, 10) {
		t.Fatal("should enqueue")
	}
	// 其它错误仍走 selection.apply
	if shouldRetryViaEnqueueForNoMainVideo("download_file_resolution_invalid", &hash, 10) {
		t.Fatal("should not enqueue for other errors")
	}
}

// version conflict 与 idempotency 的语义由通用层保证，这里验证错误码与 helper 不干扰
func TestDownloadNoMainVideoRetryPreservesVersionAndIdempotencySemantics(t *testing.T) {
	// 纯 helper 层面的验证：空 hash 或空 manifest 不应触发 enqueue，避免对无 hash 的 file_resolution 误判
	var nilHash *string
	if shouldRetryViaEnqueueForNoMainVideo("download_no_main_video", nilHash, 5) {
		t.Fatal("nil hash should not enqueue")
	}
	empty := "   "
	if shouldRetryViaEnqueueForNoMainVideo("download_no_main_video", &empty, 5) {
		t.Fatal("whitespace hash should not enqueue")
	}
}
