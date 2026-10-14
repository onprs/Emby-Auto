package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestNormalizeRSSSubtitleGroupInput(t *testing.T) {
	tests := []struct {
		name, input, wantName, wantKey string
		invalid                        bool
	}{
		{"空白", " \t ", "", "", true},
		{"仅括号", "【 】", "", "", true},
		{"完整规范化", " 【 ＲｅｌｅａｓｅＴｅａｍ 】 ", "ReleaseTeam", "releaseteam", false},
		{"折叠空白", "Release\t Team", "Release Team", "release team", false},
		{"保留连字符", "Release-Team", "Release-Team", "release-team", false},
		{"长度128", strings.Repeat("字", 128), strings.Repeat("字", 128), strings.Repeat("字", 128), false},
		{"长度129", strings.Repeat("字", 129), "", "", true},
		{"空字符", "Release\x00Team", "", "", true},
		{"非空白控制字符", "Release\u0081Team", "", "", true},
		{"仅零宽字符", "\u200b", "", "", true},
		{"仅组合字符", "\u0301", "", "", true},
		{"含连接字符的可见名称", "翻译\u200d组", "翻译\u200d组", "翻译\u200d组", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name, key, err := normalizeRSSSubtitleGroupInput(test.input)
			if test.invalid {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("name=%q key=%q err=%v，期望输入校验错误", name, key, err)
				}
				return
			}
			if err != nil || name != test.wantName || key != test.wantKey {
				t.Fatalf("name=%q key=%q err=%v，期望 %q/%q", name, key, err, test.wantName, test.wantKey)
			}
		})
	}
}

func TestMaintainedRSSSubtitleGroupNamesMatchExactLabels(t *testing.T) {
	for _, raw := range []string{"A", "AACStudio", "123", "!!!", "Release Team", "Release-Team"} {
		t.Run(raw, func(t *testing.T) {
			name, _, err := normalizeRSSSubtitleGroupInput(raw)
			if err != nil {
				t.Fatal(err)
			}
			got := domain.ExtractRSSSubtitleGroupCandidates(domain.RSSFeed{Entries: []domain.RSSFeedEntry{{Title: "[" + name + "] 动画 - 01"}}}, []string{name})
			if len(got) != 1 || got[0] != name {
				t.Fatalf("维护接口接受 %q，识别结果 = %#v", name, got)
			}
		})
	}
}
