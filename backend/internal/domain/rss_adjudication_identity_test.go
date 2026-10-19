package domain

import "testing"

func TestRSSAdjudicationCountsDistinctContentIdentities(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		first  RSSFeedEntry
		second RSSFeedEntry
		want   bool
	}{
		{
			name:   "同一哈希的十六进制和 base32 地址",
			first:  RSSFeedEntry{GUID: "first", DownloadURI: "magnet:?xt=urn:btih:0000000000000000000000000000000000000000"},
			second: RSSFeedEntry{GUID: "second", DownloadURI: "magnet:?xt=urn:btih:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		},
		{
			name:   "同一地址的参数顺序与片段",
			first:  RSSFeedEntry{DownloadURI: "HTTPS://EXAMPLE.TEST:443/item.torrent?b=2&a=1#first"},
			second: RSSFeedEntry{DownloadURI: "https://example.test/item.torrent?a=1&b=2#second"},
		},
		{
			name:   "复用 GUID 的不同种子仍需裁决",
			first:  RSSFeedEntry{GUID: "reused", DownloadURI: "magnet:?xt=urn:btih:1111111111111111111111111111111111111111"},
			second: RSSFeedEntry{GUID: "reused", DownloadURI: "magnet:?xt=urn:btih:2222222222222222222222222222222222222222"},
			want:   true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.first.Title = "Series S01E01"
			testCase.second.Title = "Series S01E01"
			result := PlanRSSReleaseAdjudication([]RSSFeedEntry{testCase.first, testCase.second}, 1, nil, nil)
			if len(result) != 2 || result[0] != testCase.want || result[1] != testCase.want {
				t.Fatalf("同集资源裁决=%v，期望每项=%t", result, testCase.want)
			}
		})
	}
}
