package service

import (
	"cmp"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

// 保留优化前的排序规则，作为数据库分页兼容性回归的独立参考。
func compareAcquisitionContent(left, right domain.AcquisitionView) int {
	if comparison := strings.Compare(strings.ToLower(acquisitionSortTitle(left)), strings.ToLower(acquisitionSortTitle(right))); comparison != 0 {
		return comparison
	}
	if left.MediaType == domain.TaskMediaMovie || right.MediaType == domain.TaskMediaMovie {
		if comparison := strings.Compare(string(left.MediaType), string(right.MediaType)); comparison != 0 {
			return comparison
		}
		return cmp.Compare(left.ReleaseYear, right.ReleaseYear)
	}
	return compareOptionalEpisode(left.SourceSeason, left.SourceEpisode, right.SourceSeason, right.SourceEpisode)
}

func sortAcquisitionViews(views []domain.AcquisitionView, field string, direction int) {
	sort.SliceStable(views, func(i, j int) bool {
		left, right := views[i], views[j]
		comparison := 0
		switch field {
		case "content":
			comparison = compareAcquisitionContent(left, right)
		case "source_kind":
			comparison = strings.Compare(left.SourceKind, right.SourceKind)
		case "progress":
			comparison = cmp.Compare(left.OverallProgress, right.OverallProgress)
		default:
			comparison = compareTime(left.UpdatedAt, right.UpdatedAt)
		}
		if comparison == 0 {
			comparison = strings.Compare(left.ID.String(), right.ID.String())
		}
		return direction*comparison < 0
	})
}

func compareOptionalEpisode(leftSeason, leftEpisode, rightSeason, rightEpisode *int) int {
	leftSeasonValue, rightSeasonValue := math.MaxInt, math.MaxInt
	leftEpisodeValue, rightEpisodeValue := math.MaxInt, math.MaxInt
	if leftSeason != nil {
		leftSeasonValue = *leftSeason
	}
	if rightSeason != nil {
		rightSeasonValue = *rightSeason
	}
	if comparison := cmp.Compare(leftSeasonValue, rightSeasonValue); comparison != 0 {
		return comparison
	}
	if leftEpisode != nil {
		leftEpisodeValue = *leftEpisode
	}
	if rightEpisode != nil {
		rightEpisodeValue = *rightEpisode
	}
	return cmp.Compare(leftEpisodeValue, rightEpisodeValue)
}

func compareTime(left, right time.Time) int {
	if left.Before(right) {
		return -1
	}
	if left.After(right) {
		return 1
	}
	return 0
}
