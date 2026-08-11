package domain

import "testing"

func TestParseRSSReleaseCoordinateRecognizesRankedReleaseForms(t *testing.T) {
	tests := []struct {
		name          string
		title         string
		defaultSeason int
		wantSeason    int
		wantEpisode   int
		wantEvidence  string
	}{
		{
			name:          "multilingual title punctuation remains intact",
			title:         "[Group] Example A. / Example B. / Example C. - 01 [WebRip 1080p HEVC-10bit AAC]",
			defaultSeason: 1, wantSeason: 1, wantEpisode: 1, wantEvidence: "release_dash",
		},
		{name: "compact season episode", title: "[Group] Show.S02E03.1080p", defaultSeason: 1, wantSeason: 2, wantEpisode: 3, wantEvidence: "season_episode"},
		{name: "x notation", title: "Show 2x03 WEB-DL", defaultSeason: 1, wantSeason: 2, wantEpisode: 3, wantEvidence: "season_x_episode"},
		{name: "season episode words", title: "Show Season 2 Episode 03", defaultSeason: 1, wantSeason: 2, wantEpisode: 3, wantEvidence: "season_episode_words"},
		{name: "east asian coordinate", title: "Show 第2季 第三话", defaultSeason: 1, wantSeason: 2, wantEpisode: 3, wantEvidence: "east_asian_season_episode"},
		{name: "episode marker", title: "Show EP 29 WEB-DL", defaultSeason: 4, wantSeason: 4, wantEpisode: 29, wantEvidence: "episode_marker"},
		{name: "hash marker", title: "Show #07 1080p", defaultSeason: 3, wantSeason: 3, wantEpisode: 7, wantEvidence: "hash_marker"},
		{name: "east asian large number", title: "Show 第一百零二話", defaultSeason: 5, wantSeason: 5, wantEpisode: 102, wantEvidence: "east_asian_episode"},
		{name: "bracketed episode", title: "[Group] Show [12] [1080p]", defaultSeason: 1, wantSeason: 1, wantEpisode: 12, wantEvidence: "bracketed_episode"},
		{name: "dotted episode", title: "Show.03.1080p.HEVC", defaultSeason: 1, wantSeason: 1, wantEpisode: 3, wantEvidence: "delimited_episode"},
		{name: "standalone season marker", title: "Show 2nd Season - 03 [1080p]", defaultSeason: 1, wantSeason: 2, wantEpisode: 3, wantEvidence: "release_dash"},
		{name: "full width notation", title: "Show Ｓ０３Ｅ０８ １０８０ｐ", defaultSeason: 1, wantSeason: 3, wantEpisode: 8, wantEvidence: "season_episode"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ParseRSSReleaseCoordinate(test.title, test.defaultSeason)
			if got.Status != RSSCoordinateMatched || got.SourceSeason != test.wantSeason || got.SourceEpisode != test.wantEpisode || got.Evidence != test.wantEvidence {
				t.Fatalf("ParseRSSReleaseCoordinate() = %#v, want S%02dE%02d via %s", got, test.wantSeason, test.wantEpisode, test.wantEvidence)
			}
		})
	}
}

func TestParseRSSReleaseCoordinateUsesEvidencePriorityAndRejectsConflicts(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		wantStatus  RSSCoordinateParseStatus
		wantSeason  int
		wantEpisode int
	}{
		{
			name: "strong evidence ignores lower priority number", title: "Show EP03 [04] 1080p",
			wantStatus: RSSCoordinateMatched, wantSeason: 1, wantEpisode: 3,
		},
		{
			name: "same coordinate from peer evidence is accepted", title: "Show EP03 #03 1080p",
			wantStatus: RSSCoordinateMatched, wantSeason: 1, wantEpisode: 3,
		},
		{name: "peer episode markers conflict", title: "Show EP03 #04 1080p", wantStatus: RSSCoordinateAmbiguous},
		{name: "repeated episode marker conflicts", title: "Show EP03 E04 1080p", wantStatus: RSSCoordinateAmbiguous},
		{name: "repeated coordinate marker conflicts", title: "Show S01E03 S01E04 1080p", wantStatus: RSSCoordinateAmbiguous},
		{name: "standalone season markers conflict", title: "Show Season 1 / 2nd Season - 03", wantStatus: RSSCoordinateAmbiguous},
		{name: "audio channels are not an episode", title: "Show AAC 5.1 1080p", wantStatus: RSSCoordinateNotFound},
		{name: "codec number is not an episode", title: "Show H.265 AAC 10bit 2026", wantStatus: RSSCoordinateNotFound},
		{name: "resolution and year are not episodes", title: "Show 1920x1080 1080p 2026", wantStatus: RSSCoordinateNotFound},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ParseRSSReleaseCoordinate(test.title, 1)
			if got.Status != test.wantStatus || got.SourceSeason != test.wantSeason || got.SourceEpisode != test.wantEpisode {
				t.Fatalf("ParseRSSReleaseCoordinate() = %#v, want status=%s S%02dE%02d", got, test.wantStatus, test.wantSeason, test.wantEpisode)
			}
		})
	}
}
