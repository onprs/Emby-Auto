package worker

import (
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/emby"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func TestMatchRealtimeTargetsUsesMappedTargetCoordinate(t *testing.T) {
	targets := []rssRealtimeTarget{{
		source:          domain.EpisodeCoordinate{Season: 1, Episode: 1},
		targetEpisodeID: uuid.New(),
		target:          domain.EpisodeCoordinate{Season: 2, Episode: 1},
		tmdbSeriesID:    500,
	}}
	sourceSeason, sourceEpisode := 1, 1
	targetSeason, targetEpisode := 2, 1
	matches := matchRealtimeTargets(targets, []domain.EmbyLibraryItemCatalog{
		{ItemType: "Episode", Path: "/library/source.mkv", SeasonNumber: &sourceSeason, EpisodeNumber: &sourceEpisode},
		{ItemType: "Episode", Path: "/library/target.mkv", SeasonNumber: &targetSeason, EpisodeNumber: &targetEpisode},
	})
	if len(matches) != 1 || !matches[0].present || matches[0].source != "target_coordinate" {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestMatchRealtimeTargetsPrefersTMDbEpisodeID(t *testing.T) {
	tmdbEpisodeID := int64(9001)
	season, episode := 9, 9
	matches := matchRealtimeTargets([]rssRealtimeTarget{{
		target: domain.EpisodeCoordinate{Season: 2, Episode: 1}, tmdbEpisodeID: &tmdbEpisodeID,
	}}, []domain.EmbyLibraryItemCatalog{{
		ItemType: "Episode", Path: "/library/target.mkv", ProviderIDs: map[string]string{"TmDb": "9001"},
		SeasonNumber: &season, EpisodeNumber: &episode,
	}})
	if len(matches) != 1 || !matches[0].present || matches[0].source != "tmdb_episode" {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestMatchRealtimeTargetsDoesNotFallbackWhenTMDbEpisodeIDIsKnown(t *testing.T) {
	tmdbEpisodeID := int64(9001)
	season, episode := 2, 1
	matches := matchRealtimeTargets([]rssRealtimeTarget{{
		target: domain.EpisodeCoordinate{Season: 2, Episode: 1}, tmdbEpisodeID: &tmdbEpisodeID,
	}}, []domain.EmbyLibraryItemCatalog{{
		ItemType: "Episode", Path: "/library/wrong-provider.mkv", ProviderIDs: map[string]string{"Tmdb": "9002"},
		SeasonNumber: &season, EpisodeNumber: &episode,
	}})
	if len(matches) != 1 || matches[0].present || matches[0].source != "absent" {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestMatchRealtimeTargetsFallsBackWhenEmbyProviderIDIsUnavailable(t *testing.T) {
	tmdbEpisodeID := int64(9001)
	season, episode := 2, 1
	matches := matchRealtimeTargets([]rssRealtimeTarget{{
		target: domain.EpisodeCoordinate{Season: 2, Episode: 1}, tmdbEpisodeID: &tmdbEpisodeID,
	}}, []domain.EmbyLibraryItemCatalog{{
		ItemType: "Episode", Path: "/library/target.mkv", ProviderIDs: map[string]string{},
		SeasonNumber: &season, EpisodeNumber: &episode,
	}})
	if len(matches) != 1 || !matches[0].present || matches[0].source != "target_coordinate" {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestMatchRealtimeTargetsAcceptsTheMovieDbProviderKey(t *testing.T) {
	tmdbEpisodeID := int64(9001)
	matches := matchRealtimeTargets([]rssRealtimeTarget{{tmdbEpisodeID: &tmdbEpisodeID}}, []domain.EmbyLibraryItemCatalog{{
		ItemType: "Episode", Path: "/library/target.mkv", ProviderIDs: map[string]string{"TheMovieDb": "9001"},
	}})
	if len(matches) != 1 || !matches[0].present || matches[0].source != "tmdb_episode" {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestMatchRealtimeTargetsRejectsPathlessEpisode(t *testing.T) {
	season, episode := 2, 1
	matches := matchRealtimeTargets([]rssRealtimeTarget{{
		target: domain.EpisodeCoordinate{Season: 2, Episode: 1},
	}}, []domain.EmbyLibraryItemCatalog{{
		ItemType: "Episode", SeasonNumber: &season, EpisodeNumber: &episode,
	}})
	if len(matches) != 1 || matches[0].present || matches[0].source != "absent" {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestClassifyRealtimeEmbyRequestError(t *testing.T) {
	tests := []struct {
		status    int
		retryable bool
		code      string
	}{
		{status: http.StatusUnauthorized, retryable: false, code: "emby_authentication_failed"},
		{status: http.StatusTooManyRequests, retryable: true, code: "emby_realtime_request_failed"},
		{status: http.StatusServiceUnavailable, retryable: true, code: "emby_realtime_request_failed"},
		{status: http.StatusBadRequest, retryable: false, code: "emby_realtime_request_rejected"},
	}
	for _, test := range tests {
		err := classifyRealtimeEmbyRequestError(&emby.HTTPError{StatusCode: test.status})
		var verificationErr *service.RSSRealtimeVerificationError
		if !errors.As(err, &verificationErr) || verificationErr.Code != test.code || verificationErr.Retryable != test.retryable {
			t.Fatalf("status %d error = %#v", test.status, err)
		}
	}
}
