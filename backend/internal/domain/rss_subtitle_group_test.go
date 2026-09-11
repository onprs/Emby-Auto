package domain

import (
	"reflect"
	"testing"
)

func TestExtractRSSSubtitleGroupCandidatesPrefersMaintainedNames(t *testing.T) {
	feed := RSSFeed{
		Title: "Show RSS",
		Entries: []RSSFeedEntry{
			{Title: "【LoliHouse】 Show - 01 [1080p]"},
			{Title: "[OtherGroup] Show - 02 [1080p]"},
		},
	}
	got := ExtractRSSSubtitleGroupCandidates(feed, []string{"OtherGroup", "LoliHouse"})
	want := []string{"LoliHouse", "OtherGroup"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractRSSSubtitleGroupCandidates() = %#v, want %#v", got, want)
	}
}

func TestExtractRSSSubtitleGroupCandidatesUsesFeedTitleAndEntryFallbacks(t *testing.T) {
	tests := []struct {
		name       string
		feed       RSSFeed
		maintained []string
		want       []string
	}{
		{
			name:       "maintained name can appear in feed title",
			feed:       RSSFeed{Title: "LoliHouse Anime RSS"},
			maintained: []string{"LoliHouse"},
			want:       []string{"LoliHouse"},
		},
		{
			name: "unknown group falls back to first entry bracket",
			feed: RSSFeed{Entries: []RSSFeedEntry{{Title: "[NewGroup] Show - 01 [1080p]"}}},
			want: []string{"NewGroup"},
		},
		{
			name: "quality bracket is not a group",
			feed: RSSFeed{Title: "[1080p] Show RSS", Entries: []RSSFeedEntry{{Title: "Show - 01 [1080p]"}}},
			want: []string{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ExtractRSSSubtitleGroupCandidates(test.feed, test.maintained)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ExtractRSSSubtitleGroupCandidates() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestExtractRSSSubtitleGroupCandidatesSupportsFullWidthAndNestedLabels(t *testing.T) {
	feed := RSSFeed{Entries: []RSSFeedEntry{{Title: "「ＬｏｌｉＨｏｕｓｅ」 [1080p] Show - 01"}}}
	got := ExtractRSSSubtitleGroupCandidates(feed, nil)
	want := []string{"LoliHouse"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractRSSSubtitleGroupCandidates() = %#v, want %#v", got, want)
	}
}

func TestNormalizeRSSSubtitleGroupNameRemovesPresentationWrappers(t *testing.T) {
	if got := NormalizeRSSSubtitleGroupName(" 【 ＬｏｌｉＨｏｕｓｅ 】 "); got != "LoliHouse" {
		t.Fatalf("NormalizeRSSSubtitleGroupName() = %q, want LoliHouse", got)
	}
}
