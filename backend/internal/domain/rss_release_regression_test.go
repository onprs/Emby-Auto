package domain

import "testing"

// 小数集需要保留小数或进入待解析状态，不能直接占用整数正片的目标。
func TestRSSAuditFractionalEpisodesDoNotBecomeRegularEpisodes(t *testing.T) {
	for _, title := range []string{
		"Audit Series S01E12.5 [1080p]",
		"Audit Series EP12.5 [1080p]",
		"Audit Series 第12.5集 [1080p]",
		"Audit Series [12.5] [1080p]",
	} {
		t.Run(title, func(t *testing.T) {
			analysis := AnalyzeRSSRelease(title, "https://example.test/fractional.torrent", 1, nil, nil)
			if analysis.Downloadable {
				t.Fatalf("小数集被识别为可直接下载的整数集：S%02dE%02d，分析结果=%+v", analysis.SourceSeason, analysis.SourceEpisode, analysis)
			}
		})
	}
}

func TestRSSAuditRepeatedIdentityDoesNotRequireAdjudication(t *testing.T) {
	entry := RSSFeedEntry{
		GUID: "same-release", Title: "Audit Series S01E01 [1080p]",
		DownloadURI: "magnet:?xt=urn:btih:1111111111111111111111111111111111111111",
	}
	for index, needsAgent := range PlanRSSReleaseAdjudication([]RSSFeedEntry{entry, entry}, 1, nil, nil) {
		if needsAgent {
			t.Fatalf("同一种子的重复记录触发 Agent 裁决：索引=%d", index)
		}
	}
}
