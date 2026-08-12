package service

import (
	"testing"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func TestNullableMappingActor(t *testing.T) {
	t.Parallel()

	if got := nullableMappingActor(uuid.Nil); got.Valid {
		t.Fatalf("nullableMappingActor(nil).Valid = true, want false")
	}
	actor := uuid.MustParse("53000000-0000-4000-8000-000000000001")
	got := nullableMappingActor(actor)
	if !got.Valid || uuid.UUID(got.Bytes) != actor {
		t.Fatalf("nullableMappingActor(actor) = %#v, want %s", got, actor)
	}
}

func TestDeterministicMappingAnchorRequiresCompleteUniqueExactCoordinates(t *testing.T) {
	t.Parallel()
	firstID := uuid.MustParse("53000000-0000-4000-8000-000000000011")
	secondID := uuid.MustParse("53000000-0000-4000-8000-000000000012")
	season, firstEpisode, secondEpisode := int32(2), int32(1), int32(2)
	files := []db.ListAgentMappingFilesRow{
		{ID: repository.UUIDToPG(firstID), SourceSeason: &season, SourceEpisode: &firstEpisode},
		{ID: repository.UUIDToPG(secondID), SourceSeason: &season, SourceEpisode: &secondEpisode},
	}
	episodes := []db.ListAgentTMDbEpisodesRow{
		{SeasonNumber: 2, EpisodeNumber: 1},
		{SeasonNumber: 2, EpisodeNumber: 2},
	}

	anchor, ok := deterministicMappingAnchor(files, episodes)
	if !ok || anchor.SourceFileID != firstID || anchor.Target != (domain.EpisodeCoordinate{Season: 2, Episode: 1}) {
		t.Fatalf("deterministicMappingAnchor() = %#v/%t", anchor, ok)
	}

	tests := []struct {
		name   string
		mutate func([]db.ListAgentMappingFilesRow, []db.ListAgentTMDbEpisodesRow)
	}{
		{name: "missing source coordinate", mutate: func(files []db.ListAgentMappingFilesRow, _ []db.ListAgentTMDbEpisodesRow) {
			files[0].SourceEpisode = nil
		}},
		{name: "cross source season", mutate: func(files []db.ListAgentMappingFilesRow, _ []db.ListAgentTMDbEpisodesRow) {
			other := int32(1)
			files[1].SourceSeason = &other
		}},
		{name: "duplicate source coordinate", mutate: func(files []db.ListAgentMappingFilesRow, _ []db.ListAgentTMDbEpisodesRow) {
			files[1].SourceEpisode = files[0].SourceEpisode
		}},
		{name: "missing exact target", mutate: func(_ []db.ListAgentMappingFilesRow, episodes []db.ListAgentTMDbEpisodesRow) {
			episodes[1].EpisodeNumber = 3
		}},
		{name: "duplicate target", mutate: func(_ []db.ListAgentMappingFilesRow, episodes []db.ListAgentTMDbEpisodesRow) {
			episodes[1] = episodes[0]
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fileCopy := append([]db.ListAgentMappingFilesRow(nil), files...)
			episodeCopy := append([]db.ListAgentTMDbEpisodesRow(nil), episodes...)
			test.mutate(fileCopy, episodeCopy)
			if anchor, ok := deterministicMappingAnchor(fileCopy, episodeCopy); ok {
				t.Fatalf("deterministicMappingAnchor() = %#v/true, want ineligible", anchor)
			}
		})
	}
}
