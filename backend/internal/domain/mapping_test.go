package domain

import (
	"reflect"
	"testing"
)

func TestResolveEpisodeMappingUsesOneAnchorAcrossTMDbSeasonBoundary(t *testing.T) {
	seasons := []TMDbSeason{
		{Season: 1, EpisodeCount: 12, Titles: map[int]string{12: "第一季终章"}},
		{Season: 2, EpisodeCount: 12, Titles: map[int]string{1: "第二季开端", 2: "第二季第二集"}},
	}
	tests := []struct {
		name   string
		source EpisodeCoordinate
		want   EpisodeMappingResult
	}{
		{
			name:   "anchor episode",
			source: EpisodeCoordinate{Season: 1, Episode: 12},
			want: EpisodeMappingResult{
				Status: MappingMapped, AbsoluteEpisode: 12,
				Target: EpisodeCoordinate{Season: 1, Episode: 12}, TargetTitle: "第一季终章", MatchSource: MappingMatchAnchor,
			},
		},
		{
			name:   "first episode after boundary",
			source: EpisodeCoordinate{Season: 1, Episode: 13},
			want: EpisodeMappingResult{
				Status: MappingMapped, AbsoluteEpisode: 13,
				Target: EpisodeCoordinate{Season: 2, Episode: 1}, TargetTitle: "第二季开端", MatchSource: MappingMatchAnchor,
			},
		},
		{
			name:   "second episode after boundary",
			source: EpisodeCoordinate{Season: 1, Episode: 14},
			want: EpisodeMappingResult{
				Status: MappingMapped, AbsoluteEpisode: 14,
				Target: EpisodeCoordinate{Season: 2, Episode: 2}, TargetTitle: "第二季第二集", MatchSource: MappingMatchAnchor,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveEpisodeMapping(EpisodeMappingRequest{
				Source: test.source, AnchorSource: EpisodeCoordinate{Season: 1, Episode: 12},
				AnchorTarget: EpisodeCoordinate{Season: 1, Episode: 12}, TMDbSeasons: seasons,
			})
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ResolveEpisodeMapping() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestResolveEpisodeMappingShiftsSourceSeasonWithoutSourceSeasonLengths(t *testing.T) {
	got := ResolveEpisodeMapping(EpisodeMappingRequest{
		Source:       EpisodeCoordinate{Season: 2, Episode: 2},
		AnchorSource: EpisodeCoordinate{Season: 2, Episode: 1},
		AnchorTarget: EpisodeCoordinate{Season: 1, Episode: 29},
		TMDbSeasons: []TMDbSeason{
			{Season: 1, EpisodeCount: 38, Titles: map[int]string{30: "第三十集"}},
		},
	})
	want := EpisodeMappingResult{
		Status: MappingMapped, AbsoluteEpisode: 30,
		Target: EpisodeCoordinate{Season: 1, Episode: 30}, TargetTitle: "第三十集", MatchSource: MappingMatchAnchor,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveEpisodeMapping() = %#v, want %#v", got, want)
	}
}

func TestResolveEpisodeMappingRejectsUnsafeAnchorInference(t *testing.T) {
	seasons := []TMDbSeason{
		{Season: 0, EpisodeCount: 1, Titles: map[int]string{1: "特别篇"}},
		{Season: 1, EpisodeCount: 2, Titles: map[int]string{1: "第一集", 2: "第二集"}},
	}
	tests := []struct {
		name    string
		request EpisodeMappingRequest
		want    EpisodeMappingResult
	}{
		{
			name: "fractional source requires explicit mapping",
			request: EpisodeMappingRequest{
				Source:       EpisodeCoordinate{Season: 1, Episode: 12, EpisodeFractionHundredths: 50},
				AnchorSource: EpisodeCoordinate{Season: 1, Episode: 1},
				AnchorTarget: EpisodeCoordinate{Season: 1, Episode: 1}, TMDbSeasons: seasons,
			},
			want: EpisodeMappingResult{Status: MappingPending, MatchSource: MappingMatchPending, ErrorCode: "mapping_source_requires_explicit"},
		},
		{
			name: "different source season",
			request: EpisodeMappingRequest{
				Source: EpisodeCoordinate{Season: 2, Episode: 2}, AnchorSource: EpisodeCoordinate{Season: 1, Episode: 1},
				AnchorTarget: EpisodeCoordinate{Season: 1, Episode: 1}, TMDbSeasons: seasons,
			},
			want: EpisodeMappingResult{Status: MappingPending, MatchSource: MappingMatchPending, ErrorCode: "mapping_source_season_mismatch"},
		},
		{
			name: "before first TMDb episode",
			request: EpisodeMappingRequest{
				Source: EpisodeCoordinate{Season: 1, Episode: 1}, AnchorSource: EpisodeCoordinate{Season: 1, Episode: 3},
				AnchorTarget: EpisodeCoordinate{Season: 1, Episode: 1}, TMDbSeasons: seasons,
			},
			want: EpisodeMappingResult{Status: MappingPending, MatchSource: MappingMatchPending, ErrorCode: "mapping_target_out_of_range"},
		},
		{
			name: "after last TMDb episode",
			request: EpisodeMappingRequest{
				Source: EpisodeCoordinate{Season: 1, Episode: 3}, AnchorSource: EpisodeCoordinate{Season: 1, Episode: 1},
				AnchorTarget: EpisodeCoordinate{Season: 1, Episode: 1}, TMDbSeasons: seasons,
			},
			want: EpisodeMappingResult{Status: MappingPending, AbsoluteEpisode: 3, MatchSource: MappingMatchPending, ErrorCode: "mapping_target_out_of_range"},
		},
		{
			name: "special cannot anchor regular sequence",
			request: EpisodeMappingRequest{
				Source: EpisodeCoordinate{Season: 1, Episode: 1}, AnchorSource: EpisodeCoordinate{Season: 1, Episode: 1},
				AnchorTarget: EpisodeCoordinate{Season: 0, Episode: 1}, TMDbSeasons: seasons,
			},
			want: EpisodeMappingResult{
				Status: MappingPending, Target: EpisodeCoordinate{Season: 0, Episode: 1},
				MatchSource: MappingMatchPending, ErrorCode: "mapping_anchor_target_invalid",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveEpisodeMapping(test.request)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ResolveEpisodeMapping() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestResolveExplicitEpisodeMappingSupportsRegularAndSeasonZeroTargets(t *testing.T) {
	seasons := []TMDbSeason{
		{Season: 0, EpisodeCount: 2, Titles: map[int]string{1: "特别篇一", 2: "特别篇二"}},
		{Season: 1, EpisodeCount: 2, Titles: map[int]string{1: "第一集", 2: "第二集"}},
	}
	tests := []struct {
		name   string
		target EpisodeCoordinate
		want   EpisodeMappingResult
	}{
		{
			name:   "regular episode keeps absolute coordinate",
			target: EpisodeCoordinate{Season: 1, Episode: 2},
			want: EpisodeMappingResult{
				Status: MappingMapped, AbsoluteEpisode: 2, Target: EpisodeCoordinate{Season: 1, Episode: 2},
				TargetTitle: "第二集", MatchSource: MappingMatchExplicit,
			},
		},
		{
			name:   "special episode has no regular absolute coordinate",
			target: EpisodeCoordinate{Season: 0, Episode: 2},
			want: EpisodeMappingResult{
				Status: MappingMapped, Target: EpisodeCoordinate{Season: 0, Episode: 2},
				TargetTitle: "特别篇二", MatchSource: MappingMatchExplicit,
			},
		},
		{
			name:   "unknown target stays pending",
			target: EpisodeCoordinate{Season: 0, Episode: 3},
			want: EpisodeMappingResult{
				Status: MappingPending, Target: EpisodeCoordinate{Season: 0, Episode: 3},
				MatchSource: MappingMatchPending, ErrorCode: "mapping_target_out_of_range",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveExplicitEpisodeMapping(EpisodeCoordinate{Season: 1, Episode: 13}, test.target, seasons)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ResolveExplicitEpisodeMapping() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestTMDbAbsoluteEpisodeHelpersIgnoreSpecialsAndCrossSeasons(t *testing.T) {
	seasons := []TMDbSeason{
		{Season: 2, EpisodeCount: 2},
		{Season: 0, EpisodeCount: 5},
		{Season: 1, EpisodeCount: 3},
	}
	if got := RegularEpisodeCount(seasons); got != 5 {
		t.Fatalf("RegularEpisodeCount() = %d, want 5", got)
	}
	if got, ok := AbsoluteEpisodeForTarget(EpisodeCoordinate{Season: 2, Episode: 1}, seasons); !ok || got != 4 {
		t.Fatalf("AbsoluteEpisodeForTarget() = %d/%t, want 4/true", got, ok)
	}
	if got, ok := TargetForAbsoluteEpisode(5, seasons); !ok || got != (EpisodeCoordinate{Season: 2, Episode: 2}) {
		t.Fatalf("TargetForAbsoluteEpisode() = %#v/%t, want S02E02/true", got, ok)
	}
}
