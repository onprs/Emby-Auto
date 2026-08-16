package service

import (
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte(id), Valid: true}
}

func pgTimestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func numeric(value float64) pgtype.Numeric {
	if value == 0 {
		return pgtype.Numeric{}
	}
	return pgtype.Numeric{Int: big.NewInt(int64(value * 100)), Exp: -2, Valid: true}
}

func TestGroupSubscriptionProgressViewsGroupsBySubscriptionAndKeepsRowOrder(t *testing.T) {
	seriesID := uuid.MustParse("20000000-0000-0000-0000-000000000001")
	subscriptionA := uuid.MustParse("20000000-0000-0000-0000-000000000002")
	subscriptionB := uuid.MustParse("20000000-0000-0000-0000-000000000003")
	now := time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC)
	rows := []db.ListRSSSubscriptionAcquisitionsBySubscriptionIDsRow{
		{ID: pgUUID(uuid.MustParse("20000000-0000-0000-0000-000000000010")), SeriesID: pgUUID(seriesID), SourceKind: "rss", CreatedAt: pgTimestamp(now), UpdatedAt: pgTimestamp(now), SubscriptionID: pgUUID(subscriptionA)},
		{ID: pgUUID(uuid.MustParse("20000000-0000-0000-0000-000000000011")), SeriesID: pgUUID(seriesID), SourceKind: "rss", CreatedAt: pgTimestamp(now), UpdatedAt: pgTimestamp(now), SubscriptionID: pgUUID(subscriptionA)},
		{ID: pgUUID(uuid.MustParse("20000000-0000-0000-0000-000000000012")), SeriesID: pgUUID(seriesID), SourceKind: "rss", CreatedAt: pgTimestamp(now), UpdatedAt: pgTimestamp(now), SubscriptionID: pgUUID(subscriptionB)},
	}
	mappingByAcquisition := map[uuid.UUID]db.GetAcquisitionMappingCompletenessByAcquisitionIDsRow{
		uuid.MustParse("20000000-0000-0000-0000-000000000010"): {SelectedVideoCount: 1, MappedVideoCount: 1},
		uuid.MustParse("20000000-0000-0000-0000-000000000011"): {SelectedVideoCount: 1, MappedVideoCount: 0},
		uuid.MustParse("20000000-0000-0000-0000-000000000012"): {SelectedVideoCount: 0, MappedVideoCount: 0},
	}

	views := groupSubscriptionProgressViews(rows, nil, nil, nil, mappingByAcquisition)

	if len(views[subscriptionA]) != 2 || len(views[subscriptionB]) != 1 {
		t.Fatalf("group sizes = %d/%d, want 2/1", len(views[subscriptionA]), len(views[subscriptionB]))
	}
	if views[subscriptionA][0].ID != uuid.MustParse("20000000-0000-0000-0000-000000000010") ||
		views[subscriptionA][1].ID != uuid.MustParse("20000000-0000-0000-0000-000000000011") {
		t.Fatalf("subscription A order = %s, %s", views[subscriptionA][0].ID, views[subscriptionA][1].ID)
	}
	if views[subscriptionA][0].Mapping.Complete != true || views[subscriptionA][1].Mapping.Complete {
		t.Fatalf("mapping completeness = %#v / %#v, want true/false", views[subscriptionA][0].Mapping, views[subscriptionA][1].Mapping)
	}
}

func TestAcquisitionProgressViewDerivesFailedAttentionFromDownload(t *testing.T) {
	seriesID := uuid.MustParse("20000000-0000-0000-0000-000000000001")
	acquisitionID := uuid.MustParse("20000000-0000-0000-0000-000000000010")
	downloadID := uuid.MustParse("20000000-0000-0000-0000-000000000020")
	subscriptionID := uuid.MustParse("20000000-0000-0000-0000-000000000002")
	now := time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC)
	row := db.ListRSSSubscriptionAcquisitionsBySubscriptionIDsRow{
		ID: pgUUID(acquisitionID), SeriesID: pgUUID(seriesID), SourceKind: "rss",
		CreatedAt: pgTimestamp(now.Add(-time.Hour)), UpdatedAt: pgTimestamp(now), SubscriptionID: pgUUID(subscriptionID),
	}
	media := db.MediaSeries{ID: pgUUID(seriesID), MediaType: "tv"}
	download := db.Download{
		ID: pgUUID(downloadID), AcquisitionID: pgUUID(acquisitionID), Attempt: 1, Status: "failed",
		Progress: numeric(0), ErrorCode: ptr("qbittorrent_unavailable"), ErrorMessage: ptr("connection refused"),
		UpdatedAt: pgTimestamp(now),
	}
	mapping := db.GetAcquisitionMappingCompletenessByAcquisitionIDsRow{ID: pgUUID(acquisitionID), SelectedVideoCount: 0, MappedVideoCount: 0}

	views := groupSubscriptionProgressViews(
		[]db.ListRSSSubscriptionAcquisitionsBySubscriptionIDsRow{row},
		map[uuid.UUID]db.MediaSeries{seriesID: media},
		map[uuid.UUID]db.Download{acquisitionID: download},
		nil,
		map[uuid.UUID]db.GetAcquisitionMappingCompletenessByAcquisitionIDsRow{acquisitionID: mapping},
	)

	view := views[subscriptionID][0]
	if view.AggregateStatus != "failed" {
		t.Fatalf("aggregate status = %q, want failed", view.AggregateStatus)
	}
	item, ok := dashboardAttentionItem(view)
	if !ok || item.Reason != "workflow_failed" || item.ErrorCode != "qbittorrent_unavailable" || item.ErrorMessage != "connection refused" {
		t.Fatalf("attention item = %#v ok=%v, want workflow_failed with download error", item, ok)
	}
	if view.Download == nil || view.Download.Status != "failed" || view.Download.Attempt != 1 || view.Download.Progress != 0 {
		t.Fatalf("download summary = %#v", view.Download)
	}
}

func TestAcquisitionProgressViewMapsTaskAndMovieFields(t *testing.T) {
	seriesID := uuid.MustParse("20000000-0000-0000-0000-000000000001")
	acquisitionID := uuid.MustParse("20000000-0000-0000-0000-000000000010")
	downloadID := uuid.MustParse("20000000-0000-0000-0000-000000000020")
	taskID := uuid.MustParse("20000000-0000-0000-0000-000000000030")
	subscriptionID := uuid.MustParse("20000000-0000-0000-0000-000000000002")
	now := time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC)
	row := db.ListRSSSubscriptionAcquisitionsBySubscriptionIDsRow{
		ID: pgUUID(acquisitionID), SeriesID: pgUUID(seriesID), SourceKind: "rss",
		CreatedAt: pgTimestamp(now.Add(-time.Hour)), UpdatedAt: pgTimestamp(now), SubscriptionID: pgUUID(subscriptionID),
	}
	media := db.MediaSeries{ID: pgUUID(seriesID), MediaType: "movie"}
	download := db.Download{
		ID: pgUUID(downloadID), AcquisitionID: pgUUID(acquisitionID), Attempt: 1, Status: "completed",
		Progress: numeric(1), UpdatedAt: pgTimestamp(now),
	}
	sourceSeason, sourceEpisode := int32(1), int32(4)
	targetSeason, targetEpisode := int32(1), int32(4)
	reviewedAt := now
	task := db.ListAcquisitionTaskSummariesByAcquisitionIDsRow{
		AcquisitionID: pgUUID(acquisitionID), ID: pgUUID(taskID), MediaType: "episode", DownloadID: pgUUID(downloadID),
		SourceSeason: &sourceSeason, SourceEpisode: &sourceEpisode,
		TargetSeason: &targetSeason, TargetEpisode: &targetEpisode, TargetEpisodeTitle: ptr("Four"),
		State: "imported", VideoState: "video_ready", SubtitleState: "ass_ready",
		ArtifactBasename: ptr("Show - S01E04 - Four"), ReviewDecision: ptr("approved"), ReviewedAt: pgTimestamp(reviewedAt),
		ImportStatus: "succeeded", EmbyRefreshStatus: "refreshed", CleanupStatus: "cleaned",
		UpdatedAt: pgTimestamp(now),
	}
	mapping := db.GetAcquisitionMappingCompletenessByAcquisitionIDsRow{ID: pgUUID(acquisitionID), SelectedVideoCount: 1, MappedVideoCount: 1}

	views := groupSubscriptionProgressViews(
		[]db.ListRSSSubscriptionAcquisitionsBySubscriptionIDsRow{row},
		map[uuid.UUID]db.MediaSeries{seriesID: media},
		map[uuid.UUID]db.Download{acquisitionID: download},
		map[uuid.UUID][]db.ListAcquisitionTaskSummariesByAcquisitionIDsRow{acquisitionID: {task}},
		map[uuid.UUID]db.GetAcquisitionMappingCompletenessByAcquisitionIDsRow{acquisitionID: mapping},
	)

	view := views[subscriptionID][0]
	if view.MediaType != domain.TaskMediaMovie {
		t.Fatalf("media type = %q, want movie", view.MediaType)
	}
	if len(view.Tasks) != 1 {
		t.Fatalf("task count = %d, want 1", len(view.Tasks))
	}
	summary := view.Tasks[0]
	if summary.ID != taskID || summary.SourceSeason != 1 || summary.SourceEpisode != 4 ||
		summary.TargetSeason == nil || *summary.TargetSeason != 1 || summary.TargetEpisodeTitle != "Four" ||
		summary.ArtifactBasename != "Show - S01E04 - Four" || summary.ImportStatus != "succeeded" ||
		summary.CleanupStatus != "cleaned" || summary.EmbyRefreshStatus != "refreshed" ||
		summary.ReviewedAt == nil || !summary.ReviewedAt.Equal(reviewedAt) {
		t.Fatalf("task summary = %#v", summary)
	}
	if view.Mapping.Complete != true {
		t.Fatalf("mapping = %#v, want complete", view.Mapping)
	}
	if view.AggregateStatus != "completed" {
		t.Fatalf("aggregate status = %q, want completed", view.AggregateStatus)
	}
}

func TestAcquisitionProgressViewWithoutDownloadStaysPending(t *testing.T) {
	seriesID := uuid.MustParse("20000000-0000-0000-0000-000000000001")
	acquisitionID := uuid.MustParse("20000000-0000-0000-0000-000000000010")
	subscriptionID := uuid.MustParse("20000000-0000-0000-0000-000000000002")
	now := time.Date(2026, time.August, 15, 10, 0, 0, 0, time.UTC)
	row := db.ListRSSSubscriptionAcquisitionsBySubscriptionIDsRow{
		ID: pgUUID(acquisitionID), SeriesID: pgUUID(seriesID), SourceKind: "rss",
		CreatedAt: pgTimestamp(now), UpdatedAt: pgTimestamp(now), SubscriptionID: pgUUID(subscriptionID),
	}
	media := db.MediaSeries{ID: pgUUID(seriesID), MediaType: "tv"}
	mapping := db.GetAcquisitionMappingCompletenessByAcquisitionIDsRow{ID: pgUUID(acquisitionID), SelectedVideoCount: 0, MappedVideoCount: 0}

	views := groupSubscriptionProgressViews(
		[]db.ListRSSSubscriptionAcquisitionsBySubscriptionIDsRow{row},
		map[uuid.UUID]db.MediaSeries{seriesID: media},
		nil, nil,
		map[uuid.UUID]db.GetAcquisitionMappingCompletenessByAcquisitionIDsRow{acquisitionID: mapping},
	)

	view := views[subscriptionID][0]
	if view.Download != nil {
		t.Fatalf("download summary = %#v, want nil", view.Download)
	}
	if view.AggregateStatus != "pending" {
		t.Fatalf("aggregate status = %q, want pending", view.AggregateStatus)
	}
	if _, ok := dashboardAttentionItem(view); ok {
		t.Fatalf("pending acquisition must not need attention")
	}
}

func ptr[T any](value T) *T {
	return &value
}
