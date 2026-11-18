package domain

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

const charsetSampleSimplified = "1\n00:00:01,000 --> 00:00:03,000\n这是简体中文字幕测试，包含常用字的一是不了在人有我他这中大来上国个到说们为子和你地出道也时年得就那要下以生会自着去之过家学对可她里后小么心多天而能好都然没日于起还发成事只作当想看\n"
const charsetSampleTraditional = "1\n00:00:01,000 --> 00:00:03,000\n這是繁體中文字幕測試，包含常用字的的一是不了在人有我他這中大來上國個到說們為子和你地出道也時年得就那要下以生會自著去之過家學對可她裡後小麼心多天而能好都然沒日於起還發成事只作當想看\n"

func encodeSubtitle(t *testing.T, content string, transformer transform.Transformer) []byte {
	t.Helper()
	encoded, _, err := transform.Bytes(transformer, []byte(content))
	if err != nil {
		t.Fatalf("encode subtitle: %v", err)
	}
	return encoded
}

func encodeBOMLessUTF16(t *testing.T, content string, endianness unicode.Endianness) []byte {
	t.Helper()
	return encodeSubtitle(t, content, unicode.UTF16(endianness, unicode.IgnoreBOM).NewEncoder())
}

func TestDetectSubtitleCharsetRecognizesBOMLessUTF16(t *testing.T) {
	shortChinese := "1\n00:00:01,000 --> 00:00:03,000\n这是简体中文字幕测试\n"
	ascii := "1\n00:00:01,000 --> 00:00:03,000\nHello subtitle\n"
	tests := []struct {
		name    string
		content string
		charset SubtitleCharset
	}{
		{name: "short chinese le", content: shortChinese, charset: SubtitleCharsetUTF16LE},
		{name: "short chinese be", content: shortChinese, charset: SubtitleCharsetUTF16BE},
		{name: "long chinese le", content: charsetSampleSimplified, charset: SubtitleCharsetUTF16LE},
		{name: "long chinese be", content: charsetSampleSimplified, charset: SubtitleCharsetUTF16BE},
		{name: "ascii le", content: ascii, charset: SubtitleCharsetUTF16LE},
		{name: "ascii be", content: ascii, charset: SubtitleCharsetUTF16BE},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endianness := unicode.LittleEndian
			if test.charset == SubtitleCharsetUTF16BE {
				endianness = unicode.BigEndian
			}
			sample := encodeBOMLessUTF16(t, test.content, endianness)
			charset, err := DetectSubtitleCharset(sample, "episode.简体.srt")
			if err != nil {
				t.Fatalf("DetectSubtitleCharset() error = %v", err)
			}
			if charset != test.charset {
				t.Fatalf("DetectSubtitleCharset() = %q, want %q", charset, test.charset)
			}
			decoded, err := DecodeSubtitleText(sample, charset)
			if err != nil {
				t.Fatalf("DecodeSubtitleText() error = %v", err)
			}
			if !strings.Contains(string(decoded), strings.SplitN(test.content, "\n", 3)[2]) {
				t.Fatalf("decoded text = %q, want %q", string(decoded), test.content)
			}
		})
	}
}

func TestDetectSubtitleCharsetRejectsNullBytesOutsideUTF16(t *testing.T) {
	truncated := encodeBOMLessUTF16(t, "1\n00:00:01,000 --> 00:00:03,000\n测试\n", unicode.LittleEndian)
	tests := map[string][]byte{
		"utf-8 with null":     append([]byte("valid utf-8 text\n"), 0x00),
		"utf-8 bom with null": append(append([]byte{0xEF, 0xBB, 0xBF}, []byte("valid utf-8 text\n")...), 0x00),
		"odd utf-16":          append(truncated, 0x41),
		"binary nul runs":     bytes.Repeat([]byte{0x00, 0x01}, 64),
	}
	for name, sample := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := DetectSubtitleCharset(sample, "")
			var subtitleErr *SubtitleError
			if !errors.As(err, &subtitleErr) || subtitleErr.Code != "subtitle_charset_unsupported" {
				t.Fatalf("DetectSubtitleCharset() error = %v, want subtitle_charset_unsupported", err)
			}
		})
	}
}

func TestDetectSubtitleCharsetRecognizesSupportedEncodings(t *testing.T) {
	tests := []struct {
		name    string
		sample  []byte
		label   string
		charset SubtitleCharset
	}{
		{name: "utf-8", sample: []byte(charsetSampleSimplified), charset: SubtitleCharsetUTF8},
		{name: "utf-8 bom", sample: append([]byte{0xEF, 0xBB, 0xBF}, []byte(charsetSampleSimplified)...), charset: SubtitleCharsetUTF8},
		{
			name:    "gb18030",
			sample:  encodeSubtitle(t, charsetSampleSimplified, simplifiedchinese.GB18030.NewEncoder()),
			charset: SubtitleCharsetGB18030,
		},
		{
			name:    "big5",
			sample:  encodeSubtitle(t, charsetSampleTraditional, traditionalchinese.Big5.NewEncoder()),
			label:   "Show.繁体.BIG5.srt",
			charset: SubtitleCharsetBig5,
		},
		{
			name:    "utf-16le bom",
			sample:  encodeSubtitle(t, charsetSampleSimplified, unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder()),
			charset: SubtitleCharsetUTF16LE,
		},
		{
			name:    "utf-16be bom",
			sample:  encodeSubtitle(t, charsetSampleSimplified, unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewEncoder()),
			charset: SubtitleCharsetUTF16BE,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			charset, err := DetectSubtitleCharset(test.sample, test.label)
			if err != nil {
				t.Fatalf("DetectSubtitleCharset() error = %v", err)
			}
			if charset != test.charset {
				t.Fatalf("DetectSubtitleCharset() = %q, want %q", charset, test.charset)
			}
		})
	}
}

func TestDetectSubtitleCharsetRejectsUndecodableOrAmbiguousContent(t *testing.T) {
	for name, sample := range map[string][]byte{
		"empty":         nil,
		"invalid bytes": {0xFF, 0xFF, 0xFF, 0xFF},
		"control noise": {0x01, 0x02, 0x03, 0xFE},
		"invalid pairs": {0x81, 0x20, 0x82, 0x20},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DetectSubtitleCharset(sample, "")
			var subtitleErr *SubtitleError
			if !errors.As(err, &subtitleErr) || subtitleErr.Code != "subtitle_charset_unsupported" {
				t.Fatalf("DetectSubtitleCharset() error = %v, want subtitle_charset_unsupported", err)
			}
		})
	}
}

func TestDecodeSubtitleTextConvertsSupportedEncodings(t *testing.T) {
	gb := encodeSubtitle(t, charsetSampleSimplified, simplifiedchinese.GB18030.NewEncoder())
	big5 := encodeSubtitle(t, charsetSampleTraditional, traditionalchinese.Big5.NewEncoder())
	utf16le := encodeSubtitle(t, charsetSampleSimplified, unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder())
	utf16be := encodeSubtitle(t, charsetSampleSimplified, unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewEncoder())
	tests := []struct {
		name    string
		content []byte
		charset SubtitleCharset
		want    string
	}{
		{name: "utf-8", content: []byte(charsetSampleSimplified), charset: SubtitleCharsetUTF8, want: "这是简体中文字幕测试"},
		{name: "gb18030", content: gb, charset: SubtitleCharsetGB18030, want: "这是简体中文字幕测试"},
		{name: "big5", content: big5, charset: SubtitleCharsetBig5, want: "這是繁體中文字幕測試"},
		{name: "utf-16le", content: utf16le, charset: SubtitleCharsetUTF16LE, want: "这是简体中文字幕测试"},
		{name: "utf-16be", content: utf16be, charset: SubtitleCharsetUTF16BE, want: "这是简体中文字幕测试"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoded, err := DecodeSubtitleText(test.content, test.charset)
			if err != nil {
				t.Fatalf("DecodeSubtitleText() error = %v", err)
			}
			if !strings.Contains(string(decoded), test.want) {
				t.Fatalf("decoded text = %q, want %q", string(decoded), test.want)
			}
			if strings.ContainsRune(string(decoded), '\uFFFD') {
				t.Fatalf("decoded text contains replacement characters: %q", string(decoded))
			}
		})
	}
}

func TestDecodeSubtitleTextRejectsCorruptedBytesAndUnknownCharset(t *testing.T) {
	if _, err := DecodeSubtitleText([]byte{0xFF, 0xFF, 0xFF}, SubtitleCharsetGB18030); err == nil {
		t.Fatal("DecodeSubtitleText() accepted corrupted GB18030 bytes")
	}
	if _, err := DecodeSubtitleText([]byte("text"), SubtitleCharset("KOI8-R")); err == nil {
		t.Fatal("DecodeSubtitleText() accepted an unknown charset")
	}
}

const charsetSampleBoundaryBytes = 256 << 10

// buildUTF8BoundaryContent 构造在 256 KiB 检测边界处跨越多字节字符的完整 UTF-8 内容。
// leading 是 BOM 等前缀；inside 是目标字符落在检测样本内的字节数。
func buildUTF8BoundaryContent(t *testing.T, leading []byte, filler string, character rune, inside int) []byte {
	t.Helper()
	encoded := []byte(string(character))
	if inside < 0 || inside > len(encoded) {
		t.Fatalf("inside=%d is outside encoded rune length %d", inside, len(encoded))
	}
	prefix := append(append([]byte(nil), leading...), []byte(filler)...)
	start := charsetSampleBoundaryBytes - inside
	if start < len(prefix) {
		t.Fatalf("fixture prefix=%d exceeds rune start=%d", len(prefix), start)
	}
	content := make([]byte, 0, charsetSampleBoundaryBytes+len(encoded)+64)
	content = append(content, prefix...)
	content = append(content, bytes.Repeat([]byte("a"), start-len(content))...)
	content = append(content, encoded...)
	content = append(content, []byte("后续字幕文本 tail\n")...)
	return content
}

func TestDetectSubtitleCharsetUsesFullContentAtSampleBoundary(t *testing.T) {
	tests := []struct {
		name      string
		leading   []byte
		filler    string
		character rune
		inside    int
	}{
		{name: "two byte rune split after first byte", character: 'é', inside: 1},
		{name: "three byte rune split after first byte", character: '中', inside: 1},
		{name: "three byte rune split after second byte", character: '中', inside: 2},
		{name: "four byte rune split after first byte", character: '😀', inside: 1},
		{name: "four byte rune split after second byte", character: '😀', inside: 2},
		{name: "four byte rune split after third byte", character: '😀', inside: 3},
		{name: "mixed chinese and english filler", filler: "这是中英混合字幕文本mixed", character: '中', inside: 2},
		{name: "utf-8 bom and four byte rune split", leading: []byte{0xEF, 0xBB, 0xBF}, filler: "中文字幕", character: '😀', inside: 1},
		{name: "utf-8 bom and three byte rune split", leading: []byte{0xEF, 0xBB, 0xBF}, filler: "mixed", character: '中', inside: 2},
		{name: "three byte rune ends exactly at boundary", character: '中', inside: 3},
		{name: "four byte rune starts exactly at boundary", character: '😀', inside: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := buildUTF8BoundaryContent(t, test.leading, test.filler, test.character, test.inside)
			encoded := []byte(string(test.character))
			split := test.inside > 0 && test.inside < len(encoded)
			if !utf8.Valid(content) {
				t.Fatalf("fixture is not valid UTF-8: %q", content)
			}
			sample := content[:charsetSampleBoundaryBytes]
			if split && utf8.Valid(sample) {
				t.Fatalf("fixture does not split a rune at the sample boundary")
			}
			if !split && !utf8.Valid(sample) {
				t.Fatalf("fixture unexpectedly splits a rune at the sample boundary")
			}
			charset, err := DetectSubtitleCharset(content, "episode.简体.BIG5.srt")
			if err != nil {
				t.Fatalf("DetectSubtitleCharset(valid full UTF-8 content) error = %v", err)
			}
			if charset != SubtitleCharsetUTF8 {
				t.Fatalf("DetectSubtitleCharset() = %q, want %q", charset, SubtitleCharsetUTF8)
			}
			decoded, err := DecodeSubtitleText(content, charset)
			if err != nil {
				t.Fatalf("DecodeSubtitleText() error = %v", err)
			}
			want := content
			if len(test.leading) > 0 {
				want = content[len(test.leading):]
			}
			if !bytes.Equal(decoded, want) {
				t.Fatalf("UTF-8 content bytes were modified: got %d bytes, want %d", len(decoded), len(want))
			}
			if !strings.Contains(string(decoded), string(test.character)) {
				t.Fatalf("decoded content lost rune %q", test.character)
			}
		})
	}
}

func TestDetectSubtitleCharsetHandlesSmallExactAndPostBoundaryUTF8(t *testing.T) {
	header := []byte("[Script Info]\nTitle: ")
	exact := append(append([]byte(nil), header...), bytes.Repeat([]byte("a"), charsetSampleBoundaryBytes-len(header))...)
	postBoundary := append(bytes.Repeat([]byte("a"), charsetSampleBoundaryBytes+7), []byte("中文😀")...)
	tests := map[string][]byte{
		"small":          []byte(charsetSampleSimplified),
		"exact boundary": exact,
		"after boundary": postBoundary,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if !utf8.Valid(content) {
				t.Fatal("fixture is not valid UTF-8")
			}
			charset, err := DetectSubtitleCharset(content, "")
			if err != nil || charset != SubtitleCharsetUTF8 {
				t.Fatalf("DetectSubtitleCharset() = %q, %v; want UTF-8", charset, err)
			}
		})
	}
}

func TestDetectSubtitleCharsetRejectsIncompleteUTF8AndNullContent(t *testing.T) {
	asciiPrefix := []byte("1\n00:00:01,000 --> 00:00:03,000\nIncomplete subtitle text\n")
	largeASCII := bytes.Repeat([]byte("a"), charsetSampleBoundaryBytes+1024)
	tests := map[string][]byte{
		"incomplete rune at small eof":          append(append([]byte(nil), asciiPrefix...), 0xE4, 0xB8),
		"incomplete rune at exact boundary eof": append(bytes.Repeat([]byte("a"), charsetSampleBoundaryBytes-2), 0xE4, 0xB8),
		"incomplete rune at large eof":          append(append([]byte(nil), largeASCII...), 0xE4, 0xB8),
		"null after sample boundary":            append(append([]byte(nil), largeASCII...), 0x00),
		"utf-8 bom null after sample boundary":  append(append([]byte{0xEF, 0xBB, 0xBF}, largeASCII...), 0x00),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			charset, err := DetectSubtitleCharset(content, "")
			var subtitleErr *SubtitleError
			if !errors.As(err, &subtitleErr) || subtitleErr.Code != "subtitle_charset_unsupported" {
				t.Fatalf("DetectSubtitleCharset() = %q, %v; want subtitle_charset_unsupported", charset, err)
			}
		})
	}
}

func utf16BoundarySRTText(t *testing.T, unitsBefore int) string {
	t.Helper()
	blocks := make([]string, 0, unitsBefore/64)
	length := 0
	index := 1
	blockText := "这是简体中文字幕测试文本 Bilingual subtitle text。\n\n"
	for {
		block := fmt.Sprintf("%d\n00:00:01,000 --> 00:00:03,000\n%s", index, blockText)
		blockLength := len(utf16.Encode([]rune(block)))
		if length+blockLength > unitsBefore {
			break
		}
		blocks = append(blocks, block)
		length += blockLength
		index++
	}
	for len(blocks) > 0 {
		prefix := fmt.Sprintf("%d\n00:00:01,000 --> 00:00:03,000\n", index)
		if length+len(utf16.Encode([]rune(prefix))) <= unitsBefore {
			break
		}
		last := blocks[len(blocks)-1]
		length -= len(utf16.Encode([]rune(last)))
		blocks = blocks[:len(blocks)-1]
		index--
	}
	prefix := fmt.Sprintf("%d\n00:00:01,000 --> 00:00:03,000\n", index)
	remaining := unitsBefore - length - len(utf16.Encode([]rune(prefix)))
	if remaining < 0 {
		t.Fatalf("UTF-16 fixture cannot place the final cue within %d units", unitsBefore)
	}
	var builder strings.Builder
	for _, block := range blocks {
		builder.WriteString(block)
	}
	builder.WriteString(prefix)
	builder.WriteString(strings.Repeat("a", remaining))
	builder.WriteString("😀后续字幕文本\n\n")
	return builder.String()
}

func utf16HighSurrogateBytes(character rune, endianness unicode.Endianness) []byte {
	high := utf16.Encode([]rune{character})[0]
	encoded := []byte{byte(high >> 8), byte(high)}
	if endianness == unicode.LittleEndian {
		encoded[0], encoded[1] = encoded[1], encoded[0]
	}
	return encoded
}

func TestDetectSubtitleCharsetHandlesUTF16SurrogateAtSampleBoundary(t *testing.T) {
	tests := []struct {
		name       string
		endianness unicode.Endianness
		charset    SubtitleCharset
		bomPolicy  unicode.BOMPolicy
	}{
		{name: "bomless le", endianness: unicode.LittleEndian, charset: SubtitleCharsetUTF16LE, bomPolicy: unicode.IgnoreBOM},
		{name: "bomless be", endianness: unicode.BigEndian, charset: SubtitleCharsetUTF16BE, bomPolicy: unicode.IgnoreBOM},
		{name: "bom le", endianness: unicode.LittleEndian, charset: SubtitleCharsetUTF16LE, bomPolicy: unicode.UseBOM},
		{name: "bom be", endianness: unicode.BigEndian, charset: SubtitleCharsetUTF16BE, bomPolicy: unicode.UseBOM},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unitsBefore := charsetSampleBoundaryBytes/2 - 1
			if test.bomPolicy == unicode.UseBOM {
				unitsBefore--
			}
			text := utf16BoundarySRTText(t, unitsBefore)
			content := encodeSubtitle(t, text, unicode.UTF16(test.endianness, test.bomPolicy).NewEncoder())
			if len(content) <= charsetSampleBoundaryBytes {
				t.Fatalf("fixture size = %d, want > %d", len(content), charsetSampleBoundaryBytes)
			}
			sample := content[:charsetSampleBoundaryBytes]
			high := utf16HighSurrogateBytes('😀', test.endianness)
			if !bytes.Equal(sample[len(sample)-len(high):], high) {
				t.Fatalf("sample does not end with the high surrogate: % x, want % x", sample[len(sample)-len(high):], high)
			}
			charset, err := DetectSubtitleCharset(content, "episode.简体.srt")
			if err != nil {
				t.Fatalf("DetectSubtitleCharset() error = %v", err)
			}
			if charset != test.charset {
				t.Fatalf("DetectSubtitleCharset() = %q, want %q", charset, test.charset)
			}
			decoded, err := DecodeSubtitleText(content, charset)
			if err != nil {
				t.Fatalf("DecodeSubtitleText() error = %v", err)
			}
			if !strings.Contains(string(decoded), "😀后续字幕文本") {
				t.Fatalf("decoded text lost the non-BMP character: %q", string(decoded[len(decoded)-64:]))
			}
		})
	}
}

func TestDetectSubtitleCharsetRejectsGenuineDanglingUTF16Surrogate(t *testing.T) {
	text := "1\n00:00:01,000 --> 00:00:03,000\n真实文件末尾悬挂代理\n"
	for _, test := range []struct {
		name       string
		endianness unicode.Endianness
	}{
		{name: "little endian", endianness: unicode.LittleEndian},
		{name: "big endian", endianness: unicode.BigEndian},
	} {
		t.Run("bomless "+test.name, func(t *testing.T) {
			content := encodeBOMLessUTF16(t, text, test.endianness)
			content = append(content, utf16HighSurrogateBytes('😀', test.endianness)...)
			charset, err := DetectSubtitleCharset(content, "")
			var subtitleErr *SubtitleError
			if !errors.As(err, &subtitleErr) || subtitleErr.Code != "subtitle_charset_unsupported" {
				t.Fatalf("DetectSubtitleCharset() = %q, %v; want subtitle_charset_unsupported", charset, err)
			}
		})
		t.Run("bom "+test.name, func(t *testing.T) {
			content := encodeSubtitle(t, text, unicode.UTF16(test.endianness, unicode.UseBOM).NewEncoder())
			content = append(content, utf16HighSurrogateBytes('😀', test.endianness)...)
			charset, err := DetectSubtitleCharset(content, "")
			if err != nil {
				t.Fatalf("DetectSubtitleCharset() error = %v", err)
			}
			if _, err := DecodeSubtitleText(content, charset); err == nil {
				t.Fatal("DecodeSubtitleText() accepted a genuine dangling surrogate at EOF")
			}
		})
	}
}
