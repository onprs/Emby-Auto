package domain

import (
	"sort"
	"strings"
)

type EpisodeCoordinate struct {
	Season  int
	Episode int
}

type TMDbSeason struct {
	Season       int
	EpisodeCount int
	Titles       map[int]string
}

type EpisodeMappingRequest struct {
	Source       EpisodeCoordinate
	AnchorSource EpisodeCoordinate
	AnchorTarget EpisodeCoordinate
	TMDbSeasons  []TMDbSeason
}

type MappingStatus string

const (
	MappingMapped   MappingStatus = "mapped"
	MappingPending  MappingStatus = "pending"
	MappingExcluded MappingStatus = "excluded"
)

type MappingMatchSource string

const (
	MappingMatchAnchor   MappingMatchSource = "anchor"
	MappingMatchExplicit MappingMatchSource = "explicit"
	MappingMatchAbsolute MappingMatchSource = "absolute"
	MappingMatchPending  MappingMatchSource = "pending"
)

type EpisodeMappingMode string

const (
	EpisodeMappingModeAnchor   EpisodeMappingMode = "anchor"
	EpisodeMappingModeExplicit EpisodeMappingMode = "explicit"
)

type EpisodeMappingResult struct {
	Status          MappingStatus
	AbsoluteEpisode int
	Target          EpisodeCoordinate
	TargetTitle     string
	MatchSource     MappingMatchSource
	ErrorCode       string
}

// ResolveEpisodeMapping applies one source-to-TMDb anchor to every episode in
// the same source season. TMDb regular seasons provide the canonical sequence.
func ResolveEpisodeMapping(request EpisodeMappingRequest) EpisodeMappingResult {
	if !validSourceCoordinate(request.Source) || !validSourceCoordinate(request.AnchorSource) {
		return pendingMapping(0, EpisodeCoordinate{}, "mapping_source_invalid")
	}
	if request.Source.Season != request.AnchorSource.Season {
		return pendingMapping(0, EpisodeCoordinate{}, "mapping_source_season_mismatch")
	}

	anchorAbsolute, ok := AbsoluteEpisodeForTarget(request.AnchorTarget, request.TMDbSeasons)
	if !ok {
		return pendingMapping(0, request.AnchorTarget, "mapping_anchor_target_invalid")
	}
	absolute := anchorAbsolute + request.Source.Episode - request.AnchorSource.Episode
	if absolute <= 0 {
		return pendingMapping(0, EpisodeCoordinate{}, "mapping_target_out_of_range")
	}
	target, ok := TargetForAbsoluteEpisode(absolute, request.TMDbSeasons)
	if !ok {
		return pendingMapping(absolute, EpisodeCoordinate{}, "mapping_target_out_of_range")
	}
	title, errorCode := resolveTargetTitle(request.TMDbSeasons, target)
	if errorCode != "" {
		return pendingMapping(absolute, target, errorCode)
	}
	return EpisodeMappingResult{
		Status:          MappingMapped,
		AbsoluteEpisode: absolute,
		Target:          target,
		TargetTitle:     title,
		MatchSource:     MappingMatchAnchor,
	}
}

// ResolveExplicitEpisodeMapping 只校验一个后端目录目标，不使用锚点模式的常规季累计序列。
func ResolveExplicitEpisodeMapping(source, target EpisodeCoordinate, seasons []TMDbSeason) EpisodeMappingResult {
	if !validSourceCoordinate(source) {
		return pendingMapping(0, EpisodeCoordinate{}, "mapping_source_invalid")
	}
	if target.Season < 0 || target.Episode <= 0 {
		return pendingMapping(0, target, "mapping_target_out_of_range")
	}
	title, errorCode := resolveTargetTitle(seasons, target)
	if errorCode != "" {
		return pendingMapping(0, target, errorCode)
	}
	absolute := 0
	if target.Season > 0 {
		var ok bool
		absolute, ok = AbsoluteEpisodeForTarget(target, seasons)
		if !ok {
			return pendingMapping(0, target, "mapping_target_out_of_range")
		}
	}
	return EpisodeMappingResult{
		Status:          MappingMapped,
		AbsoluteEpisode: absolute,
		Target:          target,
		TargetTitle:     title,
		MatchSource:     MappingMatchExplicit,
	}
}

func AbsoluteEpisodeForTarget(target EpisodeCoordinate, seasons []TMDbSeason) (int, bool) {
	if !validSourceCoordinate(target) {
		return 0, false
	}
	absolute := 0
	for _, season := range regularTMDbSeasons(seasons) {
		if season.Season == target.Season {
			if target.Episode > season.EpisodeCount {
				return 0, false
			}
			return absolute + target.Episode, true
		}
		absolute += season.EpisodeCount
	}
	return 0, false
}

func TargetForAbsoluteEpisode(absolute int, seasons []TMDbSeason) (EpisodeCoordinate, bool) {
	if absolute <= 0 {
		return EpisodeCoordinate{}, false
	}
	remaining := absolute
	for _, season := range regularTMDbSeasons(seasons) {
		if remaining <= season.EpisodeCount {
			return EpisodeCoordinate{Season: season.Season, Episode: remaining}, true
		}
		remaining -= season.EpisodeCount
	}
	return EpisodeCoordinate{}, false
}

func RegularEpisodeCount(seasons []TMDbSeason) int {
	total := 0
	for _, season := range regularTMDbSeasons(seasons) {
		total += season.EpisodeCount
	}
	return total
}

func regularTMDbSeasons(seasons []TMDbSeason) []TMDbSeason {
	regular := make([]TMDbSeason, 0, len(seasons))
	for _, season := range seasons {
		if season.Season > 0 && season.EpisodeCount > 0 {
			regular = append(regular, season)
		}
	}
	sort.Slice(regular, func(left, right int) bool {
		return regular[left].Season < regular[right].Season
	})
	return regular
}

func resolveTargetTitle(seasons []TMDbSeason, target EpisodeCoordinate) (string, string) {
	for _, season := range seasons {
		if season.Season != target.Season {
			continue
		}
		if target.Episode <= 0 || target.Episode > season.EpisodeCount {
			return "", "mapping_target_out_of_range"
		}
		title := strings.TrimSpace(season.Titles[target.Episode])
		if title == "" {
			return "", "mapping_title_missing"
		}
		return title, ""
	}
	return "", "mapping_target_out_of_range"
}

func validSourceCoordinate(coordinate EpisodeCoordinate) bool {
	return coordinate.Season > 0 && coordinate.Episode > 0
}

func pendingMapping(absolute int, target EpisodeCoordinate, code string) EpisodeMappingResult {
	return EpisodeMappingResult{
		Status:          MappingPending,
		AbsoluteEpisode: absolute,
		Target:          target,
		MatchSource:     MappingMatchPending,
		ErrorCode:       code,
	}
}
