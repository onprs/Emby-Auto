package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestAgentSubmissionCoordinatesMatchDatabaseIntegerBounds(t *testing.T) {
	for _, capability := range []domain.AgentCapability{domain.AgentCapabilityRSSCoordinate, domain.AgentCapabilityDownloadFileResolution} {
		for _, dimension := range []string{"season", "episode"} {
			for _, value := range []int64{-1, 0, 1, math.MaxInt32, math.MaxInt32 + 1, 1<<32 + 1} {
				if value > int64(math.MaxInt) {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s_%d", capability, dimension, value), func(t *testing.T) {
					resourceID, fileID := uuid.New(), uuid.New()
					season, episode := 1, 1
					if dimension == "season" {
						season = int(value)
					} else {
						episode = int(value)
					}
					var proposal any = domain.AgentRSSCoordinateProposal{
						EntryID: resourceID, SourceSeason: season, SourceEpisode: episode, Decision: "resolved",
					}
					if capability == domain.AgentCapabilityDownloadFileResolution {
						proposal = domain.AgentDownloadFileResolutionProposal{
							Videos: []domain.AgentDownloadVideoProposal{{FileID: fileID, SourceSeason: season, SourceEpisode: episode}}, Decision: "resolved",
						}
					}
					encoded, err := json.Marshal(proposal)
					if err != nil {
						t.Fatal(err)
					}
					service := &AgentResolutionService{}
					validate := service.agentSubmissionValidator(context.Background(), domain.AgentResolution{
						Capability: capability, ResourceID: resourceID,
					}, agentContextSnapshot{Files: map[uuid.UUID]scopedFile{fileID: {ID: fileID, MediaKind: string(domain.MediaVideo)}}})
					err = validate(encoded)
					valid := value >= 1 && value <= math.MaxInt32
					if (err == nil) != valid {
						t.Fatalf("coordinate %d validation = %v, want valid %t", value, err, valid)
					}
				})
			}
		}
	}
}
