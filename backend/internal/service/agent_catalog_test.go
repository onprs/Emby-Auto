package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

type catalogTMDbSearchStub struct {
	results map[string][]domain.TMDbSeriesSearchResult
}

func (stub catalogTMDbSearchStub) SearchTV(_ context.Context, query string) ([]domain.TMDbSeriesSearchResult, error) {
	return stub.results[query], nil
}

func TestCatalogProposalCandidateIDsAreScopedToExactQuery(t *testing.T) {
	service := &AgentResolutionService{tmdbSearch: catalogTMDbSearchStub{results: map[string][]domain.TMDbSeriesSearchResult{
		"Correct Show": {{TMDbSeriesID: 101, Name: "Correct Show"}},
		"Other Show":   {{TMDbSeriesID: 202, Name: "Other Show"}},
	}}}
	snapshot := service.catalogSearchSnapshot("rss_feed_lookup", []byte(`{"lookupId":"10000000-0000-0000-0000-000000000001"}`), []byte(`{"fixture":1}`))
	for _, query := range []string{"Correct Show", "Other Show"} {
		arguments, _ := json.Marshal(map[string]string{"query": query})
		if _, err := snapshot.Tools[0].Execute(context.Background(), "search_tmdb_catalog", arguments); err != nil {
			t.Fatalf("search_tmdb_catalog(%q) error = %v", query, err)
		}
	}

	validProposal, _ := json.Marshal(domain.AgentCatalogCandidateProposal{
		Query: "Correct Show", CandidateIDs: []int64{101}, EvidenceCodes: []string{"title_match"}, Decision: "resolved",
	})
	validation, _, err := service.validateProposal(context.Background(), domain.AgentResolution{
		Capability: domain.AgentCapabilityCatalogCandidate, Proposal: validProposal,
	}, snapshot)
	if err != nil {
		t.Fatalf("validateProposal(valid) error = %v", err)
	}
	if validation.Verdict != domain.AgentValidationReviewRequired {
		t.Fatalf("valid proposal verdict = %q", validation.Verdict)
	}

	crossQueryProposal, _ := json.Marshal(domain.AgentCatalogCandidateProposal{
		Query: "Other Show", CandidateIDs: []int64{101}, EvidenceCodes: []string{"title_match"}, Decision: "resolved",
	})
	validation, _, err = service.validateProposal(context.Background(), domain.AgentResolution{
		Capability: domain.AgentCapabilityCatalogCandidate, Proposal: crossQueryProposal,
	}, snapshot)
	if err != nil {
		t.Fatalf("validateProposal(cross-query) error = %v", err)
	}
	if validation.Verdict != domain.AgentValidationInvalid || len(validation.ReasonCodes) != 1 || validation.ReasonCodes[0] != "agent_tool_scope_violation" {
		t.Fatalf("cross-query proposal validation = %+v", validation)
	}
}
