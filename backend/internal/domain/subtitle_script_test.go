package domain

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestAnalyzeASSChineseScriptRequiresStrongUnambiguousEvidence(t *testing.T) {
	simplified := strings.Repeat("我们已经说过这个问题，后来还是决定继续学习简体中文。", 5)
	traditional := strings.Repeat("我們已經說過這個問題，後來還是決定繼續學習繁體中文。", 5)
	tests := []struct {
		name string
		text string
		want ChineseScript
	}{
		{
			name: "simplified dialogue",
			text: `{\fn繁體傳統樣式不應計分}` + simplified,
			want: ChineseScriptSimplified,
		},
		{name: "traditional dialogue", text: traditional, want: ChineseScriptTraditional},
		{name: "balanced mixed dialogue", text: simplified + traditional, want: ChineseScriptUnknown},
		{name: "short simplified sample", text: "我们说话。", want: ChineseScriptUnknown},
		{name: "script-neutral Han characters", text: strings.Repeat("天地人生山水日月大小上下。", 8), want: ChineseScriptUnknown},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis, err := AnalyzeASSChineseScript(assScriptFixture(test.text))
			if err != nil {
				t.Fatalf("AnalyzeASSChineseScript() error = %v", err)
			}
			if analysis.Script != test.want {
				t.Fatalf("analysis = %#v, want script %q", analysis, test.want)
			}
			if test.want == ChineseScriptSimplified && analysis.SimplifiedEvidence < minimumChineseScriptEvidence {
				t.Fatalf("simplified evidence = %d, want at least %d", analysis.SimplifiedEvidence, minimumChineseScriptEvidence)
			}
			if test.want == ChineseScriptTraditional && analysis.TraditionalEvidence < minimumChineseScriptEvidence {
				t.Fatalf("traditional evidence = %d, want at least %d", analysis.TraditionalEvidence, minimumChineseScriptEvidence)
			}
		})
	}
}

func TestAnalyzeASSChineseScriptIsSafeForConcurrentWorkers(t *testing.T) {
	content := assScriptFixture(strings.Repeat("我们已经说过这个问题，后来还是决定继续学习简体中文。", 5))
	const workers = 32
	errors := make(chan error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			analysis, err := AnalyzeASSChineseScript(content)
			if err != nil {
				errors <- err
				return
			}
			if analysis.Script != ChineseScriptSimplified {
				errors <- fmt.Errorf("script = %q", analysis.Script)
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Errorf("concurrent analysis: %v", err)
	}
}

func TestAnalyzeASSChineseScriptUsesDeclaredDialogueTextColumn(t *testing.T) {
	content := []byte("[Script Info]\nTitle: 测试标题不应计分\n\n[Events]\nFormat: Text, Start, End, Style\nDialogue: " + strings.Repeat("我們已經說過這個問題。", 5) + ",0:00:01.00,0:00:02.00,Default\n")
	analysis, err := AnalyzeASSChineseScript(content)
	if err != nil {
		t.Fatalf("AnalyzeASSChineseScript() error = %v", err)
	}
	if analysis.Script != ChineseScriptTraditional {
		t.Fatalf("analysis = %#v, want traditional", analysis)
	}
}

func TestAnalyzeASSChineseScriptRejectsInvalidASS(t *testing.T) {
	if _, err := AnalyzeASSChineseScript([]byte("plain subtitle text")); err == nil {
		t.Fatal("AnalyzeASSChineseScript() error = nil")
	}
}

func TestNormalizeASSToSimplifiedAddsMissingScriptInfo(t *testing.T) {
	content := []byte("[V4+ Styles]\nFormat: Name, Fontname, Fontsize\nStyle: Default,Arial,20\n\n[Events]\nFormat: Layer, Start, End, Style, Text\nDialogue: 0,0:00:01.00,0:00:02.00,Default,我們繼續學習。\n")
	if err := ValidateASS(content); err == nil {
		t.Fatal("ValidateASS() accepted candidate without Script Info")
	}
	if err := ValidateASSCandidate(content); err != nil {
		t.Fatalf("ValidateASSCandidate() error = %v", err)
	}
	normalized, err := NormalizeASSToSimplified(content)
	if err != nil {
		t.Fatalf("NormalizeASSToSimplified() error = %v", err)
	}
	if err := ValidateASS(normalized); err != nil {
		t.Fatalf("normalized ASS is invalid: %v", err)
	}
	text := string(normalized)
	if !strings.HasPrefix(text, "[Script Info]\nScriptType: v4.00+\n") || !strings.Contains(text, "我们继续学习。") {
		t.Fatalf("normalized ASS = %q", text)
	}
}

func TestNormalizeASSToSimplifiedPreservesOverrideTags(t *testing.T) {
	content := assScriptFixture(`{\fn繁體字型}我們已經決定繼續學習繁體中文。`)
	normalized, err := NormalizeASSToSimplified(content)
	if err != nil {
		t.Fatalf("NormalizeASSToSimplified() error = %v", err)
	}
	text := string(normalized)
	if !strings.Contains(text, `{\fn繁體字型}我们已经决定继续学习繁体中文。`) {
		t.Fatalf("normalized subtitle = %q", text)
	}
	if strings.Contains(text, "我們已經決定") {
		t.Fatalf("traditional dialogue remains: %q", text)
	}
}

func TestASSProcessingSupportsDialogueLinesLongerThanScannerDefault(t *testing.T) {
	content := assScriptFixture(strings.Repeat("我們已經決定繼續學習繁體中文。", 5_000))
	if err := ValidateASS(content); err != nil {
		t.Fatalf("ValidateASS() error = %v", err)
	}
	analysis, err := AnalyzeASSChineseScript(content)
	if err != nil {
		t.Fatalf("AnalyzeASSChineseScript() error = %v", err)
	}
	if analysis.Script != ChineseScriptTraditional || analysis.HanCharacters < 64*1024 {
		t.Fatalf("analysis = %#v", analysis)
	}
}

func TestSubtitleContentEvidenceRejectsUnknownJapaneseTrack(t *testing.T) {
	analysis, err := AnalyzeASSChineseScript(assScriptFixture(strings.Repeat("今日は新しい物語を見ながら勉強します。", 10)))
	if err != nil {
		t.Fatal(err)
	}
	if SubtitleContentIsLikelyChinese(analysis, SubtitleEvidenceUnknown) {
		t.Fatalf("Japanese content accepted as unknown Chinese: %#v", analysis)
	}
}

func assScriptFixture(text string) []byte {
	return []byte(fmt.Sprintf("[Script Info]\nTitle: Fixture\n\n[Events]\nFormat: Layer, Start, End, Style, Text\nDialogue: 0,0:00:01.00,0:00:02.00,Default,%s\n", text))
}
