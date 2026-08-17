package service

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func TestRecalculateRSSSubscriptionProgressRejectsNewerModelBeforeReadingSources(t *testing.T) {
	subscriptionID := uuid.New()
	err := recalculateRSSSubscriptionProgress(context.Background(), nil, []rssSubscriptionProgressCandidate{{
		subscriptionID: subscriptionID,
		modelVersion:   rssSubscriptionProgressModelVersion + 1,
	}})
	if err == nil || !strings.Contains(err.Error(), "newer model version") {
		t.Fatalf("recalculateRSSSubscriptionProgress() error = %v, want newer model rejection", err)
	}
}

func TestRSSSubscriptionProgressFromRowRequiresCurrentCleanProjection(t *testing.T) {
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	valid := db.RssSubscriptionProgress{
		SubscriptionID:     repository.UUIDToPG(uuid.New()),
		OverallProgress:    0.625,
		TaskCount:          4,
		CompletedTaskCount: 2,
		AttentionTaskCount: 1,
		SourceRevision:     7,
		CalculatedRevision: 7,
		ModelVersion:       rssSubscriptionProgressModelVersion,
		CalculatedAt:       now,
	}
	progress, err := rssSubscriptionProgressFromRow(valid)
	if err != nil {
		t.Fatalf("rssSubscriptionProgressFromRow() error = %v", err)
	}
	if progress.overallProgress != 0.625 || progress.taskCount != 4 ||
		progress.completedTaskCount != 2 || progress.attentionTaskCount != 1 {
		t.Fatalf("progress = %#v", progress)
	}

	cases := map[string]func(*db.RssSubscriptionProgress){
		"dirty":              func(row *db.RssSubscriptionProgress) { row.Dirty = true },
		"stale revision":     func(row *db.RssSubscriptionProgress) { row.CalculatedRevision-- },
		"old model version":  func(row *db.RssSubscriptionProgress) { row.ModelVersion-- },
		"missing calculated": func(row *db.RssSubscriptionProgress) { row.CalculatedAt = pgtype.Timestamptz{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			row := valid
			mutate(&row)
			if _, err := rssSubscriptionProgressFromRow(row); err == nil || !strings.Contains(err.Error(), "not reconciled") {
				t.Fatalf("rssSubscriptionProgressFromRow() error = %v, want not reconciled", err)
			}
		})
	}
}

func TestRSSSubscriptionProgressCountRejectsResponseOverflow(t *testing.T) {
	if got, err := rssSubscriptionProgressCount(math.MaxInt32); err != nil || got != math.MaxInt32 {
		t.Fatalf("rssSubscriptionProgressCount(max) = %d, %v", got, err)
	}
	for _, value := range []int{-1, math.MaxInt32 + 1} {
		if _, err := rssSubscriptionProgressCount(value); err == nil {
			t.Fatalf("rssSubscriptionProgressCount(%d) error = nil", value)
		}
	}
}
