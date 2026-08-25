package httpapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func (server *Server) ListAgentResolutions(ctx context.Context, request ListAgentResolutionsRequestObject) (ListAgentResolutionsResponseObject, error) {
	if server.agentResolutions == nil {
		return ListAgentResolutions503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "agent")}, nil
	}
	limit := 50
	if request.Params.Limit != nil {
		limit = int(*request.Params.Limit)
	}
	var cursor, resourceID *uuid.UUID
	if request.Params.Cursor != nil {
		value := uuid.UUID(*request.Params.Cursor)
		cursor = &value
	}
	if request.Params.ResourceId != nil {
		value := uuid.UUID(*request.Params.ResourceId)
		resourceID = &value
	}
	status := enumString(request.Params.Status)
	capability := enumString(request.Params.Capability)
	page, err := server.agentResolutions.List(ctx, cursor, limit, status, capability, request.Params.ResourceType, resourceID)
	if err != nil {
		return ListAgentResolutions503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}
	response := AgentResolutionPage{Items: make([]AgentResolution, 0, len(page.Items))}
	for _, resolution := range page.Items {
		response.Items = append(response.Items, agentResolutionResponse(resolution))
	}
	if page.NextCursor != nil {
		value := *page.NextCursor
		response.NextCursor = &value
	}
	return ListAgentResolutions200JSONResponse(response), nil
}

func (server *Server) GetAgentResolution(ctx context.Context, request GetAgentResolutionRequestObject) (GetAgentResolutionResponseObject, error) {
	if server.agentResolutions == nil {
		return GetAgentResolution503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "agent")}, nil
	}
	resolution, err := server.agentResolutions.Get(ctx, uuid.UUID(request.ResolutionId))
	if errors.Is(err, domain.ErrNotFound) {
		return GetAgentResolution404JSONResponse{NotFoundJSONResponse: notFoundError(ctx, "the Agent resolution was not found")}, nil
	}
	if err != nil {
		return GetAgentResolution503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}
	return GetAgentResolution200JSONResponse(agentResolutionResponse(resolution)), nil
}

func agentResolutionResponse(value domain.AgentResolution) AgentResolution {
	reasonCodes := value.Validation.ReasonCodes
	if reasonCodes == nil {
		reasonCodes = []string{}
	}
	response := AgentResolution{
		Id: value.ID, OperationId: value.OperationID, Version: int32(value.Version),
		Capability: AgentResolutionCapability(value.Capability), ResourceType: value.ResourceType, ResourceId: value.ResourceID,
		Trigger: AgentResolutionTrigger(value.Trigger), Status: AgentResolutionStatus(value.Status), Model: value.Model,
		PromptVersion: value.PromptVersion, ToolsetVersion: value.ToolsetVersion,
		ToolCallCount: int32(value.ToolCallCount),
		CreatedAt:     value.CreatedAt, UpdatedAt: value.UpdatedAt,
		InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, LatencyMilliseconds: value.LatencyMilliseconds,
		StartedAt: value.StartedAt, CompletedAt: value.CompletedAt, ReviewedAt: value.ReviewedAt, AppliedAt: value.AppliedAt,
	}
	if value.Validation.Verdict != "" {
		response.Validation = &AgentProposalValidation{Verdict: AgentProposalValidationVerdict(value.Validation.Verdict), ReasonCodes: reasonCodes}
	}
	if value.ResourceVersion != nil {
		converted := int32(*value.ResourceVersion)
		response.ResourceVersion = &converted
	}
	optionalString(&response.ErrorCode, value.ErrorCode)
	optionalString(&response.ErrorMessage, value.ErrorMessage)
	if value.ReviewDecision != "" {
		converted := AgentResolutionReviewDecision(value.ReviewDecision)
		response.ReviewDecision = &converted
	}
	if len(value.Proposal) > 2 && json.Valid(value.Proposal) {
		if proposal, err := agentProposalResponse(value.Capability, value.Proposal); err == nil {
			response.Proposal = &proposal
		}
	}
	return response
}

func agentProposalResponse(capability domain.AgentCapability, raw json.RawMessage) (AgentProposal, error) {
	var proposal AgentProposal
	switch capability {
	case domain.AgentCapabilityRSSCoordinate:
		var value domain.AgentRSSCoordinateProposal
		if err := json.Unmarshal(raw, &value); err != nil {
			return proposal, err
		}
		err := proposal.FromAgentRSSCoordinateProposal(AgentRSSCoordinateProposal{
			Capability: AgentRSSCoordinateProposalCapabilityRssCoordinate, EntryId: value.EntryID,
			SourceSeason: int32(value.SourceSeason), SourceEpisode: int32(value.SourceEpisode), EvidenceCodes: value.EvidenceCodes,
			Decision: AgentRSSCoordinateProposalDecision(value.Decision),
		})
		return proposal, err
	case domain.AgentCapabilityRSSReleaseAdjudication:
		var value domain.AgentRSSReleaseAdjudicationProposal
		if err := json.Unmarshal(raw, &value); err != nil {
			return proposal, err
		}
		entries := make([]AgentRSSReleaseDisposition, 0, len(value.Entries))
		for _, item := range value.Entries {
			var sourceSeason *int32
			if item.SourceSeason != nil {
				converted := int32(*item.SourceSeason)
				sourceSeason = &converted
			}
			var sourceEpisode *int32
			if item.SourceEpisode != nil {
				converted := int32(*item.SourceEpisode)
				sourceEpisode = &converted
			}
			entries = append(entries, AgentRSSReleaseDisposition{
				EntryId: item.EntryID, Disposition: AgentRSSReleaseDispositionDisposition(item.Disposition),
				SourceSeason: sourceSeason, SourceEpisode: sourceEpisode, RelatedEntryId: item.RelatedEntryID, EvidenceCodes: item.EvidenceCodes,
			})
		}
		err := proposal.FromAgentRSSReleaseAdjudicationProposal(AgentRSSReleaseAdjudicationProposal{
			Capability: AgentRSSReleaseAdjudicationProposalCapabilityRssReleaseAdjudication,
			BatchId:    value.BatchID, ScopedEntryIds: value.ScopedEntryIDs, Entries: entries,
			Decision: AgentRSSReleaseAdjudicationProposalDecision(value.Decision),
		})
		return proposal, err
	case domain.AgentCapabilityRSSPreacquisitionMapping:
		var value domain.AgentRSSPreacquisitionMappingProposal
		if err := json.Unmarshal(raw, &value); err != nil {
			return proposal, err
		}
		err := proposal.FromAgentRSSPreacquisitionMappingProposal(AgentRSSPreacquisitionMappingProposal{
			Capability: AgentRSSPreacquisitionMappingProposalCapabilityRssPreacquisitionMapping,
			ScopeId:    value.ScopeID, SourceSeason: int32(value.SourceSeason), SourceEpisode: int32(value.SourceEpisode),
			TargetSeason: int32(value.TargetSeason), TargetEpisode: int32(value.TargetEpisode),
			EvidenceCodes: value.EvidenceCodes, Decision: AgentRSSPreacquisitionMappingProposalDecision(value.Decision),
		})
		return proposal, err
	case domain.AgentCapabilityDownloadFileResolution:
		var value domain.AgentDownloadFileResolutionProposal
		if err := json.Unmarshal(raw, &value); err != nil {
			return proposal, err
		}
		videos := make([]AgentDownloadVideoProposal, 0, len(value.Videos))
		for _, item := range value.Videos {
			videos = append(videos, AgentDownloadVideoProposal{FileId: item.FileID, SourceSeason: int32(item.SourceSeason), SourceEpisode: int32(item.SourceEpisode)})
		}
		subtitles := make([]AgentDownloadSubtitleProposal, 0, len(value.Subtitles))
		for _, item := range value.Subtitles {
			subtitles = append(subtitles, AgentDownloadSubtitleProposal{FileId: item.FileID, VideoFileId: item.VideoFileID})
		}
		err := proposal.FromAgentDownloadFileResolutionProposal(AgentDownloadFileResolutionProposal{
			Capability: AgentDownloadFileResolutionProposalCapabilityDownloadFileResolution, Videos: videos, Subtitles: subtitles,
			Decision: AgentDownloadFileResolutionProposalDecision(value.Decision),
		})
		return proposal, err
	case domain.AgentCapabilityCatalogCandidate:
		var value domain.AgentCatalogCandidateProposal
		if err := json.Unmarshal(raw, &value); err != nil {
			return proposal, err
		}
		err := proposal.FromAgentCatalogCandidateProposal(AgentCatalogCandidateProposal{
			Capability: AgentCatalogCandidateProposalCapabilityCatalogCandidate, Query: value.Query, CandidateIds: value.CandidateIDs,
			EvidenceCodes: value.EvidenceCodes, Decision: AgentCatalogCandidateProposalDecision(value.Decision),
		})
		return proposal, err
	case domain.AgentCapabilityEpisodeMapping:
		value, err := domain.DecodeAgentEpisodeMappingProposal(raw)
		if err != nil {
			return proposal, err
		}
		converted, err := agentEpisodeMappingProposalResponse(value)
		if err != nil {
			return proposal, err
		}
		err = proposal.FromAgentEpisodeMappingProposal(converted)
		return proposal, err
	default:
		return proposal, errors.New("unsupported Agent proposal capability")
	}
}

func agentEpisodeMappingProposalResponse(value domain.AgentEpisodeMappingProposal) (AgentEpisodeMappingProposal, error) {
	var proposal AgentEpisodeMappingProposal
	if err := service.ValidateAgentEpisodeMappingProposalShape(value); err != nil {
		return proposal, err
	}
	switch value.Mode {
	case "":
		err := proposal.FromAgentEpisodeMappingLegacyAnchorProposal(AgentEpisodeMappingLegacyAnchorProposal{
			Capability:    AgentEpisodeMappingLegacyAnchorProposalCapabilityEpisodeMapping,
			AcquisitionId: value.AcquisitionID, SourceFileId: *value.SourceFileID,
			TargetSeason: int32(*value.TargetSeason), TargetEpisode: int32(*value.TargetEpisode),
			EvidenceCodes: value.EvidenceCodes, Decision: AgentEpisodeMappingLegacyAnchorProposalDecision(value.Decision),
		})
		return proposal, err
	case domain.EpisodeMappingModeAnchor:
		err := proposal.FromAgentEpisodeMappingAnchorProposal(AgentEpisodeMappingAnchorProposal{
			Capability:    AgentEpisodeMappingAnchorProposalCapabilityEpisodeMapping,
			AcquisitionId: value.AcquisitionID, Mode: AgentEpisodeMappingAnchorProposalModeAnchor,
			Anchor: EpisodeMappingAnchor{
				SourceFileId: value.Anchor.SourceFileID, TargetSeason: int32(value.Anchor.TargetSeason), TargetEpisode: int32(value.Anchor.TargetEpisode),
			},
			EvidenceCodes: value.EvidenceCodes, Decision: AgentEpisodeMappingAnchorProposalDecision(value.Decision),
		})
		return proposal, err
	case domain.EpisodeMappingModeExplicit:
		assignments := make([]EpisodeMappingExplicitDisposition, 0, len(value.Assignments))
		for _, assignment := range value.Assignments {
			var converted EpisodeMappingExplicitDisposition
			var err error
			switch assignment.Action {
			case domain.EpisodeMappingExplicitMap:
				err = converted.FromEpisodeMappingExplicitMapDisposition(EpisodeMappingExplicitMapDisposition{
					SourceFileId: assignment.SourceFileID, Action: EpisodeMappingExplicitMapDispositionAction(domain.EpisodeMappingExplicitMap),
					TargetSeason: int32(*assignment.TargetSeason), TargetEpisode: int32(*assignment.TargetEpisode),
				})
			case domain.EpisodeMappingExplicitExclude:
				err = converted.FromEpisodeMappingExplicitExcludeDisposition(EpisodeMappingExplicitExcludeDisposition{
					SourceFileId: assignment.SourceFileID, Action: EpisodeMappingExplicitExcludeDispositionAction(domain.EpisodeMappingExplicitExclude),
				})
			}
			if err != nil {
				return proposal, err
			}
			assignments = append(assignments, converted)
		}
		err := proposal.FromAgentEpisodeMappingExplicitProposal(AgentEpisodeMappingExplicitProposal{
			Capability:    AgentEpisodeMappingExplicitProposalCapabilityEpisodeMapping,
			AcquisitionId: value.AcquisitionID, Mode: AgentEpisodeMappingExplicitProposalModeExplicit, Assignments: assignments,
			EvidenceCodes: value.EvidenceCodes, Decision: AgentEpisodeMappingExplicitProposalDecision(value.Decision),
		})
		return proposal, err
	default:
		return proposal, errors.New("unsupported Agent episode mapping proposal mode")
	}
}

func enumString[T ~string](value *T) *string {
	if value == nil {
		return nil
	}
	converted := string(*value)
	return &converted
}
