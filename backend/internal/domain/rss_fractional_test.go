package domain

import "testing"

func TestRSSFractionalEpisodeRequiresExplicitMapping(t *testing.T) {
	for _, title := range []string{
		"Series S01E12.5 [1080p]", "Series EP12.5 [1080p]", "Series 1x12.5 [1080p]",
		"Series Season 1 Episode 12.5", "Series 第十二点五集 S01E12.5", "Series 第12.5集",
		"Series 第1季 第12.5集", "Series [12.5]", "Series - 12.5 [1080p]", "Series S01E12.5v2",
		"Series Ｓ０１Ｅ１２．５", "Series 第１２．５集",
	} {
		t.Run(title, func(t *testing.T) {
			result := AnalyzeRSSRelease(title, "https://example.test/episode.torrent", 1, nil, nil)
			if result.Downloadable || result.SourceSeason != 0 || result.SourceEpisode != 0 ||
				!rssContainsString(result.RejectionReasons, "fractional_episode_requires_explicit_mapping") {
				t.Fatalf("小数集分析=%+v，期望拒绝整数正片映射", result)
			}
			if CanAdjudicateRSSRelease(title, "https://example.test/episode.torrent", 1, nil, nil) {
				t.Fatal("小数集的硬拒绝不能由整数坐标裁决绕过")
			}
		})
	}
}

func TestRSSRegularEpisodeKeepsTechnicalDecimalMetadata(t *testing.T) {
	for _, title := range []string{"Series S01E03 [AAC 5.1]", "Series EP03 [5.1] [H.265]", "Series - 03 [7.1ch]"} {
		t.Run(title, func(t *testing.T) {
			result := AnalyzeRSSRelease(title, "https://example.test/episode.torrent", 1, nil, nil)
			if !result.Downloadable || result.SourceEpisode != 3 {
				t.Fatalf("普通正片被音频或编码信息误拒绝：%+v", result)
			}
		})
	}
}
