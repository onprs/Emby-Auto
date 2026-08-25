package service

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type agentEpisodeMappingPreviewCatalogStub struct {
	input   domain.EpisodeMappingPlanInput
	preview domain.EpisodeMappingPreview
}

func (*agentEpisodeMappingPreviewCatalogStub) AutomaticEpisodeMappingEnabled(context.Context, uuid.UUID) (bool, error) {
	return true, nil
}

func (*agentEpisodeMappingPreviewCatalogStub) TryDeterministicEpisodeMapping(context.Context, uuid.UUID) (bool, error) {
	return false, nil
}

func (stub *agentEpisodeMappingPreviewCatalogStub) PreviewEpisodeMapping(_ context.Context, input domain.EpisodeMappingPlanInput) (domain.EpisodeMappingPreview, error) {
	stub.input = input
	return stub.preview, nil
}

func (*agentEpisodeMappingPreviewCatalogStub) ApplyAgentEpisodeMapping(context.Context, domain.AgentResolution, domain.AgentEpisodeMappingProposal, domain.AgentProposalValidation) (domain.SavedEpisodeMapping, error) {
	return domain.SavedEpisodeMapping{}, nil
}

func TestPreviewMappingToolPreservesSeasonZeroTargetPresence(t *testing.T) {
	t.Parallel()

	acquisitionID := uuid.MustParse("75000000-0000-4000-8000-000000000001")
	mappedFileID := uuid.MustParse("75000000-0000-4000-8000-000000000002")
	excludedFileID := uuid.MustParse("75000000-0000-4000-8000-000000000003")
	catalog := &agentEpisodeMappingPreviewCatalogStub{preview: domain.EpisodeMappingPreview{
		AcquisitionID: acquisitionID,
		Mode:          domain.EpisodeMappingModeExplicit,
		Rows: []domain.EpisodeMappingRow{
			{
				SourceFileID: mappedFileID, Status: domain.MappingMapped,
				TargetSeason: 0, TargetEpisode: 2, TargetEpisodeID: uuid.New(),
			},
			{SourceFileID: excludedFileID, Status: domain.MappingExcluded},
		},
	}}
	service := &AgentResolutionService{catalog: catalog}
	tool := service.previewMappingTool(acquisitionID, map[uuid.UUID]scopedFile{
		mappedFileID:   {ID: mappedFileID},
		excludedFileID: {ID: excludedFileID},
	})
	arguments, err := json.Marshal(map[string]any{
		"mode": domain.EpisodeMappingModeExplicit,
		"assignments": []map[string]any{
			{
				"sourceFileId":  mappedFileID,
				"action":        domain.EpisodeMappingExplicitMap,
				"targetSeason":  0,
				"targetEpisode": 2,
			},
			{
				"sourceFileId": excludedFileID,
				"action":       domain.EpisodeMappingExplicitExclude,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := tool.Execute(context.Background(), tool.Definition.Name, arguments)
	if err != nil {
		t.Fatalf("preview_episode_mapping error = %v", err)
	}
	if !bytes.Contains(result, []byte(`"targetSeason":0`)) || !bytes.Contains(result, []byte(`"targetEpisode":2`)) {
		t.Fatalf("preview JSON omits mapped Season 0 target: %s", result)
	}
	if catalog.input.AcquisitionID != acquisitionID || catalog.input.Mode != domain.EpisodeMappingModeExplicit || len(catalog.input.Assignments) != 2 {
		t.Fatalf("authoritative preview input = %+v", catalog.input)
	}
	if catalog.input.Assignments[0].Target != (domain.EpisodeCoordinate{Season: 0, Episode: 2}) || catalog.input.Assignments[1].Action != domain.EpisodeMappingExplicitExclude {
		t.Fatalf("authoritative preview assignments = %+v", catalog.input.Assignments)
	}

	var payload struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("decode preview JSON: %v", err)
	}
	if len(payload.Rows) != 2 {
		t.Fatalf("preview rows = %d, want 2", len(payload.Rows))
	}
	mapped := payload.Rows[0]
	if string(mapped["targetSeason"]) != "0" || string(mapped["targetEpisode"]) != "2" {
		t.Fatalf("mapped row target presence = %s/%s in %s", mapped["targetSeason"], mapped["targetEpisode"], result)
	}
	excluded := payload.Rows[1]
	if _, present := excluded["targetSeason"]; present {
		t.Fatalf("excluded row contains targetSeason: %s", result)
	}
	if _, present := excluded["targetEpisode"]; present {
		t.Fatalf("excluded row contains targetEpisode: %s", result)
	}
}
