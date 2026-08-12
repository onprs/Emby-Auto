package domain

import "testing"

func TestBuildRSSIdentityUsesStablePriorityAndNormalization(t *testing.T) {
	tests := []struct {
		name  string
		input RSSIdentityInput
		want  string
	}{
		{
			name: "guid has priority over hash and URL",
			input: RSSIdentityInput{
				GUID: "  release-42  ",
				BTIH: "0123456789ABCDEF0123456789ABCDEF01234567",
				URL:  "https://example.test/release.torrent",
			},
			want: "guid:release-42",
		},
		{
			name:  "BTIH is case insensitive",
			input: RSSIdentityInput{BTIH: "0123456789ABCDEF0123456789ABCDEF01234567"},
			want:  "btih:0123456789abcdef0123456789abcdef01234567",
		},
		{
			name:  "magnet BTIH is extracted before URL identity",
			input: RSSIdentityInput{URL: "magnet:?dn=Show&xt=urn:btih:89ABCDEF0123456789ABCDEF0123456789ABCDEF"},
			want:  "btih:89abcdef0123456789abcdef0123456789abcdef",
		},
		{
			name:  "HTTP URL removes fragment default port and query order",
			input: RSSIdentityInput{URL: "HTTPS://EXAMPLE.TEST:443/releases/item.torrent?b=2&a=1#download"},
			want:  "url:https://example.test/releases/item.torrent?a=1&b=2",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := BuildRSSIdentity(test.input)
			if err != nil {
				t.Fatalf("BuildRSSIdentity() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("BuildRSSIdentity() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBuildRSSIdentityDeduplicatesEquivalentSignals(t *testing.T) {
	left, err := BuildRSSIdentity(RSSIdentityInput{
		GUID: "entry-7",
		URL:  "https://one.example/release.torrent",
	})
	if err != nil {
		t.Fatalf("left GUID identity error = %v", err)
	}
	right, err := BuildRSSIdentity(RSSIdentityInput{
		GUID: " entry-7 ",
		URL:  "https://two.example/different.torrent",
	})
	if err != nil {
		t.Fatalf("right GUID identity error = %v", err)
	}
	if left != right || left != "guid:entry-7" {
		t.Fatalf("GUID identities = %q and %q, want guid:entry-7", left, right)
	}

	left, err = BuildRSSIdentity(RSSIdentityInput{URL: "https://EXAMPLE.test:443/a?z=9&x=1#first"})
	if err != nil {
		t.Fatalf("left URL identity error = %v", err)
	}
	right, err = BuildRSSIdentity(RSSIdentityInput{URL: "https://example.test/a?x=1&z=9#second"})
	if err != nil {
		t.Fatalf("right URL identity error = %v", err)
	}
	if left != right || left != "url:https://example.test/a?x=1&z=9" {
		t.Fatalf("URL identities = %q and %q, want normalized URL identity", left, right)
	}
}

func TestCanAdjudicateRSSReleaseOnlyEscalatesDeterministicSoftFailures(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		uri      string
		includes []string
		excludes []string
		want     bool
	}{
		{
			name:  "standard coordinate is resolved without Agent",
			title: "[Group] Fixture Show - S01E03 [1080p]",
			uri:   "magnet:?xt=urn:btih:6123456789abcdef0123456789abcdef01234567",
			want:  false,
		},
		{
			name:  "conflicting coordinates require Agent",
			title: "[Group] Fixture Show S01E01 S01E02",
			uri:   "magnet:?xt=urn:btih:5123456789abcdef0123456789abcdef01234567",
			want:  true,
		},
		{name: "unknown title form requires Agent", title: "release naming unknown to deterministic rules", uri: "https://example.test/release.torrent", want: true},
		{name: "episode range is rejected without Agent", title: "Show S01E01-02", uri: "https://example.test/release.torrent", want: false},
		{name: "unsafe URI is not staged", title: "Show 01", uri: "file:///tmp/release.torrent", want: false},
		{name: "explicit exclusion is authoritative", title: "Show 01 CAM", uri: "https://example.test/release.torrent", excludes: []string{"CAM"}, want: false},
		{name: "explicit inclusion is authoritative", title: "Show 01 AVC", uri: "https://example.test/release.torrent", includes: []string{"HEVC"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CanAdjudicateRSSRelease(test.title, test.uri, 1, test.includes, test.excludes); got != test.want {
				t.Fatalf("CanAdjudicateRSSRelease() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestPlanRSSReleaseAdjudicationEscalatesDuplicateCoordinates(t *testing.T) {
	entries := []RSSFeedEntry{
		{GUID: "episode-1-primary", Title: "[Group A] Fixture Show - 01 [1080p]", DownloadURI: "magnet:?xt=urn:btih:1123456789abcdef0123456789abcdef01234567"},
		{GUID: "episode-1-alternate", Title: "[Group B] Fixture Show - 01 [1080p]", DownloadURI: "magnet:?xt=urn:btih:2123456789abcdef0123456789abcdef01234567"},
		{GUID: "episode-2", Title: "[Group A] Fixture Show - 02 [1080p]", DownloadURI: "magnet:?xt=urn:btih:3123456789abcdef0123456789abcdef01234567"},
		{GUID: "episode-2-excluded", Title: "[Group B] Fixture Show - 02 CAM", DownloadURI: "magnet:?xt=urn:btih:4123456789abcdef0123456789abcdef01234567"},
		{GUID: "unknown", Title: "Fixture release without a coordinate", DownloadURI: "magnet:?xt=urn:btih:5123456789abcdef0123456789abcdef01234567"},
	}
	got := PlanRSSReleaseAdjudication(entries, 1, nil, []string{"CAM"})
	want := []bool{true, true, false, false, true}
	if len(got) != len(want) {
		t.Fatalf("PlanRSSReleaseAdjudication() length = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("PlanRSSReleaseAdjudication()[%d] = %t, want %t", index, got[index], want[index])
		}
	}
}

func TestPlanRSSReleaseAdjudicationEscalatesDuplicateCoordinatesFromProductionFeed(t *testing.T) {
	const downloadBase = "https://example.test/releases/"
	entries := []RSSFeedEntry{
		{Title: "[LoliHouse] Grow Up Show ～向日葵马戏团～ / 成长秀 向日葵马戏团 / Grow Up Show - Himawari no Circus-dan - 05 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕]", DownloadURI: downloadBase + "release-05.torrent"},
		{Title: "[LoliHouse] Grow Up Show ～向日葵马戏团～ / 成长秀 向日葵马戏团 / Grow Up Show - Himawari no Circus-dan - 03 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕]", DownloadURI: downloadBase + "release-03.torrent"},
		{Title: "[LoliHouse] Grow Up Show ～向日葵马戏团～ / 成长秀 向日葵马戏团 / Grow Up Show - Himawari no Circus-dan - 04 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕]", DownloadURI: downloadBase + "release-04.torrent"},
		{Title: "[LoliHouse] Grow Up Show ～向日葵马戏团～ / 成长秀 向日葵马戏团 / Grow Up Show - Himawari no Circus-dan - 02 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕]", DownloadURI: downloadBase + "release-02.torrent"},
		{Title: "[LoliHouse] Grow Up Show ～向日葵马戏团～ / 成长秀 向日葵马戏团 / Grow Up Show - Himawari no Circus-dan - 01 [WebRip 1080p HEVC-10bit AAC][简繁内封字幕]", DownloadURI: downloadBase + "release-01-primary.torrent"},
		{Title: "[喵萌奶茶屋&LoliHouse] Grow Up Show ～向日葵马戏团～ / 成长秀 向日葵马戏团 / Grow Up Show - Himawari no Circus-dan - 01 [WebRip 1080p HEVC-10bit AAC][简繁日内封字幕]", DownloadURI: downloadBase + "release-01-alternate.torrent"},
	}

	got := PlanRSSReleaseAdjudication(entries, 1, nil, []string{"合集"})
	want := []bool{false, false, false, false, true, true}
	for index := range want {
		if got[index] != want[index] {
			analysis := AnalyzeRSSRelease(entries[index].Title, entries[index].DownloadURI, 1, nil, []string{"合集"})
			t.Fatalf("PlanRSSReleaseAdjudication()[%d] = %t, want %t; analysis = %#v", index, got[index], want[index], analysis)
		}
	}
}

func TestBuildRSSIdentityRejectsEntryWithoutStableSignal(t *testing.T) {
	_, err := BuildRSSIdentity(RSSIdentityInput{Title: "Show latest"})
	if err == nil {
		t.Fatal("BuildRSSIdentity() error = nil, want published time requirement")
	}
}

func TestAnalyzeRSSReleaseUsesTitleCoordinatesAndRejectsNonEpisodes(t *testing.T) {
	tests := []struct {
		name          string
		title         string
		downloadURI   string
		defaultSeason int
		wantSeason    int
		wantEpisode   int
		wantDownload  bool
		wantReason    string
	}{
		{
			name:          "explicit season episode",
			title:         "[Group] Show S02E03 1080p",
			downloadURI:   "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
			defaultSeason: 1,
			wantSeason:    2,
			wantEpisode:   3,
			wantDownload:  true,
		},
		{
			name:          "subscription season fallback",
			title:         "[Group] Show - 07 [1080p]",
			downloadURI:   "https://example.test/show-07.torrent",
			defaultSeason: 4,
			wantSeason:    4,
			wantEpisode:   7,
			wantDownload:  true,
		},
		{
			name:          "uppercase revision marker",
			title:         "[Group][Show][02V2][HEVC-10bit 1080p][MKV]",
			downloadURI:   "https://example.test/show-02v2.torrent",
			defaultSeason: 1,
			wantSeason:    1,
			wantEpisode:   2,
			wantDownload:  true,
		},
		{
			name:          "chinese season marker is not the episode",
			title:         "【TSDM字幕组】[Re:从零开始的异世界生活 第4季][10][HEVC-10bit 1080p AAC][MKV][简日内封字幕][Re Zero kara Hajimeru Isekai Seikatsu 4th Season]",
			downloadURI:   "https://example.test/re-zero-s04e10.torrent",
			defaultSeason: 4,
			wantSeason:    4,
			wantEpisode:   10,
			wantDownload:  true,
		},
		{
			name:          "multilingual title punctuation does not truncate episode",
			title:         "[Group] Example A. / Example B. / Example C. - 13 [WebRip 1080p HEVC-10bit AAC][END]",
			downloadURI:   "https://example.test/show-13.torrent",
			defaultSeason: 1,
			wantSeason:    1,
			wantEpisode:   13,
			wantDownload:  true,
		},
		{
			name:          "conflicting peer evidence is not guessed",
			title:         "[Group] Show EP03 #04 [1080p]",
			downloadURI:   "https://example.test/conflict.torrent",
			defaultSeason: 1,
			wantReason:    "episode_ambiguous",
		},
		{
			name:          "creditless extra",
			title:         "Show NCOP 01",
			downloadURI:   "https://example.test/ncop.torrent",
			defaultSeason: 1,
			wantReason:    "non_episode_extra",
		},
		{
			name:          "complete compilation",
			title:         "Show Complete Edition",
			downloadURI:   "https://example.test/complete.torrent",
			defaultSeason: 1,
			wantReason:    "non_episode_extra",
		},
		{
			name:          "complete prefix is not a compilation",
			title:         "Completely different corrected release",
			downloadURI:   "https://example.test/corrected.torrent",
			defaultSeason: 1,
			wantReason:    "episode_not_detected",
		},
		{
			name:          "episode range",
			title:         "Show 01-12 Complete",
			downloadURI:   "https://example.test/pack.torrent",
			defaultSeason: 1,
			wantReason:    "episode_range_batch",
		},
		{
			name:          "marked episode range",
			title:         "Show S01E01-E02 1080p",
			downloadURI:   "https://example.test/marked-pack.torrent",
			defaultSeason: 1,
			wantReason:    "episode_range_batch",
		},
		{
			name:          "missing direct download",
			title:         "Show - 08",
			defaultSeason: 1,
			wantSeason:    1,
			wantEpisode:   8,
			wantReason:    "download_uri_missing",
		},
		{
			name:          "feed order is not an episode",
			title:         "Show latest release",
			downloadURI:   "https://example.test/latest.torrent",
			defaultSeason: 1,
			wantReason:    "episode_not_detected",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := AnalyzeRSSRelease(test.title, test.downloadURI, test.defaultSeason, nil, nil)
			if got.SourceSeason != test.wantSeason || got.SourceEpisode != test.wantEpisode || got.Downloadable != test.wantDownload {
				t.Fatalf("AnalyzeRSSRelease() = %#v, want season=%d episode=%d downloadable=%t", got, test.wantSeason, test.wantEpisode, test.wantDownload)
			}
			if test.wantReason != "" && !rssContainsString(got.RejectionReasons, test.wantReason) {
				t.Fatalf("rejection reasons = %v, want %q", got.RejectionReasons, test.wantReason)
			}
			if test.wantDownload && len(got.RejectionReasons) != 0 {
				t.Fatalf("downloadable release reasons = %v, want none", got.RejectionReasons)
			}
		})
	}
}

func TestAnalyzeRSSReleaseAppliesIncludeAndExcludeKeywords(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		includes []string
		excludes []string
		want     string
	}{
		{name: "empty rules accept", title: "[Group] Show S01E02"},
		{name: "include matches case insensitively", title: "[Group] Show S01E02 CHS", includes: []string{"chs"}},
		{name: "include does not match", title: "[Group] Show S01E02 CHT", includes: []string{"简日"}, want: "title_include_mismatch"},
		{name: "exclude matches", title: "[Group] Show S01E02 720P", excludes: []string{"720p"}, want: "title_excluded"},
		{name: "exclude takes precedence", title: "[Group] Show S01E02 CHS 720p", includes: []string{"CHS"}, excludes: []string{"720P"}, want: "title_excluded"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := AnalyzeRSSRelease(test.title, "https://example.test/episode.torrent", 1, test.includes, test.excludes)
			if test.want == "" {
				if !got.Downloadable || len(got.RejectionReasons) != 0 {
					t.Fatalf("AnalyzeRSSRelease() = %#v, want downloadable", got)
				}
				return
			}
			if got.Downloadable || !rssContainsString(got.RejectionReasons, test.want) {
				t.Fatalf("AnalyzeRSSRelease() = %#v, want rejection %q", got, test.want)
			}
		})
	}
}

func rssContainsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
