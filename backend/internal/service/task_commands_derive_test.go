package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/google/uuid"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
)

func TestDeriveSecondaryIdempotencyKeyBoundedAndStable(t *testing.T) {
	taskID1 := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	taskID2 := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	primary := "primary-key-" + strings.Repeat("x", 100)
	kindVideo := appqueue.KindTranscodeRun
	kindSubtitle := appqueue.KindSubtitlePrepare
	key1 := deriveSecondaryIdempotencyKey(primary, kindVideo, taskID1)
	key1Again := deriveSecondaryIdempotencyKey(primary, kindVideo, taskID1)
	keyDifferentTask := deriveSecondaryIdempotencyKey(primary, kindVideo, taskID2)
	keyDifferentKind := deriveSecondaryIdempotencyKey(primary, kindSubtitle, taskID1)
	keyDifferentPrimary := deriveSecondaryIdempotencyKey(primary+"diff", kindVideo, taskID1)
	if key1 != key1Again {
		t.Fatalf("derived key must be stable")
	}
	if key1 == keyDifferentTask {
		t.Fatalf("different task should produce different derived key")
	}
	if key1 == keyDifferentKind {
		t.Fatalf("different kind should produce different derived key")
	}
	if key1 == keyDifferentPrimary {
		t.Fatalf("different primary should produce different digest")
	}
	if len(key1) >= 256 {
		t.Fatalf("derived key must be well below 256, got %d", len(key1))
	}
	if strings.Contains(key1, primary) {
		t.Fatalf("derived key must not contain raw primary")
	}
	digest := sha256.Sum256([]byte(primary))
	expectedHex := hex.EncodeToString(digest[:])
	expected := "task-retry:" + taskID1.String() + ":" + kindVideo + ":" + expectedHex
	if key1 != expected {
		t.Fatalf("derived key format mismatch: got %q want %q", key1, expected)
	}
	if strings.ToLower(expectedHex) != expectedHex {
		t.Fatalf("hex digest must be lowercase")
	}
	if len(expectedHex) != 64 {
		t.Fatalf("hex digest must be 64 chars")
	}
}

func TestDeriveSecondaryIdempotencyKeyLengthBelowLimit(t *testing.T) {
	taskID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	// 256-char primary still yields bounded derived key
	primary256 := strings.Repeat("a", 256)
	derived := deriveSecondaryIdempotencyKey(primary256, appqueue.KindTranscodeRun, taskID)
	if len(derived) >= 256 {
		t.Fatalf("derived from 256-char primary must still be <256, got %d", len(derived))
	}
	if !strings.HasPrefix(derived, "task-retry:") {
		t.Fatalf("derived must have fixed ASCII prefix, got %q", derived)
	}
	// 257-char primary would be rejected at entry, but derive itself should still be bounded (defense)
	primary257 := strings.Repeat("a", 257)
	derived257 := deriveSecondaryIdempotencyKey(primary257, appqueue.KindTranscodeRun, taskID)
	if len(derived257) >= 256 {
		t.Fatalf("derived from 257-char primary must still be <256, got %d", len(derived257))
	}
}
