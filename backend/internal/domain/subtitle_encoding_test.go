package domain

import (
	"bytes"
	"errors"
	"strings"
	"testing"

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
