package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestRSSAdjudicationCannotSelectFractionalEpisodeAsRegularEpisode(t *testing.T) {
	entryID := uuid.New()
	season, episode := 1, 12
	for _, storedReason := range []bool{false, true} {
		entry := scopedRSSReleaseEntry{EntryID: entryID}
		if storedReason {
			entry.RejectionReasons = []string{"fractional_episode_requires_explicit_mapping"}
		} else {
			entry.Deterministic.RejectionReasons = []string{"fractional_episode_requires_explicit_mapping"}
		}
		validation := validateRSSReleaseAdjudicationProposal(domain.AgentRSSReleaseAdjudicationProposal{
			ScopedEntryIDs: []uuid.UUID{entryID}, Decision: "resolved",
			Entries: []domain.AgentRSSReleaseDisposition{{EntryID: entryID, Disposition: "select", SourceSeason: &season, SourceEpisode: &episode, EvidenceCodes: []string{"source_coordinate"}}},
		}, agentContextSnapshot{RSSAdjudicationEntries: map[uuid.UUID]scopedRSSReleaseEntry{entryID: entry}})
		if validation.Verdict != domain.AgentValidationInvalid || len(validation.ReasonCodes) != 1 || validation.ReasonCodes[0] != "rss_adjudication_hard_rejection" {
			t.Fatalf("小数集整数裁决=%+v，期望硬拒绝", validation)
		}
	}
}
