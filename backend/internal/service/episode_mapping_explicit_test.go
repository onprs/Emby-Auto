package service

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestValidateExplicitMappingInputSupportsSeasonZeroAndExclusion(t *testing.T) {
	mappedFileID, excludedFileID := uuid.New(), uuid.New()
	input := domain.EpisodeMappingPlanInput{
		AcquisitionID: uuid.New(),
		Mode:          domain.EpisodeMappingModeExplicit,
		Assignments: []domain.EpisodeMappingExplicitInput{
			{
				SourceFileID: mappedFileID,
				Action:       domain.EpisodeMappingExplicitMap,
				Target:       domain.EpisodeCoordinate{Season: 0, Episode: 4},
			},
			{SourceFileID: excludedFileID, Action: domain.EpisodeMappingExplicitExclude},
		},
	}
	if err := validateMappingInput(input, false); err != nil {
		t.Fatalf("validateMappingInput() error = %v", err)
	}
}

func TestValidateExplicitMappingInputRejectsMalformedDispositions(t *testing.T) {
	fileID := uuid.New()
	tests := []struct {
		name        string
		assignments []domain.EpisodeMappingExplicitInput
	}{
		{name: "empty", assignments: nil},
		{
			name: "duplicate source",
			assignments: []domain.EpisodeMappingExplicitInput{
				{SourceFileID: fileID, Action: domain.EpisodeMappingExplicitExclude},
				{SourceFileID: fileID, Action: domain.EpisodeMappingExplicitExclude},
			},
		},
		{
			name: "exclude with target",
			assignments: []domain.EpisodeMappingExplicitInput{{
				SourceFileID: fileID,
				Action:       domain.EpisodeMappingExplicitExclude,
				Target:       domain.EpisodeCoordinate{Season: 0, Episode: 1},
			}},
		},
		{
			name: "map without episode",
			assignments: []domain.EpisodeMappingExplicitInput{{
				SourceFileID: fileID,
				Action:       domain.EpisodeMappingExplicitMap,
				Target:       domain.EpisodeCoordinate{Season: 0},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateMappingInput(domain.EpisodeMappingPlanInput{
				AcquisitionID: uuid.New(), Mode: domain.EpisodeMappingModeExplicit, Assignments: test.assignments,
			}, false)
			if err == nil {
				t.Fatal("validateMappingInput() error = nil")
			}
		})
	}
}

func TestExplicitMappingFingerprintIsIndependentOfAssignmentOrder(t *testing.T) {
	acquisitionID, firstFileID, secondFileID := uuid.New(), uuid.New(), uuid.New()
	assignments := []domain.EpisodeMappingExplicitInput{
		{SourceFileID: firstFileID, Action: domain.EpisodeMappingExplicitMap, Target: domain.EpisodeCoordinate{Season: 1, Episode: 1}},
		{SourceFileID: secondFileID, Action: domain.EpisodeMappingExplicitExclude},
	}
	first, err := mappingFingerprint(domain.EpisodeMappingPlanInput{
		AcquisitionID: acquisitionID, Mode: domain.EpisodeMappingModeExplicit, Assignments: assignments,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := mappingFingerprint(domain.EpisodeMappingPlanInput{
		AcquisitionID: acquisitionID,
		Mode:          domain.EpisodeMappingModeExplicit,
		Assignments:   []domain.EpisodeMappingExplicitInput{assignments[1], assignments[0]},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("equivalent explicit assignments produced different fingerprints")
	}
}

func TestValidateAgentExplicitMappingProposalPreservesTargetPresence(t *testing.T) {
	fileID := uuid.New()
	zero, episode := 0, 1
	valid := domain.AgentEpisodeMappingProposal{
		Mode: domain.EpisodeMappingModeExplicit,
		Assignments: []domain.AgentEpisodeMappingDisposition{{
			SourceFileID: fileID, Action: domain.EpisodeMappingExplicitMap, TargetSeason: &zero, TargetEpisode: &episode,
		}},
	}
	if err := validateAgentEpisodeMappingProposalShape(valid); err != nil {
		t.Fatalf("validateAgentEpisodeMappingProposalShape(Season 0) error = %v", err)
	}
	missingSeason := valid
	missingSeason.Assignments = append([]domain.AgentEpisodeMappingDisposition(nil), valid.Assignments...)
	missingSeason.Assignments[0].TargetSeason = nil
	if err := validateAgentEpisodeMappingProposalShape(missingSeason); err == nil {
		t.Fatal("map disposition without targetSeason was accepted")
	}
	excludedWithTarget := valid
	excludedWithTarget.Assignments = []domain.AgentEpisodeMappingDisposition{{
		SourceFileID: fileID, Action: domain.EpisodeMappingExplicitExclude, TargetSeason: &zero,
	}}
	if err := validateAgentEpisodeMappingProposalShape(excludedWithTarget); err == nil {
		t.Fatal("exclude disposition with targetSeason was accepted")
	}
}

func TestEpisodeMappingPlanFromAgentProposalKeepsLegacyAnchorCompatibility(t *testing.T) {
	acquisitionID, sourceFileID := uuid.New(), uuid.New()
	targetSeason, targetEpisode := 2, 3
	plan := episodeMappingPlanFromAgentProposal(domain.AgentEpisodeMappingProposal{
		AcquisitionID: acquisitionID,
		SourceFileID:  &sourceFileID,
		TargetSeason:  &targetSeason,
		TargetEpisode: &targetEpisode,
	})
	if plan.Mode != domain.EpisodeMappingModeAnchor || plan.Anchor.SourceFileID != sourceFileID || plan.Anchor.Target != (domain.EpisodeCoordinate{Season: 2, Episode: 3}) {
		t.Fatalf("legacy Agent proposal plan = %#v", plan)
	}
}

func TestAnchorMappingFingerprintMatchesLegacyCanonicalPayload(t *testing.T) {
	fingerprint, err := mappingFingerprint(domain.EpisodeMappingPlanInput{
		AcquisitionID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Mode:          domain.EpisodeMappingModeAnchor,
		Anchor: domain.EpisodeMappingAnchorInput{
			SourceFileID: uuid.MustParse("22222222-2222-2222-2222-222222222222"),
			Target:       domain.EpisodeCoordinate{Season: 2, Episode: 3},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const legacyDigest = "8ab22fb19c7b4c58b76d289c76235eb11ab8acced7381794a841c6d849eccbc0"
	if got := hex.EncodeToString(fingerprint[:]); got != legacyDigest {
		t.Fatalf("anchor mapping fingerprint = %s, want legacy digest %s", got, legacyDigest)
	}
}

func TestPlanExplicitMappingFilesAssignsSharedPlaceholderSubtitlesByPath(t *testing.T) {
	firstVideoID, secondVideoID := uuid.New(), uuid.New()
	firstSubtitleID, secondSubtitleID := uuid.New(), uuid.New()
	season, placeholderEpisode := int32(1), int32(1)
	changes, excluded, err := planExplicitMappingFileChanges(
		[]domain.EpisodeMappingRow{
			{SourceFileID: firstVideoID, SourceSeason: 1, SourceEpisode: 1, Status: domain.MappingMapped},
			{SourceFileID: secondVideoID, SourceSeason: 1, SourceEpisode: 2, Status: domain.MappingExcluded},
		},
		[]explicitMappingFile{
			{id: firstVideoID, relativePath: "Pack S01E01.mkv", mediaKind: domain.MediaVideo, sourceSeason: &season, sourceEpisode: &placeholderEpisode},
			{id: secondVideoID, relativePath: "Pack S01E02.mkv", mediaKind: domain.MediaVideo, sourceSeason: &season, sourceEpisode: &placeholderEpisode},
			{id: firstSubtitleID, relativePath: "Pack S01E01.ass", mediaKind: domain.MediaSubtitle, sourceSeason: &season, sourceEpisode: &placeholderEpisode},
			{id: secondSubtitleID, relativePath: "Pack S01E02.ass", mediaKind: domain.MediaSubtitle, sourceSeason: &season, sourceEpisode: &placeholderEpisode},
		},
	)
	if err != nil {
		t.Fatalf("planExplicitMappingFileChanges() error = %v", err)
	}
	if excluded != 1 || len(changes) != 4 {
		t.Fatalf("planned changes/excluded = %d/%d, want 4/1", len(changes), excluded)
	}
	byID := make(map[uuid.UUID]explicitMappingFileChange, len(changes))
	for _, change := range changes {
		byID[change.id] = change
	}
	if change := byID[firstSubtitleID]; !change.selected || change.coordinate != (domain.EpisodeCoordinate{Season: 1, Episode: 1}) {
		t.Fatalf("first subtitle change = %#v", change)
	}
	if change := byID[secondSubtitleID]; change.selected || change.coordinate != (domain.EpisodeCoordinate{Season: 1, Episode: 2}) {
		t.Fatalf("second subtitle change = %#v", change)
	}
}

func TestPlanExplicitMappingFilesRejectsAmbiguousStoredSubtitleCoordinate(t *testing.T) {
	firstVideoID, secondVideoID, subtitleID := uuid.New(), uuid.New(), uuid.New()
	season, placeholderEpisode := int32(1), int32(1)
	_, _, err := planExplicitMappingFileChanges(
		[]domain.EpisodeMappingRow{
			{SourceFileID: firstVideoID, SourceSeason: 1, SourceEpisode: 1, Status: domain.MappingMapped},
			{SourceFileID: secondVideoID, SourceSeason: 1, SourceEpisode: 2, Status: domain.MappingMapped},
		},
		[]explicitMappingFile{
			{id: firstVideoID, relativePath: "Pack S01E01.mkv", mediaKind: domain.MediaVideo, sourceSeason: &season, sourceEpisode: &placeholderEpisode},
			{id: secondVideoID, relativePath: "Pack S01E02.mkv", mediaKind: domain.MediaVideo, sourceSeason: &season, sourceEpisode: &placeholderEpisode},
			{id: subtitleID, relativePath: "Pack commentary.ass", mediaKind: domain.MediaSubtitle, sourceSeason: &season, sourceEpisode: &placeholderEpisode},
		},
	)
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "mapping_subtitle_owner_ambiguous" {
		t.Fatalf("ambiguous subtitle error = %v", err)
	}
}
