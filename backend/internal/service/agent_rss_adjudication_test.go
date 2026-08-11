package service

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/agentapi"
)

func TestAutomaticAgentResolutionRetryableIsBounded(t *testing.T) {
	legacy := domain.AgentResolution{
		Trigger: "automatic", Status: domain.AgentResolutionFailed,
		ErrorCode: "agent_submission_invalid", Proposal: []byte(`{}`),
	}
	if !AutomaticAgentResolutionRetryable(legacy) {
		t.Fatal("legacy empty submission failure is not retryable")
	}
	proposed := legacy
	proposed.ErrorCode = "apply_failed"
	proposed.Proposal = []byte(`{"decision":"resolved"}`)
	if !AutomaticAgentResolutionRetryable(proposed) {
		t.Fatal("failed automatic proposal is not retryable")
	}
	newExhausted := legacy
	newExhausted.ErrorCode = "agent_submission_exhausted"
	if AutomaticAgentResolutionRetryable(newExhausted) {
		t.Fatal("new exhausted failure would create an unbounded reconciliation loop")
	}
	manual := legacy
	manual.Trigger = "user"
	if AutomaticAgentResolutionRetryable(manual) {
		t.Fatal("manual failure is retryable through the automatic path")
	}
}

func TestAgentRunErrorPreservesRetryableOutputClassification(t *testing.T) {
	err := agentRunError(&agentapi.Error{Code: "agent_submission_exhausted", Retryable: true})
	var runErr *AgentRunError
	if !errors.As(err, &runErr) || runErr.Code != "agent_submission_exhausted" || !runErr.Retryable {
		t.Fatalf("agentRunError() = %#v, want retryable submission exhaustion", err)
	}
}

func TestAgentToolStepBudgetScalesOnlyForRSSAdjudication(t *testing.T) {
	entries := make(map[uuid.UUID]scopedRSSReleaseEntry, 13)
	for index := 0; index < 13; index++ {
		id := uuid.New()
		entries[id] = scopedRSSReleaseEntry{EntryID: id}
	}
	snapshot := agentContextSnapshot{RSSAdjudicationEntries: entries}
	if got := agentToolStepBudget(domain.AgentCapabilityRSSReleaseAdjudication, snapshot); got != 19 {
		t.Fatalf("RSS budget = %d, want 19", got)
	}
	if got := agentToolStepBudget(domain.AgentCapabilityRSSCoordinate, snapshot); got != 6 {
		t.Fatalf("coordinate budget = %d, want 6", got)
	}
	for len(entries) < 100 {
		id := uuid.New()
		entries[id] = scopedRSSReleaseEntry{EntryID: id}
	}
	if got := agentToolStepBudget(domain.AgentCapabilityRSSReleaseAdjudication, snapshot); got != 64 {
		t.Fatalf("capped RSS budget = %d, want 64", got)
	}
}

func TestValidateRSSReleaseAdjudicationProposalUsesScopedEntriesWithoutTitleRules(t *testing.T) {
	first := uuid.MustParse("71000000-0000-4000-8000-000000000001")
	second := uuid.MustParse("71000000-0000-4000-8000-000000000002")
	season, episode := 1, 1
	snapshot := agentContextSnapshot{
		RSSAdjudicationEntries: map[uuid.UUID]scopedRSSReleaseEntry{
			first:  {EntryID: first, Title: "Arbitrary release alpha"},
			second: {EntryID: second, Title: "Completely different corrected release"},
		},
		RSSAdjudicationHistory: map[uuid.UUID]scopedRSSReleaseHistory{},
	}
	proposal := domain.AgentRSSReleaseAdjudicationProposal{
		BatchID: uuid.New(), ScopedEntryIDs: []uuid.UUID{first, second}, Decision: "resolved",
		Entries: []domain.AgentRSSReleaseDisposition{
			{EntryID: first, Disposition: "ignore", RelatedEntryID: &second, EvidenceCodes: []string{"superseded_release"}},
			{EntryID: second, Disposition: "select", SourceSeason: &season, SourceEpisode: &episode, EvidenceCodes: []string{"preferred_release"}},
		},
	}
	validation := validateRSSReleaseAdjudicationProposal(proposal, snapshot)
	if validation.Verdict != domain.AgentValidationAutoApplicable || len(validation.ReasonCodes) != 0 {
		t.Fatalf("validation = %#v, want auto applicable", validation)
	}
}

func TestValidateRSSReleaseAdjudicationProposalRejectsScopeAndCoordinateViolations(t *testing.T) {
	first := uuid.MustParse("71000000-0000-4000-8000-000000000011")
	second := uuid.MustParse("71000000-0000-4000-8000-000000000012")
	forged := uuid.MustParse("71000000-0000-4000-8000-000000000099")
	season, episode := 1, 3
	snapshot := agentContextSnapshot{
		RSSAdjudicationEntries: map[uuid.UUID]scopedRSSReleaseEntry{
			first:  {EntryID: first, Deterministic: domain.RSSReleaseAnalysis{SourceSeason: season, SourceEpisode: episode, Downloadable: true}},
			second: {EntryID: second, Deterministic: domain.RSSReleaseAnalysis{SourceSeason: season, SourceEpisode: episode, Downloadable: true}},
		},
		RSSAdjudicationHistory: map[uuid.UUID]scopedRSSReleaseHistory{},
	}
	tests := []struct {
		name     string
		proposal domain.AgentRSSReleaseAdjudicationProposal
		code     string
	}{
		{
			name: "missing scoped entry",
			proposal: domain.AgentRSSReleaseAdjudicationProposal{
				ScopedEntryIDs: []uuid.UUID{first}, Entries: []domain.AgentRSSReleaseDisposition{{EntryID: first, Disposition: "ignore"}}, Decision: "resolved",
			},
			code: "rss_adjudication_scope_incomplete",
		},
		{
			name: "forged relation",
			proposal: domain.AgentRSSReleaseAdjudicationProposal{
				ScopedEntryIDs: []uuid.UUID{first, second}, Decision: "resolved",
				Entries: []domain.AgentRSSReleaseDisposition{
					{EntryID: first, Disposition: "ignore", RelatedEntryID: &forged, EvidenceCodes: []string{"related_release"}},
					{EntryID: second, Disposition: "select", SourceSeason: &season, SourceEpisode: &episode, EvidenceCodes: []string{"preferred_release"}},
				},
			},
			code: "agent_tool_scope_violation",
		},
		{
			name: "duplicate coordinate left unresolved",
			proposal: domain.AgentRSSReleaseAdjudicationProposal{
				ScopedEntryIDs: []uuid.UUID{first, second}, Decision: "resolved",
				Entries: []domain.AgentRSSReleaseDisposition{
					{EntryID: first, Disposition: "ignore", EvidenceCodes: []string{"candidate_one"}},
					{EntryID: second, Disposition: "ignore", EvidenceCodes: []string{"candidate_two"}},
				},
			},
			code: "rss_adjudication_duplicate_coordinate_unresolved",
		},
		{
			name: "duplicate selected coordinate",
			proposal: domain.AgentRSSReleaseAdjudicationProposal{
				ScopedEntryIDs: []uuid.UUID{first, second}, Decision: "resolved",
				Entries: []domain.AgentRSSReleaseDisposition{
					{EntryID: first, Disposition: "select", SourceSeason: &season, SourceEpisode: &episode, EvidenceCodes: []string{"candidate_one"}},
					{EntryID: second, Disposition: "select", SourceSeason: &season, SourceEpisode: &episode, EvidenceCodes: []string{"candidate_two"}},
				},
			},
			code: "rss_adjudication_coordinate_duplicate",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validation := validateRSSReleaseAdjudicationProposal(test.proposal, snapshot)
			if validation.Verdict != domain.AgentValidationInvalid || len(validation.ReasonCodes) != 1 || validation.ReasonCodes[0] != test.code {
				t.Fatalf("validation = %#v, want invalid %q", validation, test.code)
			}
		})
	}
}

func TestValidateRSSReleaseAdjudicationProposalRejectsTargetOccupancyHardConstraint(t *testing.T) {
	entryID := uuid.MustParse("71000000-0000-4000-8000-000000000031")
	season, episode := 1, 16
	snapshot := agentContextSnapshot{
		RSSAdjudicationEntries: map[uuid.UUID]scopedRSSReleaseEntry{
			entryID: {EntryID: entryID, RejectionReasons: []string{rssTargetInLibraryReason}},
		},
		RSSAdjudicationHistory: map[uuid.UUID]scopedRSSReleaseHistory{},
	}
	proposal := domain.AgentRSSReleaseAdjudicationProposal{
		ScopedEntryIDs: []uuid.UUID{entryID}, Decision: "resolved",
		Entries: []domain.AgentRSSReleaseDisposition{{
			EntryID: entryID, Disposition: "select", SourceSeason: &season, SourceEpisode: &episode,
			EvidenceCodes: []string{"provider_preference"},
		}},
	}
	validation := validateRSSReleaseAdjudicationProposal(proposal, snapshot)
	if validation.Verdict != domain.AgentValidationInvalid || len(validation.ReasonCodes) != 1 || validation.ReasonCodes[0] != "rss_adjudication_hard_rejection" {
		t.Fatalf("validation = %#v, want target occupancy hard rejection", validation)
	}
}

func TestValidateRSSReleaseAdjudicationProposalRejectsAutomaticHistoricalReplacement(t *testing.T) {
	entryID := uuid.MustParse("71000000-0000-4000-8000-000000000021")
	historyID := uuid.MustParse("71000000-0000-4000-8000-000000000022")
	season, episode := 2, 7
	snapshot := agentContextSnapshot{
		RSSAdjudicationEntries: map[uuid.UUID]scopedRSSReleaseEntry{entryID: {EntryID: entryID}},
		RSSAdjudicationHistory: map[uuid.UUID]scopedRSSReleaseHistory{
			historyID: {EntryID: historyID, SourceSeason: &season, SourceEpisode: &episode, Imported: true},
		},
	}
	proposal := domain.AgentRSSReleaseAdjudicationProposal{
		ScopedEntryIDs: []uuid.UUID{entryID}, Decision: "resolved",
		Entries: []domain.AgentRSSReleaseDisposition{{EntryID: entryID, Disposition: "select", SourceSeason: &season, SourceEpisode: &episode, RelatedEntryID: &historyID, EvidenceCodes: []string{"historical_coordinate"}}},
	}
	validation := validateRSSReleaseAdjudicationProposal(proposal, snapshot)
	if validation.Verdict != domain.AgentValidationInvalid || len(validation.ReasonCodes) != 1 || validation.ReasonCodes[0] != "rss_adjudication_historical_coordinate_conflict" {
		t.Fatalf("validation = %#v, want invalid historical coordinate conflict", validation)
	}
}
