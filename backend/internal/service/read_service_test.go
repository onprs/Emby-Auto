package service

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestRSSEntryGroupsHidePendingBackgroundAdjudication(t *testing.T) {
	pending := domain.RSSEntryView{Classification: "pending", AdjudicationState: "pending"}
	if rssEntryMatchesGroup(pending, "confirmed") || rssEntryMatchesGroup(pending, "skipped") {
		t.Fatal("pending adjudication must not appear in confirmed or skipped user groups")
	}
	if !rssEntryMatchesGroup(pending, "all") {
		t.Fatal("pending adjudication must remain available to ungrouped internal reads")
	}

	deterministicSkip := domain.RSSEntryView{Classification: "rejected", AdjudicationState: "pending"}
	if rssEntryMatchesGroup(deterministicSkip, "confirmed") || !rssEntryMatchesGroup(deterministicSkip, "skipped") {
		t.Fatal("deterministic target occupancy must override a stale pending adjudication group")
	}
}

func TestClassifyRSSEntryTargetOccupancyOverridesHistoricalEnqueueStatus(t *testing.T) {
	catalogSource := rssFulfillmentEmbyCatalog
	managedSource := rssFulfillmentManagedImport
	tests := []struct {
		name string
		row  db.ListRSSEntriesRow
		want string
	}{
		{
			name: "catalog fulfillment",
			row: db.ListRSSEntriesRow{
				Status: "enqueued", Downloadable: false, FulfillmentSource: &catalogSource,
				RejectionReasons: []string{rssTargetInLibraryReason},
			},
			want: "rejected",
		},
		{
			name: "managed import occupied alternate",
			row: db.ListRSSEntriesRow{
				Status: "enqueued", Downloadable: false, FulfillmentSource: &managedSource,
				RejectionReasons: []string{rssTargetImportedReason},
			},
			want: "rejected",
		},
		{
			name: "processing occupied alternate",
			row: db.ListRSSEntriesRow{
				Status: "enqueued", Downloadable: false,
				RejectionReasons: []string{rssTargetProcessingReason},
			},
			want: "rejected",
		},
		{
			name: "catalog occupancy overrides pending adjudication",
			row: db.ListRSSEntriesRow{
				Status: "enqueued", Downloadable: false, FulfillmentSource: &catalogSource,
				RejectionReasons: []string{rssTargetInLibraryReason}, AdjudicationState: "pending",
			},
			want: "rejected",
		},
		{
			name: "successful managed import owner keeps workflow history",
			row: db.ListRSSEntriesRow{
				Status: "enqueued", Downloadable: true, FulfillmentSource: &managedSource,
			},
			want: "enqueued",
		},
		{
			name: "managed import owner keeps completion despite stale occupancy rejection",
			row: db.ListRSSEntriesRow{
				Status: "enqueued", Downloadable: false, FulfillmentSource: &managedSource,
				ImportedAt:       pgtype.Timestamptz{Time: time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC), Valid: true},
				RejectionReasons: []string{rssTargetInLibraryReason},
			},
			want: "enqueued",
		},
		{
			name: "occupied alternate with fulfillment marker stays skipped",
			row: db.ListRSSEntriesRow{
				Status: "discovered", Downloadable: false, FulfillmentSource: &managedSource,
				ImportedAt:       pgtype.Timestamptz{Time: time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC), Valid: true},
				RejectionReasons: []string{rssTargetImportedReason},
			},
			want: "rejected",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := classifyRSSEntry(testCase.row); got != testCase.want {
				t.Fatalf("classifyRSSEntry() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestAcquisitionStageMatches(t *testing.T) {
	cases := []struct {
		name  string
		phase string
		view  domain.AcquisitionView
		want  bool
	}{
		{name: "pending download", phase: "downloading", view: domain.AcquisitionView{AggregateStatus: "pending"}, want: true},
		{name: "active download", phase: "downloading", view: domain.AcquisitionView{AggregateStatus: "downloading"}, want: true},
		{name: "materializing is not download", phase: "downloading", view: domain.AcquisitionView{AggregateStatus: "materializing"}},
		{name: "materializing", phase: "processing", view: domain.AcquisitionView{AggregateStatus: "materializing"}, want: true},
		{name: "processing", phase: "processing", view: domain.AcquisitionView{AggregateStatus: "processing"}, want: true},
		{name: "download is not processing", phase: "processing", view: domain.AcquisitionView{AggregateStatus: "downloading"}},
		{name: "awaiting review", phase: "awaiting_review", view: domain.AcquisitionView{AggregateStatus: "awaiting_review"}, want: true},
		{name: "processing is not review", phase: "awaiting_review", view: domain.AcquisitionView{AggregateStatus: "processing"}},
		{name: "importing", phase: "importing", view: domain.AcquisitionView{AggregateStatus: "importing"}, want: true},
		{name: "completed is not importing", phase: "importing", view: domain.AcquisitionView{AggregateStatus: "completed"}},
		{name: "completed", phase: "completed", view: domain.AcquisitionView{AggregateStatus: "completed"}, want: true},
		{name: "mapping attention", phase: "attention", view: domain.AcquisitionView{AggregateStatus: "mapping_pending"}, want: true},
		{name: "workflow failure attention", phase: "attention", view: domain.AcquisitionView{AggregateStatus: "failed"}, want: true},
		{name: "cleanup failure attention", phase: "attention", view: domain.AcquisitionView{AggregateStatus: "completed", Tasks: []domain.AcquisitionTaskSummary{{CleanupStatus: "failed"}}}, want: true},
		{name: "Emby refresh failure attention", phase: "attention", view: domain.AcquisitionView{AggregateStatus: "completed", Tasks: []domain.AcquisitionTaskSummary{{EmbyRefreshStatus: "failed"}}}, want: true},
		{name: "rejected attention", phase: "attention", view: domain.AcquisitionView{AggregateStatus: "rejected"}, want: true},
		{name: "cancelled is not attention", phase: "attention", view: domain.AcquisitionView{AggregateStatus: "cancelled"}},
		{name: "unknown phase", phase: "unknown", view: domain.AcquisitionView{AggregateStatus: "failed"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := acquisitionStageMatches(testCase.phase, testCase.view); got != testCase.want {
				t.Fatalf("acquisitionStageMatches(%q, %#v) = %v, want %v", testCase.phase, testCase.view, got, testCase.want)
			}
		})
	}
}

func TestDashboardAttentionItemUsesBusinessStateInsteadOfOperations(t *testing.T) {
	now := time.Date(2026, time.July, 26, 7, 45, 0, 0, time.UTC)
	cases := []struct {
		name       string
		view       domain.AcquisitionView
		wantReason string
		wantCode   string
		want       bool
	}{
		{
			name: "failed download",
			view: domain.AcquisitionView{
				AggregateStatus: "failed", CurrentStage: "download", UpdatedAt: now,
				Download: &domain.AcquisitionDownloadSummary{Status: "failed", ErrorCode: "qbittorrent_unavailable", ErrorMessage: "connection refused"},
			},
			wantReason: "workflow_failed", wantCode: "qbittorrent_unavailable", want: true,
		},
		{name: "mapping required", view: domain.AcquisitionView{AggregateStatus: "mapping_pending", CurrentStage: "mapping", UpdatedAt: now}, wantReason: "mapping_required", want: true},
		{name: "cleanup failed", view: domain.AcquisitionView{AggregateStatus: "completed", CurrentStage: "import", UpdatedAt: now, Tasks: []domain.AcquisitionTaskSummary{{CleanupStatus: "failed"}}}, wantReason: "cleanup_failed", want: true},
		{name: "Emby refresh failed", view: domain.AcquisitionView{AggregateStatus: "completed", CurrentStage: "import", UpdatedAt: now, Tasks: []domain.AcquisitionTaskSummary{{EmbyRefreshStatus: "failed"}}}, wantReason: "emby_refresh_failed", want: true},
		{name: "review rejected", view: domain.AcquisitionView{AggregateStatus: "rejected", CurrentStage: "review", UpdatedAt: now}, wantReason: "review_rejected", want: true},
		{name: "cancelled", view: domain.AcquisitionView{AggregateStatus: "cancelled", CurrentStage: "download", UpdatedAt: now}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			item, ok := dashboardAttentionItem(testCase.view)
			if ok != testCase.want {
				t.Fatalf("dashboardAttentionItem() ok = %v, want %v", ok, testCase.want)
			}
			if !ok {
				return
			}
			if item.Reason != testCase.wantReason || item.ErrorCode != testCase.wantCode {
				t.Fatalf("dashboardAttentionItem() = %#v", item)
			}
		})
	}
}

func TestSortAcquisitionViewsUsesDisplayedSourceAndProgressColumns(t *testing.T) {
	ids := []uuid.UUID{
		uuid.MustParse("10000000-0000-0000-0000-000000000001"),
		uuid.MustParse("10000000-0000-0000-0000-000000000002"),
		uuid.MustParse("10000000-0000-0000-0000-000000000003"),
	}
	views := []domain.AcquisitionView{
		{ID: ids[0], SourceKind: "search", OverallProgress: 0.5},
		{ID: ids[1], SourceKind: "manual", OverallProgress: 0.2},
		{ID: ids[2], SourceKind: "rss", OverallProgress: 0.8},
	}
	sortAcquisitionViews(views, "source_kind", 1)
	if views[0].ID != ids[1] || views[1].ID != ids[2] || views[2].ID != ids[0] {
		t.Fatalf("source order = %s, %s, %s", views[0].SourceKind, views[1].SourceKind, views[2].SourceKind)
	}
	sortAcquisitionViews(views, "progress", -1)
	if views[0].ID != ids[2] || views[1].ID != ids[0] || views[2].ID != ids[1] {
		t.Fatalf("progress order = %.2f, %.2f, %.2f", views[0].OverallProgress, views[1].OverallProgress, views[2].OverallProgress)
	}
	season, episodeOne, episodeTwo, episodeThree := 1, 1, 2, 3
	views = []domain.AcquisitionView{
		{ID: ids[0], MediaType: domain.TaskMediaEpisode, SeriesTitle: "Same Show", SourceSeason: &season, SourceEpisode: &episodeThree},
		{ID: ids[1], MediaType: domain.TaskMediaEpisode, SeriesTitle: "Same Show", SourceSeason: &season, SourceEpisode: &episodeOne},
		{ID: ids[2], MediaType: domain.TaskMediaEpisode, SeriesTitle: "Same Show", SourceSeason: &season, SourceEpisode: &episodeTwo},
	}
	sortAcquisitionViews(views, "content", 1)
	if views[0].ID != ids[1] || views[1].ID != ids[2] || views[2].ID != ids[0] {
		t.Fatalf("episode content order = %v, %v, %v", views[0].SourceEpisode, views[1].SourceEpisode, views[2].SourceEpisode)
	}
}

func TestDeriveAcquisitionLifecycleStartsAtFineGrainedSourceWeight(t *testing.T) {
	view := domain.AcquisitionView{MediaType: domain.TaskMediaEpisode, SourceKind: "rss", CreatedAt: time.Date(2026, time.July, 26, 1, 20, 0, 0, time.UTC)}
	deriveAcquisitionLifecycle(&view, "")
	if view.CurrentStage != "download" || view.OverallProgress != 0.02 {
		t.Fatalf("new acquisition current/progress = %q/%f, want download/0.02", view.CurrentStage, view.OverallProgress)
	}
}

func TestDeriveAcquisitionLifecycleShowsExactCurrentStageAndProgress(t *testing.T) {
	now := time.Date(2026, time.July, 26, 1, 30, 0, 0, time.UTC)
	view := domain.AcquisitionView{
		MediaType: domain.TaskMediaEpisode, SourceKind: "rss", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
		Download: &domain.AcquisitionDownloadSummary{Status: "downloading", Progress: 0.5, UpdatedAt: now},
	}
	deriveAcquisitionLifecycle(&view, "downloading")
	if view.CurrentStage != "download" || view.AggregateStatus != "downloading" {
		t.Fatalf("current/aggregate = %q/%q, want download/downloading", view.CurrentStage, view.AggregateStatus)
	}
	if view.OverallProgress != 0.16 {
		t.Fatalf("overall progress = %f, want 0.02 source plus 0.14 partial download", view.OverallProgress)
	}
	if len(view.Stages) != 9 || view.Stages[1].Key != "download" || view.Stages[1].Progress != 0.5 {
		t.Fatalf("stages = %#v", view.Stages)
	}
}

func TestDeriveAcquisitionLifecycleTreatsCompletedTransferMappingFailureAsMappingPending(t *testing.T) {
	now := time.Date(2026, time.July, 26, 5, 57, 0, 0, time.UTC)
	view := domain.AcquisitionView{
		MediaType: domain.TaskMediaEpisode, SourceKind: "rss", CreatedAt: now.Add(-time.Hour),
		Download: &domain.AcquisitionDownloadSummary{
			Status: "failed", Progress: 1, FailureStage: "materialize", ErrorCode: "mapping_profile_required", UpdatedAt: now,
		},
		Mapping: domain.AcquisitionMappingCompleteness{SelectedVideoCount: 1, MappedVideoCount: 0, Complete: false},
	}

	deriveAcquisitionLifecycle(&view, "failed")

	if view.Download.Status != "failed" || view.Download.FailureStage != "materialize" {
		t.Fatalf("diagnostic download facts changed: %#v", view.Download)
	}
	if view.CurrentStage != "mapping" || view.AggregateStatus != "mapping_pending" {
		t.Fatalf("current/aggregate = %q/%q, want mapping/mapping_pending", view.CurrentStage, view.AggregateStatus)
	}
	if acquisitionStageKeyStatus(view.Stages, "download") != stageCompleted || acquisitionStageKeyStatus(view.Stages, "mapping") != stageWaiting {
		t.Fatalf("download/mapping stages = %#v", view.Stages)
	}
	if view.OverallProgress < 0.299999 || view.OverallProgress > 0.300001 {
		t.Fatalf("overall progress = %f, want completed source and download weights", view.OverallProgress)
	}
	attention, ok := dashboardAttentionItem(view)
	if !ok || attention.Reason != "mapping_required" {
		t.Fatalf("dashboard attention = %#v/%v, want mapping_required", attention, ok)
	}
}

func TestDeriveAcquisitionLifecycleKeepsNonMappingMaterializationFailureVisible(t *testing.T) {
	now := time.Date(2026, time.July, 26, 5, 58, 0, 0, time.UTC)
	view := domain.AcquisitionView{
		MediaType: domain.TaskMediaEpisode, CreatedAt: now.Add(-time.Hour),
		Download: &domain.AcquisitionDownloadSummary{
			Status: "failed", Progress: 1, FailureStage: "materialize", ErrorCode: "download_no_main_video", UpdatedAt: now,
		},
	}

	deriveAcquisitionLifecycle(&view, "failed")

	if view.AggregateStatus != "failed" || acquisitionStageKeyStatus(view.Stages, "download") != stageCompleted {
		t.Fatalf("aggregate/download stage = %q/%q, want failed/completed", view.AggregateStatus, acquisitionStageKeyStatus(view.Stages, "download"))
	}
}

func TestDeriveAcquisitionLifecycleUsesPersistedRunningMilestones(t *testing.T) {
	now := time.Date(2026, time.July, 26, 1, 35, 0, 0, time.UTC)
	view := domain.AcquisitionView{
		MediaType: domain.TaskMediaEpisode, CreatedAt: now.Add(-time.Hour),
		Download: &domain.AcquisitionDownloadSummary{Status: "materialized", Progress: 1, UpdatedAt: now},
		Mapping:  domain.AcquisitionMappingCompleteness{SelectedVideoCount: 1, MappedVideoCount: 1, Complete: true},
		Tasks: []domain.AcquisitionTaskSummary{{
			State: "processing", VideoState: "transcoding", SubtitleState: "extracting_or_converting", UpdatedAt: now,
		}},
	}
	deriveAcquisitionLifecycle(&view, "materialized")
	if view.Stages[3].Progress != 0.5 || view.Stages[4].Progress != 0.5 {
		t.Fatalf("active branch progress = transcode %f subtitle %f, want 0.5/0.5", view.Stages[3].Progress, view.Stages[4].Progress)
	}
	if view.OverallProgress <= 0.5 || view.OverallProgress >= 0.6 {
		t.Fatalf("overall progress = %f, want weighted active branch milestones", view.OverallProgress)
	}
}

func TestDeriveAcquisitionLifecycleWaitsForReviewAfterCanonicalArtifacts(t *testing.T) {
	now := time.Date(2026, time.July, 26, 1, 40, 0, 0, time.UTC)
	view := domain.AcquisitionView{
		MediaType: domain.TaskMediaEpisode, CreatedAt: now.Add(-time.Hour),
		Download: &domain.AcquisitionDownloadSummary{Status: "materialized", Progress: 1, UpdatedAt: now},
		Mapping:  domain.AcquisitionMappingCompleteness{SelectedVideoCount: 1, MappedVideoCount: 1, Complete: true},
		Tasks: []domain.AcquisitionTaskSummary{{
			State: "awaiting_review", VideoState: "video_ready", SubtitleState: "ass_ready",
			ArtifactBasename: "Show - S01E01 - Pilot", UpdatedAt: now,
		}},
	}
	deriveAcquisitionLifecycle(&view, "materialized")
	if view.CurrentStage != "review" || view.AggregateStatus != "awaiting_review" {
		t.Fatalf("current/aggregate = %q/%q, want review/awaiting_review", view.CurrentStage, view.AggregateStatus)
	}
	for _, key := range []string{"transcode", "subtitle", "rename", "organize"} {
		if got := acquisitionStageKeyStatus(view.Stages, key); got != stageCompleted {
			t.Fatalf("stage %s = %q, want completed", key, got)
		}
	}
}

func TestDeriveAcquisitionLifecycleTreatsApprovedImportAsAutomaticAndRejectedAsNotImported(t *testing.T) {
	now := time.Date(2026, time.July, 26, 1, 50, 0, 0, time.UTC)
	base := domain.AcquisitionView{
		MediaType: domain.TaskMediaEpisode, CreatedAt: now.Add(-time.Hour),
		Download: &domain.AcquisitionDownloadSummary{Status: "materialized", Progress: 1, UpdatedAt: now},
		Mapping:  domain.AcquisitionMappingCompleteness{SelectedVideoCount: 1, MappedVideoCount: 1, Complete: true},
	}
	approved := base
	approved.Tasks = []domain.AcquisitionTaskSummary{{
		State: "import_queued", VideoState: "video_ready", SubtitleState: "ass_ready", ArtifactBasename: "Show - S01E01 - Pilot",
		ReviewDecision: "approved", ImportStatus: "queued", UpdatedAt: now,
	}}
	deriveAcquisitionLifecycle(&approved, "materialized")
	if approved.CurrentStage != "import" || approved.AggregateStatus != "importing" {
		t.Fatalf("approved current/aggregate = %q/%q, want import/importing", approved.CurrentStage, approved.AggregateStatus)
	}
	if approved.Stages[8].Progress != 0.25 || approved.OverallProgress != 0.9325 {
		t.Fatalf("queued import progress = stage %f overall %f, want 0.25/0.9325", approved.Stages[8].Progress, approved.OverallProgress)
	}

	rejected := base
	rejected.Tasks = []domain.AcquisitionTaskSummary{{
		State: "rejected", VideoState: "video_ready", SubtitleState: "ass_ready", ArtifactBasename: "Show - S01E01 - Pilot",
		ReviewDecision: "rejected", UpdatedAt: now,
	}}
	deriveAcquisitionLifecycle(&rejected, "materialized")
	if rejected.CurrentStage != "review" || rejected.AggregateStatus != "rejected" {
		t.Fatalf("rejected current/aggregate = %q/%q, want review/rejected", rejected.CurrentStage, rejected.AggregateStatus)
	}
}

func TestDeriveAcquisitionLifecycleCompletesOnlyAfterVerifiedImport(t *testing.T) {
	now := time.Date(2026, time.July, 26, 2, 0, 0, 0, time.UTC)
	view := domain.AcquisitionView{
		MediaType: domain.TaskMediaMovie, CreatedAt: now.Add(-time.Hour),
		Download: &domain.AcquisitionDownloadSummary{Status: "materialized", Progress: 1, UpdatedAt: now},
		Mapping:  domain.AcquisitionMappingCompleteness{SelectedVideoCount: 1, MappedVideoCount: 1, Complete: true},
		Tasks: []domain.AcquisitionTaskSummary{{
			State: "imported", VideoState: "video_ready", SubtitleState: "ass_ready", ArtifactBasename: "Movie(2026)",
			ReviewDecision: "approved", ImportStatus: "succeeded", DestinationVideoPath: "/library/Movie(2026)/Movie(2026).mkv",
			DestinationSubtitlePath: "/library/Movie(2026)/Movie(2026).ass", EmbyRefreshStatus: "succeeded", UpdatedAt: now,
		}},
	}
	deriveAcquisitionLifecycle(&view, "materialized")
	if view.AggregateStatus != "completed" || view.OverallProgress != 1 || acquisitionStageKeyStatus(view.Stages, "import") != stageCompleted {
		t.Fatalf("completed lifecycle = status %q progress %f stages %#v", view.AggregateStatus, view.OverallProgress, view.Stages)
	}
}

func TestResourceHrefMapsPersistedOperationResourceTypes(t *testing.T) {
	id := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	cases := []struct {
		resourceType string
		want         string
	}{
		{resourceType: "acquisition", want: "/acquisitions/" + id.String()},
		{resourceType: "download", want: "/downloads/" + id.String()},
		{resourceType: "episode_task", want: "/tasks/" + id.String()},
		{resourceType: "rss_subscription", want: "/rss/" + id.String()},
		{resourceType: "search_run", want: "/searches/" + id.String()},
		{resourceType: "emby_scan", want: "/emby/scans/" + id.String()},
		{resourceType: "emby_catalog", want: "/emby"},
		{resourceType: "media_series", want: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.resourceType, func(t *testing.T) {
			if got := resourceHref(testCase.resourceType, id); got != testCase.want {
				t.Fatalf("resourceHref(%q) = %q, want %q", testCase.resourceType, got, testCase.want)
			}
		})
	}
}
