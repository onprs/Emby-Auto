package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type rssPreacquisitionMappingAgentStub struct {
	preview            []domain.EpisodeMappingRow
	err                error
	deterministic      bool
	deterministicCalls int
}

func (stub *rssPreacquisitionMappingAgentStub) AutomaticRSSPreacquisitionMappingEnabled(context.Context, uuid.UUID) (bool, error) {
	return true, nil
}

func (stub *rssPreacquisitionMappingAgentStub) TryDeterministicRSSPreacquisitionMapping(context.Context, uuid.UUID) (bool, error) {
	stub.deterministicCalls++
	return stub.deterministic, nil
}

func (stub *rssPreacquisitionMappingAgentStub) PreviewRSSPreacquisitionMapping(context.Context, uuid.UUID, domain.EpisodeCoordinate, domain.EpisodeCoordinate) ([]domain.EpisodeMappingRow, error) {
	return stub.preview, stub.err
}

func (stub *rssPreacquisitionMappingAgentStub) ApplyAgentRSSPreacquisitionMapping(context.Context, domain.AgentResolution, domain.AgentRSSPreacquisitionMappingProposal, domain.AgentProposalValidation) error {
	return nil
}

func TestAgentSubmissionValidatorAllowsAutomaticProposalAndCatalogConfirmation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := &AgentResolutionService{}
	resourceID := uuid.MustParse("72000000-0000-4000-8000-000000000001")

	resolvedCoordinate, err := json.Marshal(domain.AgentRSSCoordinateProposal{
		EntryID: resourceID, SourceSeason: 1, SourceEpisode: 2, Decision: "resolved",
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinateResolution := domain.AgentResolution{
		Capability: domain.AgentCapabilityRSSCoordinate, ResourceID: resourceID,
	}
	if err := service.agentSubmissionValidator(ctx, coordinateResolution, agentContextSnapshot{})(resolvedCoordinate); err != nil {
		t.Fatalf("resolved automatic proposal rejected: %v", err)
	}

	catalogProposal, err := json.Marshal(domain.AgentCatalogCandidateProposal{
		Query: "Canonical Show", CandidateIDs: []int64{101}, Decision: "resolved",
	})
	if err != nil {
		t.Fatal(err)
	}
	catalogResolution := domain.AgentResolution{Capability: domain.AgentCapabilityCatalogCandidate}
	catalogSnapshot := agentContextSnapshot{AllowedCatalogQueries: map[string]map[int64]struct{}{
		"canonical show": {101: {}},
	}}
	if err := service.agentSubmissionValidator(ctx, catalogResolution, catalogSnapshot)(catalogProposal); err != nil {
		t.Fatalf("catalog confirmation proposal rejected: %v", err)
	}
}

func TestAgentDownloadFileProposalPreservesFractionalCoordinates(t *testing.T) {
	fractionalID, integerID, unselectedID := uuid.New(), uuid.New(), uuid.New()
	proposal := domain.AgentDownloadFileResolutionProposal{
		Videos: []domain.AgentDownloadVideoProposal{
			{FileID: fractionalID, SourceSeason: 1, SourceEpisode: 12, SourceEpisodeFractionHundredths: 50},
			{FileID: integerID, SourceSeason: 1, SourceEpisode: 125},
		},
		Decision: "resolved",
	}
	snapshot := agentContextSnapshot{Files: map[uuid.UUID]scopedFile{
		fractionalID: {ID: fractionalID, MediaKind: string(domain.MediaVideo)},
		integerID:    {ID: integerID, MediaKind: string(domain.MediaVideo)},
		unselectedID: {ID: unselectedID, MediaKind: string(domain.MediaSubtitle)},
	}}
	if validation := validateDownloadFileProposal(proposal, snapshot); validation.Verdict != domain.AgentValidationAutoApplicable {
		t.Fatalf("fractional proposal validation = %#v", validation)
	}

	season, episode := int32(1), int32(7)
	items, err := downloadResolutionItemsFromAgentProposal([]db.DownloadFile{
		{ID: repository.UUIDToPG(fractionalID)},
		{ID: repository.UUIDToPG(integerID)},
		{ID: repository.UUIDToPG(unselectedID), SourceSeason: &season, SourceEpisode: &episode, SourceEpisodeFractionHundredths: 25},
	}, proposal)
	if err != nil {
		t.Fatalf("downloadResolutionItemsFromAgentProposal() error = %v", err)
	}
	if items[0].SourceEpisodeFractionHundredths != 50 || items[1].SourceEpisodeFractionHundredths != 0 || items[2].SourceEpisodeFractionHundredths != 25 {
		t.Fatalf("proposal item fractions = %d/%d/%d, want 50/0/25", items[0].SourceEpisodeFractionHundredths, items[1].SourceEpisodeFractionHundredths, items[2].SourceEpisodeFractionHundredths)
	}

	duplicate := proposal
	duplicate.Videos = append([]domain.AgentDownloadVideoProposal(nil), proposal.Videos...)
	duplicate.Videos[1].SourceEpisode = 12
	duplicate.Videos[1].SourceEpisodeFractionHundredths = 50
	if validation := validateDownloadFileProposal(duplicate, snapshot); validation.Verdict != domain.AgentValidationInvalid || len(validation.ReasonCodes) != 1 || validation.ReasonCodes[0] != "download_coordinate_duplicate" {
		t.Fatalf("duplicate proposal validation = %#v", validation)
	}
}

func TestAgentSubmissionValidatorRequiresScopedCompleteRSSPreacquisitionMapping(t *testing.T) {
	t.Parallel()
	scopeID := uuid.New()
	source := domain.EpisodeCoordinate{Season: 1, Episode: 13}
	target := domain.EpisodeCoordinate{Season: 2, Episode: 1}
	mapping := &rssPreacquisitionMappingAgentStub{preview: []domain.EpisodeMappingRow{{
		SourceSeason: source.Season, SourceEpisode: source.Episode, Status: domain.MappingMapped,
		TargetSeason: target.Season, TargetEpisode: target.Episode, TargetEpisodeID: uuid.New(),
	}}}
	service := &AgentResolutionService{rssMapping: mapping}
	resolution := domain.AgentResolution{Capability: domain.AgentCapabilityRSSPreacquisitionMapping, ResourceID: scopeID}
	snapshot := agentContextSnapshot{
		RSSMappingSources: map[domain.EpisodeCoordinate]struct{}{source: {}},
		RSSMappingTargets: map[domain.EpisodeCoordinate]struct{}{target: {}},
	}
	proposal, err := json.Marshal(domain.AgentRSSPreacquisitionMappingProposal{
		ScopeID: scopeID, SourceSeason: source.Season, SourceEpisode: source.Episode,
		TargetSeason: target.Season, TargetEpisode: target.Episode,
		EvidenceCodes: []string{"episode_title_alignment"}, Decision: "resolved",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.agentSubmissionValidator(context.Background(), resolution, snapshot)(proposal); err != nil {
		t.Fatalf("valid scoped RSS Mapping proposal rejected: %v", err)
	}

	outOfScope := domain.AgentRSSPreacquisitionMappingProposal{
		ScopeID: scopeID, SourceSeason: 1, SourceEpisode: 14,
		TargetSeason: target.Season, TargetEpisode: target.Episode,
		EvidenceCodes: []string{"invented_source"}, Decision: "resolved",
	}
	encoded, _ := json.Marshal(outOfScope)
	if err := service.agentSubmissionValidator(context.Background(), resolution, snapshot)(encoded); err == nil || !strings.Contains(err.Error(), "agent_tool_scope_violation") {
		t.Fatalf("out-of-scope RSS Mapping proposal error = %v", err)
	}

	mapping.preview = nil
	if err := service.agentSubmissionValidator(context.Background(), resolution, snapshot)(proposal); err == nil || !strings.Contains(err.Error(), "agent_mapping_preview_incomplete") {
		t.Fatalf("incomplete RSS Mapping proposal error = %v", err)
	}
}

func TestAgentSubmissionValidatorRejectsProposalThatStillNeedsResolution(t *testing.T) {
	t.Parallel()
	service := &AgentResolutionService{}
	resourceID := uuid.MustParse("72000000-0000-4000-8000-000000000002")
	proposal, err := json.Marshal(domain.AgentRSSCoordinateProposal{
		EntryID: resourceID, SourceSeason: 1, SourceEpisode: 2, Decision: "review",
	})
	if err != nil {
		t.Fatal(err)
	}
	resolution := domain.AgentResolution{
		Capability: domain.AgentCapabilityRSSCoordinate, ResourceID: resourceID,
	}
	err = service.agentSubmissionValidator(context.Background(), resolution, agentContextSnapshot{})(proposal)
	if err == nil || !strings.Contains(err.Error(), "agent_requested_review") {
		t.Fatalf("unresolved proposal error = %v, want bounded-repair validation error", err)
	}
}

func TestValidateSubtitleVideoMatchProposal(t *testing.T) {
	snapshot := agentContextSnapshot{
		SubtitleVideoMatch: &scopedSubtitleVideoMatch{
			TaskID:        uuid.MustParse("74000000-0000-4000-8000-000000000001"),
			SeriesTitle:   "Show",
			TargetSeason:  1,
			TargetEpisode: 1,
			TargetTitle:   "Episode One",
			Candidates: []scopedSubtitleCandidate{
				{ID: "stream:2", Source: "embedded", StreamIndex: 2},
				{ID: "stream:3", Source: "embedded", StreamIndex: 3},
			},
		},
	}
	tests := []struct {
		name     string
		proposal domain.AgentSubtitleVideoMatchProposal
		want     domain.AgentValidationVerdict
	}{
		{
			name: "valid selection within scope",
			proposal: domain.AgentSubtitleVideoMatchProposal{
				TaskID:        uuid.MustParse("74000000-0000-4000-8000-000000000001"),
				Selected:      domain.SubtitleCandidateSelection{CandidateID: "stream:3"},
				EvidenceCodes: []string{"subtitle_title_alignment"}, Decision: "resolved",
			},
			want: domain.AgentValidationAutoApplicable,
		},
		{
			name: "candidate outside scope is invalid",
			proposal: domain.AgentSubtitleVideoMatchProposal{
				TaskID:        uuid.MustParse("74000000-0000-4000-8000-000000000001"),
				Selected:      domain.SubtitleCandidateSelection{CandidateID: "stream:9"},
				EvidenceCodes: []string{"subtitle_title_alignment"}, Decision: "resolved",
			},
			want: domain.AgentValidationInvalid,
		},
		{
			name: "task mismatch is invalid",
			proposal: domain.AgentSubtitleVideoMatchProposal{
				TaskID:        uuid.MustParse("74000000-0000-4000-8000-000000000099"),
				Selected:      domain.SubtitleCandidateSelection{CandidateID: "stream:2"},
				EvidenceCodes: []string{"subtitle_title_alignment"}, Decision: "resolved",
			},
			want: domain.AgentValidationInvalid,
		},
		{
			name: "review requested",
			proposal: domain.AgentSubtitleVideoMatchProposal{
				TaskID:        uuid.MustParse("74000000-0000-4000-8000-000000000001"),
				Selected:      domain.SubtitleCandidateSelection{CandidateID: "stream:2"},
				EvidenceCodes: []string{"subtitle_title_alignment"}, Decision: "review_required",
			},
			want: domain.AgentValidationReviewRequired,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := validateSubtitleVideoMatchProposal(test.proposal, snapshot)
			if got.Verdict != test.want {
				t.Fatalf("validateSubtitleVideoMatchProposal() verdict = %q, want %q (codes=%v)", got.Verdict, test.want, got.ReasonCodes)
			}
		})
	}
}
