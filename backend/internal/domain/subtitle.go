package domain

import (
	"bufio"
	"bytes"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type SubtitleFormat string

const maxASSScanTokenBytes = 64 << 20

const (
	SubtitleASS     SubtitleFormat = "ass"
	SubtitleSSA     SubtitleFormat = "ssa"
	SubtitleSRT     SubtitleFormat = "srt"
	SubtitleWebVTT  SubtitleFormat = "webvtt"
	SubtitleMovText SubtitleFormat = "mov_text"
	SubtitlePGS     SubtitleFormat = "pgs"
	SubtitleVobSub  SubtitleFormat = "vobsub"
)

type SubtitleSource string

const (
	SubtitleSourceExternal SubtitleSource = "external"
	SubtitleSourceEmbedded SubtitleSource = "embedded"
)

type SubtitleAction string

const (
	SubtitleActionCopy    SubtitleAction = "copy"
	SubtitleActionConvert SubtitleAction = "convert"
	SubtitleActionExtract SubtitleAction = "extract"
)

type SubtitleEvidence string

const (
	SubtitleEvidenceSimplified  SubtitleEvidence = "simplified"
	SubtitleEvidenceMixed       SubtitleEvidence = "mixed_chinese"
	SubtitleEvidenceTraditional SubtitleEvidence = "traditional"
	SubtitleEvidenceGeneric     SubtitleEvidence = "generic_chinese"
	SubtitleEvidenceUnknown     SubtitleEvidence = "unknown"
	SubtitleEvidenceOther       SubtitleEvidence = "other"
)

type SubtitleCandidate struct {
	Path     string
	Format   SubtitleFormat
	Language string
}

type SubtitleStream struct {
	Index    int
	Format   SubtitleFormat
	Language string
	Title    string
	Default  bool
	Forced   bool
	Script   ChineseScript
}

type SubtitleSelectionRequest struct {
	VideoPath string
	External  []SubtitleCandidate
	Embedded  []SubtitleStream
}

type SubtitlePlan struct {
	Source      SubtitleSource
	Action      SubtitleAction
	InputPath   string
	StreamIndex int
	InputFormat SubtitleFormat
	Language    string
	Evidence    SubtitleEvidence
}

type SubtitleError struct {
	Code    string
	Message string
}

func (err *SubtitleError) Error() string {
	return err.Message
}

type subtitleChoice struct {
	plan  SubtitlePlan
	score int
	order int
}

type SubtitleInspectionRequest struct {
	VideoPath   string
	CandidateID string
	Source      SubtitleSource
	StreamIndex int
	Format      SubtitleFormat
	Path        string
}

type SubtitleInspection struct {
	CandidateID string
	Found       bool
	Script      ChineseScript
	Sample      string
	HanCount    int
}

type SubtitleMatchCandidate struct {
	CandidateID string
	Source      SubtitleSource
	StreamIndex int
	Format      SubtitleFormat
	Language    string
	Title       string
	Path        string
}

// CandidateID returns the stable identifier used to reference a subtitle
// candidate across scope persistence, the Agent context, and the inspection
// tool. Embedded streams are keyed by their stream index; external files by
// their basename.
func CandidateID(plan SubtitlePlan) string {
	if plan.Source == SubtitleSourceEmbedded {
		return "stream:" + strconv.Itoa(plan.StreamIndex)
	}
	return "file:" + path.Base(strings.ReplaceAll(plan.InputPath, `\\`, "/"))
}

// RankSubtitleCandidates returns every potentially usable text subtitle in a
// deterministic order. The worker validates content and falls back between
// plans, so metadata ranking never becomes a single point of failure.
func RankSubtitleCandidates(request SubtitleSelectionRequest) ([]SubtitlePlan, error) {
	choices := make([]subtitleChoice, 0, len(request.External)+len(request.Embedded))
	unsupportedChinese := false
	order := 0
	for _, candidate := range request.External {
		label := path.Base(strings.ReplaceAll(candidate.Path, `\`, "/"))
		evidence := subtitleMetadataEvidence(candidate.Language, label)
		if evidence == SubtitleEvidenceOther {
			order++
			continue
		}
		if !isTextSubtitle(candidate.Format) {
			unsupportedChinese = unsupportedChinese || evidence != SubtitleEvidenceUnknown
			order++
			continue
		}
		choices = append(choices, subtitleChoice{
			plan: SubtitlePlan{
				Source: SubtitleSourceExternal, Action: SubtitleActionConvert,
				InputPath: candidate.Path, StreamIndex: -1, InputFormat: candidate.Format,
				Language: "zh-Hans", Evidence: evidence,
			},
			score: subtitleEvidenceScore(evidence) + 50 + subtitleFormatScore(candidate.Format),
			order: order,
		})
		order++
	}
	for _, stream := range request.Embedded {
		evidence := subtitleMetadataEvidence(stream.Language, stream.Title)
		if evidence == SubtitleEvidenceOther {
			order++
			continue
		}
		if !isTextSubtitle(stream.Format) {
			unsupportedChinese = unsupportedChinese || evidence != SubtitleEvidenceUnknown
			order++
			continue
		}
		score := subtitleEvidenceScore(evidence) + subtitleFormatScore(stream.Format)
		if stream.Default {
			score += 5
		}
		if stream.Forced {
			score -= 10
		}
		choices = append(choices, subtitleChoice{
			plan: SubtitlePlan{
				Source: SubtitleSourceEmbedded, Action: SubtitleActionExtract,
				InputPath: request.VideoPath, StreamIndex: stream.Index, InputFormat: stream.Format,
				Language: "zh-Hans", Evidence: evidence,
			},
			score: score, order: order,
		})
		order++
	}

	if len(choices) == 0 {
		if unsupportedChinese {
			return nil, &SubtitleError{Code: "subtitle_format_unsupported", Message: "the available Chinese subtitle uses a bitmap or unsupported format"}
		}
		return nil, &SubtitleError{Code: "simplified_chinese_subtitle_not_found", Message: "no potentially usable Chinese text subtitle source was found"}
	}
	sort.SliceStable(choices, func(left, right int) bool {
		if choices[left].score != choices[right].score {
			return choices[left].score > choices[right].score
		}
		return choices[left].order < choices[right].order
	})
	plans := make([]SubtitlePlan, 0, len(choices))
	for _, choice := range choices {
		plans = append(plans, choice.plan)
	}
	return plans, nil
}

func SelectSubtitle(request SubtitleSelectionRequest) (SubtitlePlan, error) {
	plans, err := RankSubtitleCandidates(request)
	if err != nil {
		return SubtitlePlan{}, err
	}
	return plans[0], nil
}

func subtitleEvidenceScore(evidence SubtitleEvidence) int {
	switch evidence {
	case SubtitleEvidenceSimplified:
		return 400
	case SubtitleEvidenceMixed:
		return 300
	case SubtitleEvidenceTraditional:
		return 250
	case SubtitleEvidenceGeneric:
		return 200
	case SubtitleEvidenceUnknown:
		return 100
	default:
		return 0
	}
}

func BuildSubtitleFFmpegArgs(plan SubtitlePlan, videoPath, temporaryOutput string) ([]string, error) {
	if strings.TrimSpace(temporaryOutput) == "" {
		return nil, &SubtitleError{Code: "subtitle_output_invalid", Message: "temporary subtitle output path is required"}
	}
	if plan.Action == SubtitleActionCopy {
		return nil, &SubtitleError{Code: "subtitle_ffmpeg_not_required", Message: "ASS copy plans do not require FFmpeg"}
	}
	if !isTextSubtitle(plan.InputFormat) {
		return nil, &SubtitleError{Code: "subtitle_format_unsupported", Message: "subtitle format cannot be converted to ASS"}
	}

	inputPath := plan.InputPath
	streamMap := "0:0"
	switch plan.Source {
	case SubtitleSourceExternal:
		if plan.Action != SubtitleActionConvert {
			return nil, &SubtitleError{Code: "subtitle_plan_invalid", Message: "external subtitle plan must copy or convert"}
		}
	case SubtitleSourceEmbedded:
		if plan.Action != SubtitleActionExtract || plan.StreamIndex < 0 {
			return nil, &SubtitleError{Code: "subtitle_plan_invalid", Message: "embedded subtitle plan requires a stream index"}
		}
		if inputPath == "" {
			inputPath = videoPath
		}
		streamMap = "0:" + strconv.Itoa(plan.StreamIndex)
	default:
		return nil, &SubtitleError{Code: "subtitle_plan_invalid", Message: "subtitle source is invalid"}
	}
	if strings.TrimSpace(inputPath) == "" {
		return nil, &SubtitleError{Code: "subtitle_plan_invalid", Message: "subtitle input path is required"}
	}

	return []string{
		"-y", "-i", inputPath,
		"-map", streamMap, "-vn", "-an", "-c:s", "ass", "-f", "ass",
		temporaryOutput,
	}, nil
}

func ValidateASS(content []byte) error {
	return validateASSStructure(content, true)
}

// ValidateASSCandidate accepts FFmpeg-demuxed ASS that omits Script Info but
// still contains usable styles and dialogue. Normalization adds the missing
// final-output section before strict validation.
func ValidateASSCandidate(content []byte) error {
	return validateASSStructure(content, false)
}

func validateASSStructure(content []byte, requireScriptInfo bool) error {
	content = bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	scanner := newASSScanner(content)
	section := ""
	hasScriptInfo := false
	hasEvents := false
	hasFormat := false
	hasDialogue := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(line)
			switch section {
			case "[script info]":
				hasScriptInfo = true
			case "[events]":
				hasEvents = true
			}
			continue
		}
		if section != "[events]" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "format:") && strings.TrimSpace(line[len("format:"):]) != "" {
			hasFormat = true
		}
		if strings.HasPrefix(lower, "dialogue:") && strings.TrimSpace(line[len("dialogue:"):]) != "" {
			hasDialogue = true
		}
	}
	if err := scanner.Err(); err != nil {
		return &SubtitleError{Code: "subtitle_invalid_ass", Message: fmt.Sprintf("read ASS subtitle: %v", err)}
	}
	if (requireScriptInfo && !hasScriptInfo) || !hasEvents || !hasFormat || !hasDialogue {
		message := "ASS subtitle requires Events, Format, and Dialogue"
		if requireScriptInfo {
			message = "ASS subtitle requires Script Info, Events, Format, and Dialogue"
		}
		return &SubtitleError{Code: "subtitle_invalid_ass", Message: message}
	}
	return nil
}

func newASSScanner(content []byte) *bufio.Scanner {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), maxASSScanTokenBytes)
	return scanner
}

func isTextSubtitle(format SubtitleFormat) bool {
	switch format {
	case SubtitleASS, SubtitleSSA, SubtitleSRT, SubtitleWebVTT, SubtitleMovText:
		return true
	default:
		return false
	}
}

func subtitleFormatScore(format SubtitleFormat) int {
	if format == SubtitleASS {
		return 5
	}
	return 0
}

func SubtitleStreamNeedsContentInspection(stream SubtitleStream) bool {
	if !isTextSubtitle(stream.Format) {
		return false
	}
	evidence := subtitleMetadataEvidence(stream.Language, stream.Title)
	return evidence != SubtitleEvidenceSimplified && evidence != SubtitleEvidenceOther
}

func isSimplifiedChineseSubtitle(language, label string) bool {
	return subtitleMetadataEvidence(language, label) == SubtitleEvidenceSimplified
}

func subtitleMetadataEvidence(language, label string) SubtitleEvidence {
	language = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(language), "_", "-"))
	simplified, traditional, otherLanguage := subtitleLabelSignals(label)

	switch language {
	case "zh-hans", "zh-cn", "zh-sg", "chs", "sc", "gb", "gb2312", "gbk":
		if traditional {
			return SubtitleEvidenceMixed
		}
		return SubtitleEvidenceSimplified
	case "zh-hant", "zh-tw", "zh-hk", "zh-mo", "cht", "tc", "big5":
		if simplified {
			return SubtitleEvidenceMixed
		}
		return SubtitleEvidenceTraditional
	case "", "und", "unknown":
		return subtitleLabelEvidence(simplified, traditional, otherLanguage, SubtitleEvidenceUnknown)
	case "chi", "zho", "zh":
		return subtitleLabelEvidence(simplified, traditional, otherLanguage, SubtitleEvidenceGeneric)
	default:
		return SubtitleEvidenceOther
	}
}

func subtitleLabelEvidence(simplified, traditional, otherLanguage bool, fallback SubtitleEvidence) SubtitleEvidence {
	switch {
	case simplified && traditional:
		return SubtitleEvidenceMixed
	case simplified:
		return SubtitleEvidenceSimplified
	case traditional:
		return SubtitleEvidenceTraditional
	case otherLanguage:
		return SubtitleEvidenceOther
	default:
		return fallback
	}
}

func subtitleLabelSignals(label string) (simplified, traditional, otherLanguage bool) {
	lower := strings.ToLower(strings.TrimSpace(label))
	normalized := normalizeSubtitleLabel(lower)

	simplified = containsAny(lower,
		"简体", "簡體", "简中", "簡中", "简繁", "簡繁", "繁简", "繁簡",
		"简日", "簡日", "日简", "日簡", "简英", "簡英", "英简", "英簡",
	) || containsSubtitleLabelToken(normalized,
		"zh hans", "zh cn", "zh sg", "chs", "sc", "gb", "gb2312", "gbk", "simplified", "simplified chinese",
		"jpsc", "scjp", "jpnsc", "scjpn",
	)
	traditional = containsAny(lower, "繁体", "繁體", "繁中", "繁日", "繁英", "简繁", "簡繁", "繁简", "繁簡") || containsSubtitleLabelToken(normalized,
		"zh hant", "zh tw", "zh hk", "zh mo", "cht", "tc", "big5", "traditional", "traditional chinese",
		"jptc", "tcjp", "jpntc", "tcjpn",
	)
	otherLanguage = containsAny(lower,
		"简英", "簡英", "简日", "簡日", "中英", "中日", "双语", "雙語", "英文", "英语", "英語", "日文", "日语", "日語",
	) || containsSubtitleLabelToken(normalized,
		"en", "eng", "english", "ja", "jpn", "jp", "japanese", "bilingual", "multilingual",
	)
	return simplified, traditional, otherLanguage
}

func normalizeSubtitleLabel(label string) string {
	fields := strings.FieldsFunc(label, func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsNumber(character)
	})
	return " " + strings.Join(fields, " ") + " "
}

func containsSubtitleLabelToken(normalized string, tokens ...string) bool {
	for _, token := range tokens {
		if strings.Contains(normalized, " "+token+" ") {
			return true
		}
	}
	return false
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
