//go:build rss_audit

package worker

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

func BenchmarkRSSAuditRealtimeTargetMatch(b *testing.B) {
	for _, count := range []int{100, 500, 1000, 2000} {
		b.Run(fmt.Sprintf("episodes_%d", count), func(b *testing.B) {
			targets := make([]rssRealtimeTarget, count)
			episodes := make([]domain.EmbyLibraryItemCatalog, count)
			for index := range targets {
				tmdbID := int64(10000 + index)
				season, episode := 1, index+1
				targets[index] = rssRealtimeTarget{target: domain.EpisodeCoordinate{Season: season, Episode: episode}, tmdbEpisodeID: &tmdbID}
				episodes[index] = domain.EmbyLibraryItemCatalog{
					ItemType: "Episode", Path: "/synthetic/episode.mkv", SeasonNumber: &season, EpisodeNumber: &episode,
					ProviderIDs: map[string]string{"Tmdb": strconv.FormatInt(tmdbID, 10)},
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				matches := matchRealtimeTargets(targets, episodes)
				if len(matches) != count || !matches[count-1].present {
					b.Fatal("完整目录的末集匹配失败")
				}
			}
		})
	}
}
