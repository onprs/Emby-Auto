package domain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestDecodeAgentEpisodeMappingProposalRejectsInvalidPresenceAndShape(t *testing.T) {
	acquisitionID := "81000000-0000-4000-8000-000000000001"
	fileID := "81000000-0000-4000-8000-000000000002"
	anchor := `"anchor":{"sourceFileId":"` + fileID + `","targetSeason":1,"targetEpisode":2}`
	common := `"acquisitionId":"` + acquisitionID + `","evidenceCodes":[],"decision":"resolved"`
	tests := []struct {
		name string
		raw  string
	}{
		{name: "missing acquisition", raw: `{` + anchor + `,"evidenceCodes":[],"decision":"resolved"}`},
		{name: "null acquisition", raw: `{"acquisitionId":null,` + anchor + `,"evidenceCodes":[],"decision":"resolved"}`},
		{name: "nil acquisition", raw: `{"acquisitionId":"00000000-0000-0000-0000-000000000000",` + anchor + `,"evidenceCodes":[],"decision":"resolved"}`},
		{name: "null mode", raw: `{` + common + `,"mode":null,` + anchor + `}`},
		{name: "empty mode", raw: `{` + common + `,"mode":"",` + anchor + `}`},
		{name: "anchor null assignments", raw: `{` + common + `,"mode":"anchor",` + anchor + `,"assignments":null}`},
		{name: "anchor empty assignments", raw: `{` + common + `,"mode":"anchor",` + anchor + `,"assignments":[]}`},
		{name: "explicit null anchor", raw: `{` + common + `,"mode":"explicit","anchor":null,"assignments":[{"sourceFileId":"` + fileID + `","action":"exclude"}]}`},
		{name: "exclude null targetSeason", raw: `{` + common + `,"mode":"explicit","assignments":[{"sourceFileId":"` + fileID + `","action":"exclude","targetSeason":null}]}`},
		{name: "exclude null targetEpisode", raw: `{` + common + `,"mode":"explicit","assignments":[{"sourceFileId":"` + fileID + `","action":"exclude","targetEpisode":null}]}`},
		{name: "map null targetSeason", raw: `{` + common + `,"mode":"explicit","assignments":[{"sourceFileId":"` + fileID + `","action":"map","targetSeason":null,"targetEpisode":2}]}`},
		{name: "map null targetEpisode", raw: `{` + common + `,"mode":"explicit","assignments":[{"sourceFileId":"` + fileID + `","action":"map","targetSeason":0,"targetEpisode":null}]}`},
		{name: "map missing targetSeason", raw: `{` + common + `,"mode":"explicit","assignments":[{"sourceFileId":"` + fileID + `","action":"map","targetEpisode":2}]}`},
		{name: "map missing targetEpisode", raw: `{` + common + `,"mode":"explicit","assignments":[{"sourceFileId":"` + fileID + `","action":"map","targetSeason":0}]}`},
		{name: "null assignments", raw: `{` + common + `,"mode":"explicit","assignments":null}`},
		{name: "empty assignments", raw: `{` + common + `,"mode":"explicit","assignments":[]}`},
		{name: "null assignment", raw: `{` + common + `,"mode":"explicit","assignments":[null]}`},
		{name: "null evidenceCodes", raw: `{"acquisitionId":"` + acquisitionID + `",` + anchor + `,"evidenceCodes":null,"decision":"resolved"}`},
		{name: "null evidence item", raw: `{"acquisitionId":"` + acquisitionID + `",` + anchor + `,"evidenceCodes":[null],"decision":"resolved"}`},
		{name: "too many evidence codes", raw: `{"acquisitionId":"` + acquisitionID + `",` + anchor + `,"evidenceCodes":[` + strings.Repeat(`"x",`, 16) + `"x"],"decision":"resolved"}`},
		{name: "evidence code too long", raw: `{"acquisitionId":"` + acquisitionID + `",` + anchor + `,"evidenceCodes":["` + strings.Repeat("x", 129) + `"],"decision":"resolved"}`},
		{name: "null decision", raw: `{"acquisitionId":"` + acquisitionID + `",` + anchor + `,"evidenceCodes":[],"decision":null}`},
		{name: "invalid decision", raw: `{"acquisitionId":"` + acquisitionID + `",` + anchor + `,"evidenceCodes":[],"decision":"accepted"}`},
		{name: "unknown top-level field", raw: `{` + common + `,` + anchor + `,"unexpected":true}`},
		{name: "unknown anchor field", raw: `{` + common + `,"mode":"anchor","anchor":{"sourceFileId":"` + fileID + `","targetSeason":1,"targetEpisode":2,"unexpected":true}}`},
		{name: "unknown assignment field", raw: `{` + common + `,"mode":"explicit","assignments":[{"sourceFileId":"` + fileID + `","action":"exclude","unexpected":true}]}`},
		{name: "trailing data", raw: `{` + common + `,` + anchor + `} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if proposal, err := DecodeAgentEpisodeMappingProposal(json.RawMessage(test.raw)); err == nil {
				t.Fatalf("DecodeAgentEpisodeMappingProposal() = %#v, want error", proposal)
			}
		})
	}
}

func TestDecodeAgentEpisodeMappingProposalReturnsCanonicalShapes(t *testing.T) {
	acquisitionID := uuid.MustParse("82000000-0000-4000-8000-000000000001")
	firstFileID := uuid.MustParse("82000000-0000-4000-8000-000000000002")
	secondFileID := uuid.MustParse("82000000-0000-4000-8000-000000000003")
	tests := []struct {
		name   string
		raw    string
		verify func(*testing.T, AgentEpisodeMappingProposal)
	}{
		{
			name: "legacy anchor",
			raw:  `{"acquisitionId":"` + acquisitionID.String() + `","sourceFileId":"` + firstFileID.String() + `","targetSeason":2,"targetEpisode":3,"evidenceCodes":[],"decision":"resolved"}`,
			verify: func(t *testing.T, proposal AgentEpisodeMappingProposal) {
				if proposal.Mode != "" || proposal.SourceFileID == nil || *proposal.SourceFileID != firstFileID || proposal.TargetSeason == nil || *proposal.TargetSeason != 2 || proposal.TargetEpisode == nil || *proposal.TargetEpisode != 3 || proposal.Anchor != nil || proposal.Assignments != nil {
					t.Fatalf("legacy proposal = %#v", proposal)
				}
			},
		},
		{
			name: "v2 anchor",
			raw:  `{"acquisitionId":"` + acquisitionID.String() + `","mode":"anchor","anchor":{"sourceFileId":"` + firstFileID.String() + `","targetSeason":1,"targetEpisode":4},"evidenceCodes":["anchor"],"decision":"review_required"}`,
			verify: func(t *testing.T, proposal AgentEpisodeMappingProposal) {
				if proposal.Mode != EpisodeMappingModeAnchor || proposal.Anchor == nil || proposal.Anchor.SourceFileID != firstFileID || proposal.Anchor.TargetSeason != 1 || proposal.Anchor.TargetEpisode != 4 || proposal.SourceFileID != nil || proposal.Assignments != nil {
					t.Fatalf("anchor proposal = %#v", proposal)
				}
			},
		},
		{
			name: "explicit Season 0 and exclude",
			raw:  `{"acquisitionId":"` + acquisitionID.String() + `","mode":"explicit","assignments":[{"sourceFileId":"` + firstFileID.String() + `","action":"map","targetSeason":0,"targetEpisode":2},{"sourceFileId":"` + secondFileID.String() + `","action":"exclude"}],"evidenceCodes":["explicit"],"decision":"resolved"}`,
			verify: func(t *testing.T, proposal AgentEpisodeMappingProposal) {
				if proposal.Mode != EpisodeMappingModeExplicit || len(proposal.Assignments) != 2 || proposal.Assignments[0].TargetSeason == nil || *proposal.Assignments[0].TargetSeason != 0 || proposal.Assignments[0].TargetEpisode == nil || *proposal.Assignments[0].TargetEpisode != 2 || proposal.Assignments[1].Action != EpisodeMappingExplicitExclude || proposal.Assignments[1].TargetSeason != nil || proposal.Assignments[1].TargetEpisode != nil || proposal.Anchor != nil || proposal.SourceFileID != nil {
					t.Fatalf("explicit proposal = %#v", proposal)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proposal, err := DecodeAgentEpisodeMappingProposal(json.RawMessage(test.raw))
			if err != nil {
				t.Fatalf("DecodeAgentEpisodeMappingProposal() error = %v", err)
			}
			if proposal.AcquisitionID != acquisitionID || proposal.EvidenceCodes == nil {
				t.Fatalf("common proposal = %#v", proposal)
			}
			test.verify(t, proposal)
			canonical, err := json.Marshal(proposal)
			if err != nil {
				t.Fatalf("marshal canonical proposal: %v", err)
			}
			if _, err := DecodeAgentEpisodeMappingProposal(canonical); err != nil {
				t.Fatalf("decode canonical proposal %s: %v", canonical, err)
			}
		})
	}
}
