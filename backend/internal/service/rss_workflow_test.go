package service

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateShortTextStaysUntouched(t *testing.T) {
	input := "短的错误消息"
	if got := truncate(input, 2000); got != input {
		t.Fatalf("truncate() = %q, want original %q", got, input)
	}
}

func TestTruncateAtExactRuneLimitStaysUntouched(t *testing.T) {
	input := strings.Repeat("a", 2000)
	if got := truncate(input, 2000); got != input {
		t.Fatalf("truncate() length = %d, want %d", len(got), len(input))
	}
}

func TestTruncateLongASCIIKeepsLimitRunes(t *testing.T) {
	input := strings.Repeat("a", 2001)
	if got := truncate(input, 2000); got != strings.Repeat("a", 2000) {
		t.Fatalf("truncate() = %q, want 2000 a characters", got)
	}
}

func TestTruncateMultibyteTextOverBytesButUnderRunesStaysUntouched(t *testing.T) {
	// 字节数超过 2000（旧实现按字节截断会切断多字节字符），但 rune 数未超限。
	input := "abc" + strings.Repeat("番", 700)
	if len(input) <= 2000 {
		t.Fatalf("test input must exceed 2000 bytes, got %d", len(input))
	}
	if got := truncate(input, 2000); got != input {
		t.Fatalf("truncate() = %q, want original %q", got, input)
	}
}

func TestTruncateMultibyteTextOverRuneLimitKeepsValidUTF8Prefix(t *testing.T) {
	prefix := "前缀" + strings.Repeat("番", 1998) // 2 + 1998 = 2000 runes
	input := prefix + "后缀"
	got := truncate(input, 2000)
	if got != prefix {
		t.Fatalf("truncate() = %q, want prefix %q", got, prefix)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncate() output is not valid UTF-8: %q", got)
	}
}

func TestTruncateReplacesInvalidBytesWithReplacementCharacter(t *testing.T) {
	// 输入本身含非法 UTF-8 字节，即使长度未超限也必须输出合法 UTF-8。
	input := "abc" + string([]byte{0xff, 0xfe}) + "def"
	if utf8.ValidString(input) {
		t.Fatal("test input must contain invalid UTF-8 bytes")
	}
	want := "abc\ufffd\ufffddef"
	if got := truncate(input, 2000); got != want {
		t.Fatalf("truncate() = %q, want %q", got, want)
	}
}

func TestTruncateOutputAlwaysValidUTF8WithinRuneLimit(t *testing.T) {
	cases := []string{
		strings.Repeat("中文剧名", 500), // 2000 runes, 6000 bytes
		"https://example.test/feed/" + strings.Repeat("中文", 1000) + "!",
		strings.Repeat("番", 10000),
		string([]byte{0xc0, 0xaf}) + strings.Repeat("a", 3000),
	}
	for _, input := range cases {
		got := truncate(input, 2000)
		if !utf8.ValidString(got) {
			t.Fatalf("truncate(%q) output is not valid UTF-8: %q", input, got)
		}
		if runes := len([]rune(got)); runes > 2000 {
			t.Fatalf("truncate(%q) output has %d runes, want at most 2000", input, runes)
		}
	}
}
