package service

import (
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestRSSReleaseAnalysisWithOccupancyAddsDeterministicHardRejection(t *testing.T) {
	base := domain.RSSReleaseAnalysis{
		Downloadable:  true,
		SourceSeason:  1,
		SourceEpisode: 16,
	}

	got := rssReleaseAnalysisWithOccupancy(base, rssTargetOccupancy{Reason: rssTargetInLibraryReason, Fulfilled: true})
	if got.Downloadable {
		t.Fatal("occupied release remained downloadable")
	}
	if len(got.RejectionReasons) != 1 || got.RejectionReasons[0] != rssTargetInLibraryReason {
		t.Fatalf("rejection reasons = %v, want target library reason", got.RejectionReasons)
	}
	if len(base.RejectionReasons) != 0 {
		t.Fatalf("input analysis was mutated: %v", base.RejectionReasons)
	}
}

func TestRSSReleaseAnalysisWithOccupancyPreservesExistingHardRejections(t *testing.T) {
	base := domain.RSSReleaseAnalysis{
		RejectionReasons: []string{"title_excluded"},
		SourceSeason:     1,
		SourceEpisode:    17,
	}

	got := rssReleaseAnalysisWithOccupancy(base, rssTargetOccupancy{Reason: rssTargetProcessingReason})
	if got.Downloadable || len(got.RejectionReasons) != 2 || got.RejectionReasons[0] != "title_excluded" || got.RejectionReasons[1] != rssTargetProcessingReason {
		t.Fatalf("analysis = %#v, want both deterministic hard rejections", got)
	}

	replayed := rssReleaseAnalysisWithOccupancy(got, rssTargetOccupancy{Reason: rssTargetProcessingReason})
	if len(replayed.RejectionReasons) != 2 {
		t.Fatalf("replayed reasons = %v, want no duplicate", replayed.RejectionReasons)
	}
}
