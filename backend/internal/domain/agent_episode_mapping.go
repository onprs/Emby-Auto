package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"unicode/utf8"

	"github.com/google/uuid"
)

type agentEpisodeMappingProposalJSON struct {
	AcquisitionID json.RawMessage `json:"acquisitionId"`
	Mode          json.RawMessage `json:"mode"`
	Anchor        json.RawMessage `json:"anchor"`
	Assignments   json.RawMessage `json:"assignments"`
	SourceFileID  json.RawMessage `json:"sourceFileId"`
	TargetSeason  json.RawMessage `json:"targetSeason"`
	TargetEpisode json.RawMessage `json:"targetEpisode"`
	EvidenceCodes json.RawMessage `json:"evidenceCodes"`
	Decision      json.RawMessage `json:"decision"`
}

type agentEpisodeMappingAnchorJSON struct {
	SourceFileID  json.RawMessage `json:"sourceFileId"`
	TargetSeason  json.RawMessage `json:"targetSeason"`
	TargetEpisode json.RawMessage `json:"targetEpisode"`
}

type agentEpisodeMappingDispositionJSON struct {
	SourceFileID  json.RawMessage `json:"sourceFileId"`
	Action        json.RawMessage `json:"action"`
	TargetSeason  json.RawMessage `json:"targetSeason"`
	TargetEpisode json.RawMessage `json:"targetEpisode"`
}

func DecodeAgentEpisodeMappingProposal(raw json.RawMessage) (AgentEpisodeMappingProposal, error) {
	var encoded agentEpisodeMappingProposalJSON
	if err := decodeAgentEpisodeMappingJSON(raw, &encoded); err != nil {
		return AgentEpisodeMappingProposal{}, fmt.Errorf("decode Agent episode mapping proposal: %w", err)
	}
	acquisitionID, err := decodeAgentEpisodeMappingUUID(encoded.AcquisitionID, "acquisitionId")
	if err != nil {
		return AgentEpisodeMappingProposal{}, err
	}
	evidenceCodes, err := decodeAgentEpisodeMappingEvidence(encoded.EvidenceCodes)
	if err != nil {
		return AgentEpisodeMappingProposal{}, err
	}
	decision, err := decodeAgentEpisodeMappingString(encoded.Decision, "decision")
	if err != nil {
		return AgentEpisodeMappingProposal{}, err
	}
	if decision != "resolved" && decision != "review_required" {
		return AgentEpisodeMappingProposal{}, fmt.Errorf("decision must be resolved or review_required")
	}
	proposal := AgentEpisodeMappingProposal{
		AcquisitionID: acquisitionID,
		EvidenceCodes: evidenceCodes,
		Decision:      decision,
	}
	if len(encoded.Mode) == 0 {
		return decodeLegacyAgentEpisodeMappingProposal(encoded, proposal)
	}
	mode, err := decodeAgentEpisodeMappingString(encoded.Mode, "mode")
	if err != nil {
		return AgentEpisodeMappingProposal{}, err
	}
	switch EpisodeMappingMode(mode) {
	case EpisodeMappingModeAnchor:
		return decodeAnchorAgentEpisodeMappingProposal(encoded, proposal)
	case EpisodeMappingModeExplicit:
		return decodeExplicitAgentEpisodeMappingProposal(encoded, proposal)
	default:
		return AgentEpisodeMappingProposal{}, fmt.Errorf("mode must be anchor or explicit")
	}
}

func decodeLegacyAgentEpisodeMappingProposal(encoded agentEpisodeMappingProposalJSON, proposal AgentEpisodeMappingProposal) (AgentEpisodeMappingProposal, error) {
	if len(encoded.Anchor) != 0 || len(encoded.Assignments) != 0 {
		return AgentEpisodeMappingProposal{}, fmt.Errorf("legacy Agent episode mapping proposal must omit anchor and assignments")
	}
	sourceFileID, err := decodeAgentEpisodeMappingUUID(encoded.SourceFileID, "sourceFileId")
	if err != nil {
		return AgentEpisodeMappingProposal{}, err
	}
	targetSeason, err := decodeAgentEpisodeMappingInteger(encoded.TargetSeason, 1, math.MaxInt32, "targetSeason")
	if err != nil {
		return AgentEpisodeMappingProposal{}, err
	}
	targetEpisode, err := decodeAgentEpisodeMappingInteger(encoded.TargetEpisode, 1, math.MaxInt32, "targetEpisode")
	if err != nil {
		return AgentEpisodeMappingProposal{}, err
	}
	proposal.SourceFileID = &sourceFileID
	proposal.TargetSeason = &targetSeason
	proposal.TargetEpisode = &targetEpisode
	return proposal, nil
}

func decodeAnchorAgentEpisodeMappingProposal(encoded agentEpisodeMappingProposalJSON, proposal AgentEpisodeMappingProposal) (AgentEpisodeMappingProposal, error) {
	if len(encoded.SourceFileID) != 0 || len(encoded.TargetSeason) != 0 || len(encoded.TargetEpisode) != 0 || len(encoded.Assignments) != 0 {
		return AgentEpisodeMappingProposal{}, fmt.Errorf("anchor Agent episode mapping proposal must omit legacy fields and assignments")
	}
	if agentEpisodeMappingJSONMissing(encoded.Anchor) {
		return AgentEpisodeMappingProposal{}, fmt.Errorf("anchor must be present and non-null")
	}
	var encodedAnchor agentEpisodeMappingAnchorJSON
	if err := decodeAgentEpisodeMappingJSON(encoded.Anchor, &encodedAnchor); err != nil {
		return AgentEpisodeMappingProposal{}, fmt.Errorf("decode anchor: %w", err)
	}
	sourceFileID, err := decodeAgentEpisodeMappingUUID(encodedAnchor.SourceFileID, "anchor.sourceFileId")
	if err != nil {
		return AgentEpisodeMappingProposal{}, err
	}
	targetSeason, err := decodeAgentEpisodeMappingInteger(encodedAnchor.TargetSeason, 1, math.MaxInt32, "anchor.targetSeason")
	if err != nil {
		return AgentEpisodeMappingProposal{}, err
	}
	targetEpisode, err := decodeAgentEpisodeMappingInteger(encodedAnchor.TargetEpisode, 1, math.MaxInt32, "anchor.targetEpisode")
	if err != nil {
		return AgentEpisodeMappingProposal{}, err
	}
	proposal.Mode = EpisodeMappingModeAnchor
	proposal.Anchor = &AgentEpisodeMappingAnchor{
		SourceFileID:  sourceFileID,
		TargetSeason:  targetSeason,
		TargetEpisode: targetEpisode,
	}
	return proposal, nil
}

func decodeExplicitAgentEpisodeMappingProposal(encoded agentEpisodeMappingProposalJSON, proposal AgentEpisodeMappingProposal) (AgentEpisodeMappingProposal, error) {
	if len(encoded.SourceFileID) != 0 || len(encoded.TargetSeason) != 0 || len(encoded.TargetEpisode) != 0 || len(encoded.Anchor) != 0 {
		return AgentEpisodeMappingProposal{}, fmt.Errorf("explicit Agent episode mapping proposal must omit legacy fields and anchor")
	}
	if agentEpisodeMappingJSONMissing(encoded.Assignments) {
		return AgentEpisodeMappingProposal{}, fmt.Errorf("assignments must be present and non-null")
	}
	var encodedAssignments []json.RawMessage
	if err := decodeAgentEpisodeMappingJSON(encoded.Assignments, &encodedAssignments); err != nil {
		return AgentEpisodeMappingProposal{}, fmt.Errorf("decode assignments: %w", err)
	}
	if len(encodedAssignments) < 1 || len(encodedAssignments) > 128 {
		return AgentEpisodeMappingProposal{}, fmt.Errorf("assignments must contain between 1 and 128 dispositions")
	}
	assignments := make([]AgentEpisodeMappingDisposition, 0, len(encodedAssignments))
	for index, encodedAssignment := range encodedAssignments {
		assignment, err := decodeAgentEpisodeMappingDisposition(encodedAssignment)
		if err != nil {
			return AgentEpisodeMappingProposal{}, fmt.Errorf("decode assignments[%d]: %w", index, err)
		}
		assignments = append(assignments, assignment)
	}
	proposal.Mode = EpisodeMappingModeExplicit
	proposal.Assignments = assignments
	return proposal, nil
}

func decodeAgentEpisodeMappingDisposition(raw json.RawMessage) (AgentEpisodeMappingDisposition, error) {
	var encoded agentEpisodeMappingDispositionJSON
	if err := decodeAgentEpisodeMappingJSON(raw, &encoded); err != nil {
		return AgentEpisodeMappingDisposition{}, err
	}
	sourceFileID, err := decodeAgentEpisodeMappingUUID(encoded.SourceFileID, "sourceFileId")
	if err != nil {
		return AgentEpisodeMappingDisposition{}, err
	}
	action, err := decodeAgentEpisodeMappingString(encoded.Action, "action")
	if err != nil {
		return AgentEpisodeMappingDisposition{}, err
	}
	assignment := AgentEpisodeMappingDisposition{SourceFileID: sourceFileID, Action: EpisodeMappingExplicitAction(action)}
	switch assignment.Action {
	case EpisodeMappingExplicitMap:
		targetSeason, err := decodeAgentEpisodeMappingInteger(encoded.TargetSeason, 0, math.MaxInt32, "targetSeason")
		if err != nil {
			return AgentEpisodeMappingDisposition{}, err
		}
		targetEpisode, err := decodeAgentEpisodeMappingInteger(encoded.TargetEpisode, 1, math.MaxInt32, "targetEpisode")
		if err != nil {
			return AgentEpisodeMappingDisposition{}, err
		}
		assignment.TargetSeason = &targetSeason
		assignment.TargetEpisode = &targetEpisode
		return assignment, nil
	case EpisodeMappingExplicitExclude:
		if len(encoded.TargetSeason) != 0 || len(encoded.TargetEpisode) != 0 {
			return AgentEpisodeMappingDisposition{}, fmt.Errorf("exclude disposition must omit targetSeason and targetEpisode")
		}
		return assignment, nil
	default:
		return AgentEpisodeMappingDisposition{}, fmt.Errorf("action must be map or exclude")
	}
}

func decodeAgentEpisodeMappingEvidence(raw json.RawMessage) ([]string, error) {
	if agentEpisodeMappingJSONMissing(raw) {
		return nil, fmt.Errorf("evidenceCodes must be present and non-null")
	}
	var encoded []json.RawMessage
	if err := decodeAgentEpisodeMappingJSON(raw, &encoded); err != nil {
		return nil, fmt.Errorf("decode evidenceCodes: %w", err)
	}
	if len(encoded) > 16 {
		return nil, fmt.Errorf("evidenceCodes must contain at most 16 entries")
	}
	values := make([]string, 0, len(encoded))
	for index, item := range encoded {
		value, err := decodeAgentEpisodeMappingString(item, fmt.Sprintf("evidenceCodes[%d]", index))
		if err != nil {
			return nil, err
		}
		if utf8.RuneCountInString(value) > 128 {
			return nil, fmt.Errorf("evidenceCodes[%d] must contain at most 128 characters", index)
		}
		values = append(values, value)
	}
	return values, nil
}

func decodeAgentEpisodeMappingUUID(raw json.RawMessage, field string) (uuid.UUID, error) {
	if agentEpisodeMappingJSONMissing(raw) {
		return uuid.Nil, fmt.Errorf("%s must be present and non-null", field)
	}
	var value uuid.UUID
	if err := decodeAgentEpisodeMappingJSON(raw, &value); err != nil {
		return uuid.Nil, fmt.Errorf("decode %s: %w", field, err)
	}
	if value == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%s must not be nil UUID", field)
	}
	return value, nil
}

func decodeAgentEpisodeMappingString(raw json.RawMessage, field string) (string, error) {
	if agentEpisodeMappingJSONMissing(raw) {
		return "", fmt.Errorf("%s must be present and non-null", field)
	}
	var value string
	if err := decodeAgentEpisodeMappingJSON(raw, &value); err != nil {
		return "", fmt.Errorf("decode %s: %w", field, err)
	}
	return value, nil
}

func decodeAgentEpisodeMappingInteger(raw json.RawMessage, minimum, maximum int, field string) (int, error) {
	if agentEpisodeMappingJSONMissing(raw) {
		return 0, fmt.Errorf("%s must be present and non-null", field)
	}
	var value int
	if err := decodeAgentEpisodeMappingJSON(raw, &value); err != nil {
		return 0, fmt.Errorf("decode %s: %w", field, err)
	}
	if value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", field, minimum, maximum)
	}
	return value, nil
}

func decodeAgentEpisodeMappingJSON(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("JSON contains trailing data")
	}
	return nil
}

func agentEpisodeMappingJSONMissing(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}
