package domain

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRSSSubtitleGroupCandidateRegression(t *testing.T) {
	tests := []struct {
		name       string
		title      string
		maintained []string
		want       []string
	}{
		{"词内子串不覆盖真实标签", "[ReleaseTeam] Cometary Adventure - 01", []string{"Comet"}, []string{"ReleaseTeam"}},
		{"保留精确标签的标点", "[Release-Team] 动画 - 01", []string{"Release Team", "Release-Team"}, []string{"Release-Team"}},
		{"不同分隔符名称独立出现", "[Release-Team][Release Team] 动画 - 01", []string{"Release Team", "Release-Team"}, []string{"Release-Team", "Release Team"}},
		{"全角方括号", "［ReleaseTeam］ 动画 - 01", nil, []string{"ReleaseTeam"}},
		{"同类嵌套括号", "[[ReleaseTeam]] 动画 - 01", nil, []string{"ReleaseTeam"}},
		{"混合嵌套括号", "【[「ReleaseTeam」]】 动画 - 01", nil, []string{"ReleaseTeam"}},
		{"不匹配的括号", "[ReleaseTeam) 动画 - 01", nil, []string{}},
		{"缺少闭括号", "[[ReleaseTeam] 动画 - 01", nil, []string{}},
		{"完整元数据标签", "[BDRip][简体中文][AV1][10bit][Web-DL][CHS&JPN][01v2][S01E01] 动画", nil, []string{}},
		{"混合元数据标签", "[1080p HEVC AAC][繁體中文] 动画 - 01", nil, []string{}},
		{"人工维护的编码字样名称", "[AACStudio] 动画 - 01", []string{"AACStudio"}, []string{"AACStudio"}},
		{"未知名称中的编码字样", "[AACStudio] 动画 - 01", nil, []string{"AACStudio"}},
		{"人工维护的单字符名称", "[A] 动画 - 01", []string{"A"}, []string{"A"}},
		{"人工维护的数字名称", "[123] 动画 - 01", []string{"123"}, []string{"123"}},
		{"人工维护的标点名称", "[!!!] 动画 - 01", []string{"!!!"}, []string{"!!!"}},
		{"短名称仅匹配完整标签", "动画 A 第123话 !!!", []string{"A", "123", "!!!"}, []string{}},
		{"已维护名称保留规范大小写", "【ＲＥＬＥＡＳＥＴＥＡＭ】 动画 - 01", []string{"ReleaseTeam"}, []string{"ReleaseTeam"}},
		{"中文词内子串不命中", "[ReleaseTeam] 动漫国度 - 01", []string{"动漫国"}, []string{"ReleaseTeam"}},
		{"组合字符保持词边界", "Cafe\u0301 动画 - 01", []string{"Cafe"}, []string{}},
		{"未知标签优先于正文名称", "[ReleaseTeam] Comet Adventure - 01", []string{"Comet"}, []string{"ReleaseTeam", "Comet"}},
		{"完整标签不拆成较短维护名", "[Release Team] 动画 - 01", []string{"Release Team", "Team"}, []string{"Release Team"}},
		{"未知连字符名称不拆成维护名", "[Northern-Team] 动画 - 01", []string{"Team"}, []string{"Northern-Team"}},
		{"未知空格名称不拆成维护名", "[Northern Team] 动画 - 01", []string{"Team"}, []string{"Northern Team"}},
		{"联合发布保留名称内部连字符", "[Alpha-Team & Beta] 动画 - 01", []string{"Alpha-Team", "Beta"}, []string{"Alpha-Team", "Beta"}},
		{"联合发布保留名称内部符号", "[Alpha&Beta + Gamma] 动画 - 01", []string{"Alpha&Beta", "Gamma"}, []string{"Alpha&Beta", "Gamma"}},
		{"重复名称仅在明确联合边界命中", "[Team Edition & Team] 动画 - 01", []string{"Team"}, []string{"Team"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			feed := RSSFeed{Entries: []RSSFeedEntry{{Title: test.title}}}
			if got := ExtractRSSSubtitleGroupCandidates(feed, test.maintained); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("候选 = %#v，期望 %#v", got, test.want)
			}
		})
	}
}

func TestRSSSubtitleGroupFallbackRemainsAvailableAcrossSources(t *testing.T) {
	feed := RSSFeed{Entries: []RSSFeedEntry{
		{Title: "[ReleaseTeam] 动画 - 01"},
		{Title: "[TranslationTeam] 动画 - 02"},
	}}
	want := []string{"ReleaseTeam", "TranslationTeam"}
	if got := ExtractRSSSubtitleGroupCandidates(feed, []string{"ReleaseTeam"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("候选 = %#v，期望 %#v", got, want)
	}
}

func TestRSSSubtitleGroupRankingIgnoresUnrelatedMaintenance(t *testing.T) {
	feed := RSSFeed{Entries: []RSSFeedEntry{{Title: "[Aster&ZenithSubtitleTeam] 动画 - 01"}}}
	want := []string{"ZenithSubtitleTeam", "Aster"}
	maintained := []string{"Aster", "ZenithSubtitleTeam"}
	for index := 0; index < 100; index++ {
		maintained = append(maintained, fmt.Sprintf("Unrelated%03d", index))
	}
	for _, names := range [][]string{{"Aster", "ZenithSubtitleTeam"}, {"ZenithSubtitleTeam", "Aster"}, maintained} {
		if got := ExtractRSSSubtitleGroupCandidates(feed, names); !reflect.DeepEqual(got, want) {
			t.Fatalf("候选 = %#v，期望 %#v", got, want)
		}
	}
}

func TestRSSSubtitleGroupNormalizationKeepsSeparateLabelsIntact(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{" 【 ＲｅｌｅａｓｅＴｅａｍ 】 ", "ReleaseTeam"},
		{"[[ReleaseTeam]]", "ReleaseTeam"},
		{"[Release][Team]", "[Release][Team]"},
		{"[Release Team)", "[Release Team)"},
		{"Release\t Team", "Release Team"},
	} {
		if got := NormalizeRSSSubtitleGroupName(test.input); got != test.want {
			t.Errorf("规范化 %q = %q，期望 %q", test.input, got, test.want)
		}
	}
	if RSSSubtitleGroupNameKey("Release Team") == RSSSubtitleGroupNameKey("Release-Team") {
		t.Fatal("空格与连字符不能合并为同一名称身份")
	}
}

func BenchmarkRSSSubtitleGroupCandidates(b *testing.B) {
	feed := RSSFeed{Title: "动画聚合订阅"}
	for index := 0; index < 20; index++ {
		feed.Entries = append(feed.Entries, RSSFeedEntry{Title: fmt.Sprintf("[ReleaseTeam] 动画作品 / Animation Title - %02d [1080p][Web-DL][简体中文]", index+1)})
	}
	for _, count := range []int{50, 500, 5000} {
		names := make([]string, 0, count)
		for index := 0; index < count-1; index++ {
			names = append(names, fmt.Sprintf("MaintainedSubtitleTeam%05d", index))
		}
		names = append(names, "ReleaseTeam")
		b.Run(fmt.Sprintf("维护记录%d", count), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ExtractRSSSubtitleGroupCandidates(feed, names)
			}
		})
	}
}

func TestRSSSubtitleGroupCandidatesStayBounded(t *testing.T) {
	feed := RSSFeed{}
	for index := 0; index < 21; index++ {
		feed.Entries = append(feed.Entries, RSSFeedEntry{Title: fmt.Sprintf("[Group%02d] 动画 - %02d", index, index)})
	}
	got := ExtractRSSSubtitleGroupCandidates(feed, nil)
	if len(got) != 8 || strings.Join(got, ",") != "Group00,Group01,Group02,Group03,Group04,Group05,Group06,Group07" {
		t.Fatalf("候选上限和标题顺序 = %#v", got)
	}
}
