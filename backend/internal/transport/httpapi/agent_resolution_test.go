package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type agentResolutionServiceStub struct {
	resolution domain.AgentResolution
	getCalls   int
	listCalls  int
	err        error
}

func (stub *agentResolutionServiceStub) Get(context.Context, uuid.UUID) (domain.AgentResolution, error) {
	stub.getCalls++
	return stub.resolution, stub.err
}

func (stub *agentResolutionServiceStub) List(context.Context, *uuid.UUID, int, *string, *string, *string, *uuid.UUID) (domain.AgentResolutionPage, error) {
	stub.listCalls++
	return domain.AgentResolutionPage{Items: []domain.AgentResolution{stub.resolution}}, stub.err
}

func fixtureAgentResolution(id, operationID, resourceID uuid.UUID) domain.AgentResolution {
	now := time.Now()
	return domain.AgentResolution{
		ID: id, OperationID: operationID, Version: 2, Capability: domain.AgentCapabilityDownloadFileResolution,
		ResourceType: "download", ResourceID: resourceID, Trigger: "user", Status: domain.AgentResolutionReviewRequired,
		Model: "fixture-model", PromptVersion: "download-file-resolution-v1", ToolsetVersion: "agent-tools-v1",
		Validation: domain.AgentProposalValidation{Verdict: domain.AgentValidationReviewRequired, ReasonCodes: []string{"review_required"}},
		CreatedAt:  now, UpdatedAt: now,
	}
}

func TestQueuedAgentResolutionOmitsValidationAndCommandActions(t *testing.T) {
	resolutionID := uuid.MustParse("44000000-0000-0000-0000-000000000022")
	operationID := uuid.MustParse("44000000-0000-0000-0000-000000000023")
	resourceID := uuid.MustParse("44000000-0000-0000-0000-000000000024")
	authentication, _ := authenticatedServer()
	resolution := fixtureAgentResolution(resolutionID, operationID, resourceID)
	resolution.Status = domain.AgentResolutionQueued
	resolution.Validation = domain.AgentProposalValidation{}
	stub := &agentResolutionServiceStub{resolution: resolution}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithAgentResolutions(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/resolutions/"+resolutionID.String(), nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"validation"`) || strings.Contains(response.Body.String(), `"actions"`) {
		t.Fatalf("read-only queued response contains command metadata: %s", response.Body.String())
	}
	if stub.getCalls != 1 {
		t.Fatalf("Get calls = %d, want 1", stub.getCalls)
	}
}

func encodedAgentEpisodeMappingProposal(t *testing.T, value domain.AgentEpisodeMappingProposal) AgentEpisodeMappingProposal {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := agentProposalResponse(domain.AgentCapabilityEpisodeMapping, encoded)
	if err != nil {
		t.Fatalf("agentProposalResponse() error = %v", err)
	}
	response, err := proposal.AsAgentEpisodeMappingProposal()
	if err != nil {
		t.Fatalf("AsAgentEpisodeMappingProposal() error = %v", err)
	}
	return response
}

func TestAgentEpisodeMappingProposalResponseUsesLegacyAnchorBranch(t *testing.T) {
	acquisitionID, sourceFileID := uuid.New(), uuid.New()
	season, episode := 2, 3
	response := encodedAgentEpisodeMappingProposal(t, domain.AgentEpisodeMappingProposal{
		AcquisitionID: acquisitionID, SourceFileID: &sourceFileID, TargetSeason: &season, TargetEpisode: &episode,
		EvidenceCodes: []string{"legacy_anchor"}, Decision: "resolved",
	})
	legacy, err := response.AsAgentEpisodeMappingLegacyAnchorProposal()
	if err != nil {
		t.Fatalf("AsAgentEpisodeMappingLegacyAnchorProposal() error = %v", err)
	}
	if legacy.AcquisitionId != acquisitionID || legacy.SourceFileId != sourceFileID || legacy.TargetSeason != 2 || legacy.TargetEpisode != 3 {
		t.Fatalf("legacy Agent response = %#v", legacy)
	}
}

func TestAgentEpisodeMappingProposalResponseUsesV2AnchorBranch(t *testing.T) {
	acquisitionID, sourceFileID := uuid.New(), uuid.New()
	response := encodedAgentEpisodeMappingProposal(t, domain.AgentEpisodeMappingProposal{
		AcquisitionID: acquisitionID, Mode: domain.EpisodeMappingModeAnchor,
		Anchor:        &domain.AgentEpisodeMappingAnchor{SourceFileID: sourceFileID, TargetSeason: 1, TargetEpisode: 4},
		EvidenceCodes: []string{"v2_anchor"}, Decision: "resolved",
	})
	anchor, err := response.AsAgentEpisodeMappingAnchorProposal()
	if err != nil {
		t.Fatalf("AsAgentEpisodeMappingAnchorProposal() error = %v", err)
	}
	if anchor.AcquisitionId != acquisitionID || anchor.Mode != AgentEpisodeMappingAnchorProposalModeAnchor || anchor.Anchor.SourceFileId != sourceFileID || anchor.Anchor.TargetSeason != 1 || anchor.Anchor.TargetEpisode != 4 {
		t.Fatalf("v2 anchor Agent response = %#v", anchor)
	}
}

func TestAgentEpisodeMappingProposalResponseKeepsExplicitSeasonZeroAndExclusion(t *testing.T) {
	acquisitionID, mappedFileID, excludedFileID := uuid.New(), uuid.New(), uuid.New()
	season, episode := 0, 2
	response := encodedAgentEpisodeMappingProposal(t, domain.AgentEpisodeMappingProposal{
		AcquisitionID: acquisitionID,
		Mode:          domain.EpisodeMappingModeExplicit,
		Assignments: []domain.AgentEpisodeMappingDisposition{
			{SourceFileID: mappedFileID, Action: domain.EpisodeMappingExplicitMap, TargetSeason: &season, TargetEpisode: &episode},
			{SourceFileID: excludedFileID, Action: domain.EpisodeMappingExplicitExclude},
		},
		EvidenceCodes: []string{"complete_scope"}, Decision: "resolved",
	})
	explicit, err := response.AsAgentEpisodeMappingExplicitProposal()
	if err != nil {
		t.Fatalf("AsAgentEpisodeMappingExplicitProposal() error = %v", err)
	}
	if explicit.Mode != AgentEpisodeMappingExplicitProposalModeExplicit || len(explicit.Assignments) != 2 {
		t.Fatalf("explicit Agent response = %#v", explicit)
	}
	mapped, err := explicit.Assignments[0].AsEpisodeMappingExplicitMapDisposition()
	if err != nil {
		t.Fatalf("AsEpisodeMappingExplicitMapDisposition() error = %v", err)
	}
	if mapped.SourceFileId != mappedFileID || mapped.TargetSeason != 0 || mapped.TargetEpisode != 2 {
		t.Fatalf("Season 0 Agent response = %#v", mapped)
	}
	excluded, err := explicit.Assignments[1].AsEpisodeMappingExplicitExcludeDisposition()
	if err != nil {
		t.Fatalf("AsEpisodeMappingExplicitExcludeDisposition() error = %v", err)
	}
	if excluded.SourceFileId != excludedFileID || excluded.Action != EpisodeMappingExplicitExcludeDispositionAction(domain.EpisodeMappingExplicitExclude) {
		t.Fatalf("excluded Agent response = %#v", excluded)
	}
}

func TestAgentEpisodeMappingProposalResponseOmitsMalformedHistoricalShapes(t *testing.T) {
	acquisitionID, sourceFileID := uuid.New(), uuid.New()
	season, episode := 1, 2
	mixed, err := json.Marshal(domain.AgentEpisodeMappingProposal{
		AcquisitionID: acquisitionID, Mode: domain.EpisodeMappingModeAnchor,
		Anchor:       &domain.AgentEpisodeMappingAnchor{SourceFileID: sourceFileID, TargetSeason: 1, TargetEpisode: 2},
		SourceFileID: &sourceFileID, TargetSeason: &season, TargetEpisode: &episode,
		EvidenceCodes: []string{"mixed"}, Decision: "resolved",
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "mixed anchor fields", raw: mixed},
		{name: "null excluded target", raw: json.RawMessage(`{"acquisitionId":"` + acquisitionID.String() + `","mode":"explicit","assignments":[{"sourceFileId":"` + sourceFileID.String() + `","action":"exclude","targetSeason":null}],"evidenceCodes":[],"decision":"resolved"}`)},
		{name: "null evidence codes", raw: json.RawMessage(`{"acquisitionId":"` + acquisitionID.String() + `","mode":"anchor","anchor":{"sourceFileId":"` + sourceFileID.String() + `","targetSeason":1,"targetEpisode":2},"evidenceCodes":null,"decision":"resolved"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := agentProposalResponse(domain.AgentCapabilityEpisodeMapping, test.raw); err == nil {
				t.Fatal("malformed historical Agent proposal was serialized")
			}
			resolution := fixtureAgentResolution(uuid.New(), uuid.New(), acquisitionID)
			resolution.Capability = domain.AgentCapabilityEpisodeMapping
			resolution.Proposal = test.raw
			if response := agentResolutionResponse(resolution); response.Proposal != nil {
				t.Fatalf("malformed historical Agent proposal was exposed: %#v", response.Proposal)
			}
		})
	}
}

func TestRemovedAgentMutationRoutesReturnStructuredNotFoundWithoutServiceCalls(t *testing.T) {
	resourceID := uuid.MustParse("44000000-0000-0000-0000-000000000031")
	authentication, _ := authenticatedServer()
	stub := &agentResolutionServiceStub{}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithAgentResolutions(stub)))
	paths := []string{
		"/api/v1/agent/resolutions/" + resourceID.String() + "/accept",
		"/api/v1/agent/resolutions/" + resourceID.String() + "/reject",
		"/api/v1/agent/resolutions/" + resourceID.String() + "/retry",
		"/api/v1/rss/entries/" + resourceID.String() + "/agent-resolution",
		"/api/v1/rss/adjudication-batches/" + resourceID.String() + "/agent-resolution",
		"/api/v1/downloads/" + resourceID.String() + "/agent-resolution",
		"/api/v1/acquisitions/" + resourceID.String() + "/episode-mapping/agent-resolution",
		"/api/v1/acquisitions/" + resourceID.String() + "/catalog/agent-resolution",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"expectedVersion":2}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "removed-command")
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var apiError ApiError
			if err := json.Unmarshal(response.Body.Bytes(), &apiError); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if apiError.Code != "not_found" {
				t.Fatalf("error = %#v", apiError)
			}
		})
	}
	if stub.getCalls != 0 || stub.listCalls != 0 {
		t.Fatalf("removed commands reached service: get=%d list=%d", stub.getCalls, stub.listCalls)
	}
}

func TestHistoricalAgentReviewStatesRemainReadableWithoutActions(t *testing.T) {
	resolutionID := uuid.MustParse("44000000-0000-0000-0000-000000000041")
	operationID := uuid.MustParse("44000000-0000-0000-0000-000000000042")
	resourceID := uuid.MustParse("44000000-0000-0000-0000-000000000043")
	authentication, _ := authenticatedServer()

	for _, status := range []domain.AgentResolutionStatus{domain.AgentResolutionReviewRequired, domain.AgentResolutionRejected} {
		t.Run(string(status), func(t *testing.T) {
			resolution := fixtureAgentResolution(resolutionID, operationID, resourceID)
			resolution.Status = status
			stub := &agentResolutionServiceStub{resolution: resolution}
			handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithAgentResolutions(stub)))
			request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/resolutions/"+resolutionID.String(), nil)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), `"actions"`) {
				t.Fatalf("historical read response contains actions: %s", response.Body.String())
			}
			if !strings.Contains(response.Body.String(), `"status":"`+string(status)+`"`) {
				t.Fatalf("historical status missing from response: %s", response.Body.String())
			}
		})
	}
}
