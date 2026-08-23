package domain

import (
	"errors"
	"testing"
)

// 顶层复合发布标签含 SP 时，主视频/字幕应按扩展和坐标正常分类，确定性选择正片
func TestClassifyTopCompoundReleaseDoesNotPolluteChildren(t *testing.T) {
	top := "SyntheticTitle S02 SP Limited Edition 1080p"
	files := []DownloadFile{
		{Index: 0, RelativePath: top + "/SyntheticTitle - 01.mkv", SizeBytes: 1200},
		{Index: 1, RelativePath: top + "/SyntheticTitle - 01.ass", SizeBytes: 80},
		{Index: 2, RelativePath: top + "/SyntheticTitle - 02.mkv", SizeBytes: 1300},
		{Index: 3, RelativePath: top + "/SyntheticTitle - 02.ass", SizeBytes: 85},
	}
	classified, err := ClassifyDownloadFiles(files, FileSelectionOptions{DefaultSeason: 1})
	if err != nil {
		t.Fatalf("ClassifyDownloadFiles error = %v", err)
	}
	for i, f := range classified {
		if f.Kind == MediaExtra {
			t.Fatalf("file %d %q classified as extra, want video/subtitle", i, f.RelativePath)
		}
	}
	if classified[0].Kind != MediaVideo || classified[1].Kind != MediaSubtitle {
		t.Fatalf("kinds = %q %q, want video/subtitle", classified[0].Kind, classified[1].Kind)
	}
	result, err := SelectDownloadFiles(files, FileSelectionOptions{DefaultSeason: 1})
	if err != nil {
		t.Fatalf("SelectDownloadFiles error = %v", err)
	}
	if len(result.Episodes) != 2 {
		t.Fatalf("episodes = %d, want 2", len(result.Episodes))
	}
	if result.Episodes[0].Video.Index != 0 || result.Episodes[1].Video.Index != 2 {
		t.Fatalf("selected videos = %v", result.Episodes)
	}
	if len(result.Episodes[0].Subtitles) != 1 || result.Episodes[0].Subtitles[0].Index != 1 {
		t.Fatalf("episode 1 subtitles = %#v", result.Episodes[0].Subtitles)
	}
}

// basename extra 与独立 extra 目录仍为 extra；顶层独立 extra 仍硬拒绝
func TestClassifyBasenameAndIndependentExtraDirectoriesRemainExtra(t *testing.T) {
	// basename 含 extra token
	baseExtra := DownloadFile{Index: 0, RelativePath: "Pack/Show NCOP.mkv", SizeBytes: 1000}
	kind := classifyDownloadPath(baseExtra.RelativePath)
	if kind != MediaExtra {
		t.Fatalf("basename NCOP kind = %q, want extra", kind)
	}
	// 嵌套独立 extra 目录 SP
	nestedSP := DownloadFile{Index: 1, RelativePath: "Pack/SP/Show - 01.mkv", SizeBytes: 1000}
	if got := classifyDownloadPath(nestedSP.RelativePath); got != MediaExtra {
		t.Fatalf("nested SP dir kind = %q, want extra", got)
	}
	// 嵌套独立 extra 目录 NCOP
	nestedNCOP := DownloadFile{Index: 2, RelativePath: "Pack/NCOP/Show - 01.mkv", SizeBytes: 1000}
	if got := classifyDownloadPath(nestedNCOP.RelativePath); got != MediaExtra {
		t.Fatalf("nested NCOP dir kind = %q, want extra", got)
	}
	// 嵌套独立 extra 目录 PV 带序号
	nestedPV := DownloadFile{Index: 3, RelativePath: "Pack/PV01/Show - 01.mkv", SizeBytes: 1000}
	if got := classifyDownloadPath(nestedPV.RelativePath); got != MediaExtra {
		t.Fatalf("nested PV01 dir kind = %q, want extra", got)
	}
	// 嵌套独立 extra 目录 Bonus
	nestedBonus := DownloadFile{Index: 4, RelativePath: "Pack/Bonus/Show - 01.mkv", SizeBytes: 1000}
	if got := classifyDownloadPath(nestedBonus.RelativePath); got != MediaExtra {
		t.Fatalf("nested Bonus dir kind = %q, want extra", got)
	}
	// 带序号的独立目录 SP 01
	nestedSPNum := DownloadFile{Index: 5, RelativePath: "Pack/SP 01/Show - 01.mkv", SizeBytes: 1000}
	if got := classifyDownloadPath(nestedSPNum.RelativePath); got != MediaExtra {
		t.Fatalf("nested SP 01 dir kind = %q, want extra", got)
	}
	// 顶层独立 extra 目录 SP/01.mkv 仍应为 extra，整体 extra-only 应 ErrNoMainVideo
	topIndependent := []DownloadFile{
		{Index: 0, RelativePath: "SP/Show - 01.mkv", SizeBytes: 1000},
		{Index: 1, RelativePath: "SP/Show - 02.mkv", SizeBytes: 1000},
	}
	for _, f := range topIndependent {
		if got := classifyDownloadPath(f.RelativePath); got != MediaExtra {
			t.Fatalf("top independent SP %q kind = %q, want extra", f.RelativePath, got)
		}
	}
	if _, err := SelectDownloadFiles(topIndependent, FileSelectionOptions{DefaultSeason: 1}); !errors.Is(err, ErrNoMainVideo) {
		t.Fatalf("extra-only top SP expected ErrNoMainVideo, got %v", err)
	}
	// 同季同目录下 extra-only
	extraOnly := []DownloadFile{
		{Index: 0, RelativePath: "Pack/Show NCOP.mkv", SizeBytes: 1000},
		{Index: 1, RelativePath: "Pack/Show Nced.mkv", SizeBytes: 1000},
		{Index: 2, RelativePath: "Pack/sample.mkv", SizeBytes: 1000},
	}
	if _, err := SelectDownloadFiles(extraOnly, FileSelectionOptions{DefaultSeason: 1}); !errors.Is(err, ErrNoMainVideo) {
		t.Fatalf("extra-only basename expected ErrNoMainVideo, got %v", err)
	}
}

// 混合季度包只选择主视频并排除 NCOP/NCED/ED/SP 等 extra
func TestSelectMixedSeasonPackExcludesExtraVariants(t *testing.T) {
	top := "SyntheticPack S01 SP Edition"
	files := []DownloadFile{
		{Index: 0, RelativePath: top + "/Synthetic - 01.mkv", SizeBytes: 2000},
		{Index: 1, RelativePath: top + "/Synthetic - 02.mkv", SizeBytes: 2000},
		{Index: 2, RelativePath: top + "/Synthetic - 03.mkv", SizeBytes: 2000},
		{Index: 3, RelativePath: top + "/Synthetic NCOP.mkv", SizeBytes: 3000},
		{Index: 4, RelativePath: top + "/Synthetic Nced.mkv", SizeBytes: 3000},
		{Index: 5, RelativePath: top + "/Synthetic ED01.mkv", SizeBytes: 3000},
		{Index: 6, RelativePath: top + "/Synthetic - 01 PV.mkv", SizeBytes: 3000},
		{Index: 7, RelativePath: top + "/NCOP/Synthetic - 01.mkv", SizeBytes: 3000},
		{Index: 8, RelativePath: top + "/SP/Synthetic - 02.mkv", SizeBytes: 3000},
		{Index: 9, RelativePath: top + "/Synthetic - 01.ass", SizeBytes: 90},
		{Index: 10, RelativePath: top + "/Synthetic - 02.ass", SizeBytes: 90},
		{Index: 11, RelativePath: top + "/readme.txt", SizeBytes: 10},
	}
	result, err := SelectDownloadFiles(files, FileSelectionOptions{DefaultSeason: 1})
	if err != nil {
		t.Fatalf("SelectDownloadFiles error = %v", err)
	}
	if len(result.Episodes) != 3 {
		t.Fatalf("episodes = %d, want 3", len(result.Episodes))
	}
	// 校验只有主视频被选中，extra variants 未选中
	for _, idx := range []int{3, 4, 5, 6, 7, 8} {
		if result.Files[idx].Selected {
			t.Fatalf("extra file index %d %q should not be selected", idx, result.Files[idx].RelativePath)
		}
		if result.Files[idx].Kind != MediaExtra {
			t.Fatalf("extra file index %d %q kind = %q, want extra", idx, result.Files[idx].RelativePath, result.Files[idx].Kind)
		}
	}
	if !result.Files[0].Selected || !result.Files[1].Selected || !result.Files[2].Selected {
		t.Fatalf("main videos not selected: %+v", result.Files[:3])
	}
}

// 顶层复合标签不应使非视频/字幕误分类，但扩展名白名单仍生效
func TestClassifyTopCompoundPreservesOtherAndSize(t *testing.T) {
	top := "Title S03 [SP+Bonus] 1080p"
	files := []DownloadFile{
		{Index: 0, RelativePath: top + "/cover.jpg", SizeBytes: 500},
		{Index: 1, RelativePath: top + "/preview.mp4", SizeBytes: 2000}, // still video? preview is not matched? basename contains? preview not in list? Actually trailer etc list includes trailer? but preview not extra, so mp4 should be video
		{Index: 2, RelativePath: top + "/Show - 01.mkv", SizeBytes: 2000},
	}
	// cover.jpg -> other (not video/subtitle), not extra despite top SP
	if got := classifyDownloadPath(files[0].RelativePath); got != MediaOther {
		t.Fatalf("cover.jpg kind = %q, want other", got)
	}
	if got := classifyDownloadPath(files[1].RelativePath); got != MediaVideo {
		t.Fatalf("preview.mp4 kind = %q, want video (basename not extra)", got)
	}
	if got := classifyDownloadPath(files[2].RelativePath); got != MediaVideo {
		t.Fatalf("main mkv kind = %q, want video", got)
	}
}

// 校验独立目录判定的边界：复合目录含非 extra 实质词不应为 extra
func TestIndependentDirectoryBoundary(t *testing.T) {
	cases := []struct {
		segment string
		want    bool
	}{
		{segment: "SP", want: true},
		{segment: " sp ", want: true},
		{segment: "[SP]", want: true},
		{segment: "NCOP", want: true},
		{segment: "NCOP 02", want: true},
		{segment: "Bonus", want: true},
		{segment: "Menu", want: true},
		{segment: "Scans", want: true},
		{segment: "SP 01", want: true},
		{segment: "ED01", want: true},
		{segment: "Season 1", want: false},
		{segment: "SyntheticTitle S01 SP Edition", want: false},
		{segment: "Title S03 SP Limited", want: false},
		{segment: "1080p", want: false},
		{segment: "Extras", want: false}, // not in list, conservative
	}
	for _, tc := range cases {
		if got := isIndependentExtraDirectory(tc.segment); got != tc.want {
			t.Fatalf("isIndependentExtraDirectory(%q) = %t, want %t", tc.segment, got, tc.want)
		}
	}
}

// 回归：单集与 RSS 路径仍可用
func TestClassifySingleEpisodeAndRssPathsNoRegression(t *testing.T) {
	single := []DownloadFile{
		{Index: 0, RelativePath: "release/video.mkv", SizeBytes: 3000},
		{Index: 1, RelativePath: "release/video.ass", SizeBytes: 100},
	}
	result, err := SelectDownloadFiles(single, FileSelectionOptions{DefaultSeason: 3, DefaultEpisode: 7, SingleEpisode: true})
	if err != nil {
		t.Fatalf("single episode select error = %v", err)
	}
	if len(result.Episodes) != 1 || result.Episodes[0].SourceSeason != 3 || result.Episodes[0].SourceEpisode != 7 {
		t.Fatalf("single episode result = %#v", result)
	}
	// RSS 同季同集路径
	rssFiles := []DownloadFile{
		{Index: 0, RelativePath: "Show/Show S01E01.mkv", SizeBytes: 2000},
		{Index: 1, RelativePath: "Show/Show S01E01.ass", SizeBytes: 80},
	}
	rssResult, err := SelectDownloadFiles(rssFiles, FileSelectionOptions{DefaultSeason: 1})
	if err != nil {
		t.Fatalf("rss select error = %v", err)
	}
	if len(rssResult.Episodes) != 1 {
		t.Fatalf("rss episodes = %d, want 1", len(rssResult.Episodes))
	}
}

// 第二层及更深的目录段继续使用 extraToken 严格匹配，即使复合标签也仍为 extra
func TestClassifyNestedCompoundExtraDirectoriesAreStillExtra(t *testing.T) {
	cases := []string{
		"Pack/NCOP 1080p/Show - 01.mkv",
		"Pack/Bonus Features/Show - 01.mkv",
		"Pack/SP Specials/Show - 01.mkv",
		"合成标题 S01 SP 1080p/NCOP/Show - 01.mkv",
		"Seed/Extra Scans/Show - 01.mkv",
	}
	for _, p := range cases {
		if got := classifyDownloadPath(p); got != MediaExtra {
			t.Fatalf("nested %q kind = %q, want extra", p, got)
		}
	}
	// 嵌套非 extra 子目录不应误判
	if got := classifyDownloadPath("Pack/Season 1/Show - 01.mkv"); got == MediaExtra {
		t.Fatalf("nested Season 1 should not be extra, got %q", got)
	}
	// 顶层复合标签不应传播，到第二层 extra 仍生效
	top := "SeriesTitle S01 SP Edition"
	if got := classifyDownloadPath(top + "/NCOP 1080p/Show - 01.mkv"); got != MediaExtra {
		t.Fatalf("top compound followed by nested NCOP kind = %q, want extra", got)
	}
}

// Unicode 标题 + SP 的顶层复合目录不应被视为独立 extra 目录而污染后代
func TestClassifyTopCompoundWithUnicodeTitleDoesNotPolluteChildren(t *testing.T) {
	tops := []string{
		"我的番剧 S01 SP 特典 1080p",
		"番組タイトル S02 SP 限定版",
		"Синтетика S01 Bonus Edition 1080p",
		"MyTitle S01 SP 特典 1080p",
	}
	for _, top := range tops {
		files := []DownloadFile{
			{Index: 0, RelativePath: top + "/Show - 01.mkv", SizeBytes: 1500},
			{Index: 1, RelativePath: top + "/Show - 02.mkv", SizeBytes: 1500},
			{Index: 2, RelativePath: top + "/Show - 01.ass", SizeBytes: 80},
		}
		classified, err := ClassifyDownloadFiles(files, FileSelectionOptions{DefaultSeason: 1})
		if err != nil {
			t.Fatalf("Classify top %q error = %v", top, err)
		}
		for i, f := range classified {
			if f.Kind == MediaExtra {
				t.Fatalf("top unicode compound %q file %d %q wrongly extra", top, i, f.RelativePath)
			}
		}
		result, err := SelectDownloadFiles(files, FileSelectionOptions{DefaultSeason: 1})
		if err != nil {
			t.Fatalf("Select top %q error = %v", top, err)
		}
		if len(result.Episodes) != 2 {
			t.Fatalf("top %q episodes = %d, want 2", top, len(result.Episodes))
		}
		if isIndependentExtraDirectory(top) {
			t.Fatalf("unicode compound top %q wrongly independent extra", top)
		}
	}
	// 独立 extra 根仍为 extra，即使含 Unicode 标题的嵌套 extra
	if !isIndependentExtraDirectory("SP") || !isIndependentExtraDirectory("[NCOP]") || !isIndependentExtraDirectory("SP 01") {
		t.Fatalf("independent extra roots misclassified")
	}
	if isIndependentExtraDirectory("我的番剧 S01 SP 特典") {
		t.Fatalf("unicode compound wrongly independent")
	}
}
