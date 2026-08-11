package domain

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/longbridgeapp/opencc"
)

type ChineseScript string

const (
	ChineseScriptUnknown     ChineseScript = "unknown"
	ChineseScriptSimplified  ChineseScript = "simplified"
	ChineseScriptTraditional ChineseScript = "traditional"

	minimumChineseScriptEvidence = 20
	chineseScriptDominanceRatio  = 3
)

type ChineseScriptAnalysis struct {
	Script              ChineseScript
	SimplifiedEvidence  int
	TraditionalEvidence int
	HanCharacters       int
	KanaCharacters      int
}

var (
	assOverridePattern = regexp.MustCompile(`\{[^}]*\}`)
	openCCOnce         sync.Once
	openCCS2T          *opencc.OpenCC
	openCCT2S          *opencc.OpenCC
	openCCErr          error
)

// AnalyzeASSChineseScript classifies only ASS Dialogue text. A result remains
// unknown unless one script has enough character-level conversion evidence and
// clearly dominates the other.
func AnalyzeASSChineseScript(content []byte) (ChineseScriptAnalysis, error) {
	text, err := assDialogueText(content)
	if err != nil {
		return ChineseScriptAnalysis{}, err
	}
	if err := initializeSubtitleOpenCC(); err != nil {
		return ChineseScriptAnalysis{}, err
	}

	frequencies := make(map[rune]int)
	analysis := ChineseScriptAnalysis{Script: ChineseScriptUnknown}
	for _, character := range text {
		switch {
		case unicode.Is(unicode.Han, character):
			frequencies[character]++
			analysis.HanCharacters++
		case unicode.In(character, unicode.Hiragana, unicode.Katakana):
			analysis.KanaCharacters++
		}
	}
	for character, count := range frequencies {
		value := string(character)
		traditional, err := openCCS2T.Convert(value)
		if err != nil {
			return ChineseScriptAnalysis{}, fmt.Errorf("convert simplified Chinese evidence: %w", err)
		}
		if traditional != value {
			analysis.SimplifiedEvidence += count
		}
		simplified, err := openCCT2S.Convert(value)
		if err != nil {
			return ChineseScriptAnalysis{}, fmt.Errorf("convert traditional Chinese evidence: %w", err)
		}
		if simplified != value {
			analysis.TraditionalEvidence += count
		}
	}

	switch {
	case dominantChineseScriptEvidence(analysis.SimplifiedEvidence, analysis.TraditionalEvidence):
		analysis.Script = ChineseScriptSimplified
	case dominantChineseScriptEvidence(analysis.TraditionalEvidence, analysis.SimplifiedEvidence):
		analysis.Script = ChineseScriptTraditional
	}
	return analysis, nil
}

func initializeSubtitleOpenCC() error {
	openCCOnce.Do(func() {
		openCCS2T, openCCErr = opencc.New("s2t")
		if openCCErr != nil {
			return
		}
		openCCT2S, openCCErr = opencc.New("t2s")
	})
	if openCCErr != nil {
		return fmt.Errorf("initialize OpenCC: %w", openCCErr)
	}
	return nil
}

func SubtitleContentIsLikelyChinese(analysis ChineseScriptAnalysis, evidence SubtitleEvidence) bool {
	switch evidence {
	case SubtitleEvidenceSimplified, SubtitleEvidenceMixed, SubtitleEvidenceTraditional:
		return true
	case SubtitleEvidenceGeneric:
		return analysis.HanCharacters >= 4 && analysis.HanCharacters >= analysis.KanaCharacters*2
	case SubtitleEvidenceUnknown:
		return analysis.HanCharacters >= 20 && analysis.HanCharacters >= analysis.KanaCharacters*3
	default:
		return false
	}
}

// NormalizeASSToSimplified converts only Dialogue text while preserving ASS
// sections, styles, and override tags.
func NormalizeASSToSimplified(content []byte) ([]byte, error) {
	if err := ValidateASSCandidate(content); err != nil {
		return nil, err
	}
	if err := initializeSubtitleOpenCC(); err != nil {
		return nil, err
	}
	content = bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	scanner := newASSScanner(content)
	section := ""
	textColumn := -1
	columnCount := 0
	var output strings.Builder
	if !assHasSection(content, "[script info]") {
		output.WriteString("[Script Info]\nScriptType: v4.00+\n\n")
	}
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(trimmed)
		} else if section == "[events]" {
			lower := strings.ToLower(trimmed)
			switch {
			case strings.HasPrefix(lower, "format:"):
				columns := strings.Split(trimmed[len("format:"):], ",")
				textColumn = -1
				columnCount = len(columns)
				for index, column := range columns {
					if strings.EqualFold(strings.TrimSpace(column), "text") {
						textColumn = index
						break
					}
				}
			case strings.HasPrefix(lower, "dialogue:") && textColumn >= 0:
				fields := strings.SplitN(trimmed[len("dialogue:"):], ",", columnCount)
				if len(fields) > textColumn {
					converted, err := convertASSVisibleText(fields[textColumn])
					if err != nil {
						return nil, err
					}
					fields[textColumn] = converted
					line = "Dialogue:" + strings.Join(fields, ",")
				}
			}
		}
		output.WriteString(line)
		output.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, &SubtitleError{Code: "subtitle_invalid_ass", Message: fmt.Sprintf("read ASS subtitle for normalization: %v", err)}
	}
	normalized := []byte(output.String())
	if err := ValidateASS(normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func assHasSection(content []byte, target string) bool {
	scanner := newASSScanner(bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf}))
	for scanner.Scan() {
		if strings.EqualFold(strings.TrimSpace(scanner.Text()), target) {
			return true
		}
	}
	return false
}

func convertASSVisibleText(text string) (string, error) {
	matches := assOverridePattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		converted, err := openCCT2S.Convert(text)
		if err != nil {
			return "", fmt.Errorf("convert ASS dialogue to simplified Chinese: %w", err)
		}
		return converted, nil
	}
	var output strings.Builder
	cursor := 0
	for _, match := range matches {
		converted, err := openCCT2S.Convert(text[cursor:match[0]])
		if err != nil {
			return "", fmt.Errorf("convert ASS dialogue to simplified Chinese: %w", err)
		}
		output.WriteString(converted)
		output.WriteString(text[match[0]:match[1]])
		cursor = match[1]
	}
	converted, err := openCCT2S.Convert(text[cursor:])
	if err != nil {
		return "", fmt.Errorf("convert ASS dialogue to simplified Chinese: %w", err)
	}
	output.WriteString(converted)
	return output.String(), nil
}

func dominantChineseScriptEvidence(primary, secondary int) bool {
	return primary >= minimumChineseScriptEvidence && primary >= secondary*chineseScriptDominanceRatio
}

// ASSDialogueText returns the concatenated visible Dialogue text of an ASS
// subtitle, with override tags and line decorations removed. It is used to
// build bounded samples for Agent inspection.
func ASSDialogueText(content []byte) (string, error) {
	return assDialogueText(content)
}

func assDialogueText(content []byte) (string, error) {
	if err := ValidateASSCandidate(content); err != nil {
		return "", err
	}
	content = bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	scanner := newASSScanner(content)
	section := ""
	textColumn := -1
	columnCount := 0
	hasTextColumn := false
	var dialogue strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(line)
			continue
		}
		if section != "[events]" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "format:") {
			columns := strings.Split(line[len("format:"):], ",")
			textColumn = -1
			columnCount = len(columns)
			for index, column := range columns {
				if strings.EqualFold(strings.TrimSpace(column), "text") {
					textColumn = index
					hasTextColumn = true
					break
				}
			}
			continue
		}
		if !strings.HasPrefix(lower, "dialogue:") || textColumn < 0 || columnCount <= textColumn {
			continue
		}
		fields := strings.SplitN(line[len("dialogue:"):], ",", columnCount)
		if len(fields) <= textColumn {
			continue
		}
		text := assOverridePattern.ReplaceAllString(fields[textColumn], "")
		text = strings.NewReplacer(`\N`, "\n", `\n`, "\n", `\h`, " ").Replace(text)
		dialogue.WriteString(text)
		dialogue.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return "", &SubtitleError{Code: "subtitle_invalid_ass", Message: fmt.Sprintf("read ASS subtitle dialogue: %v", err)}
	}
	if !hasTextColumn {
		return "", &SubtitleError{Code: "subtitle_invalid_ass", Message: "ASS Events format requires a Text column"}
	}
	return dialogue.String(), nil
}
