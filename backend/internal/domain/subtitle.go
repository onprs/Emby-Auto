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

	"github.com/google/uuid"
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
	SourceFileID uuid.UUID
	Path         string
	// RelativePath 相对下载根目录，用于对外展示与字幕检查，避免泄漏宿主路径。
	RelativePath string
	Format       SubtitleFormat
	Language     string
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
	Source       SubtitleSource
	SourceFileID uuid.UUID
	Action       SubtitleAction
	InputPath    string
	StreamIndex  int
	InputFormat  SubtitleFormat
	Language     string
	Evidence     SubtitleEvidence
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
	VideoPath string
	// BasePath 是外挂字幕相对路径的解析基准，仅在后端内部使用。
	BasePath    string
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

type SubtitleMatchSelection struct {
	CandidateID string
	Path        string
}

// SubtitleVideoMatchSchedulingState 描述字幕匹配范围当前的 Agent 调度状态。
// 用于区分“确实已有活动决议承接”与“范围存在但从未成功调度”。
// CancellationRequested 表示操作已请求取消，此时应等待取消收敛而不是重新调度。
type SubtitleVideoMatchSchedulingState struct {
	ScopeID               uuid.UUID
	ScopeStatus           string
	TaskVersion           int
	TaskState             string
	SubtitleState         string
	ContentObservation    [32]byte
	ResolutionID          *uuid.UUID
	ResolutionStatus      string
	ResolutionErrorCode   string
	ResolutionVersion     int
	OperationID           *uuid.UUID
	OperationStatus       string
	CancellationRequested bool
	ActiveOwnerCount      int
}

// SubtitleScopeObservation 是 handler 在同一个读取事务中看到的 scope/task/
// content 观察。ContentHash 只用于检测同一 handler 观察之后的候选/视频/
// mapping 变化，不是持久 generation，也不参与 owner/operation 租约。
type SubtitleScopeObservation struct {
	TaskVersion   int
	TaskState     string
	SubtitleState string
	ContentHash   [32]byte
}

// SubtitleScopeExpiryOutcome 是 orphan cleanup 的结构化结果；调用方必须按
// outcome 决定 handoff/重建/失败，不能把 nil 当“已过期”。
type SubtitleScopeExpiryOutcome string

const (
	SubtitleScopeExpired        SubtitleScopeExpiryOutcome = "expired"
	SubtitleScopeOwnerPresent   SubtitleScopeExpiryOutcome = "owner_present"
	SubtitleScopeMultipleOwners SubtitleScopeExpiryOutcome = "multiple_owners"
	SubtitleScopeAlreadyApplied SubtitleScopeExpiryOutcome = "already_applied"
	SubtitleScopeAlreadyExpired SubtitleScopeExpiryOutcome = "already_expired"
	SubtitleScopeAbsent         SubtitleScopeExpiryOutcome = "absent"
	SubtitleScopeChanged        SubtitleScopeExpiryOutcome = "scope_changed"
	SubtitleScopeConflict       SubtitleScopeExpiryOutcome = "conflict"
)

// SubtitleScopeExpiry 返回当前锁内观察与 orphan cleanup 结果，便于 handler
// 区分真正由本调用过期、合法 owner/applied、新代次变化与冲突。
type SubtitleScopeExpiry struct {
	ScopeID           uuid.UUID
	Outcome           SubtitleScopeExpiryOutcome
	ScopeStatus       string
	OwnerCount        int
	OwnerResolutionID *uuid.UUID
	OwnerOperationID  *uuid.UUID
	TaskVersion       int
	TaskState         string
	SubtitleState     string
	ContentHash       [32]byte
}

// ActiveResolution 表示当前范围内恰好存在唯一活动 owner（决议与 operation）。
// 0 个或多于 1 个活动 owner 都不能授权调度；历史 created_at 排序不参与判定。
// 终态失败、取消或过期不由这里判定；调用方需结合
// AutomaticAgentResolutionRetryable 决定恢复。
func (state SubtitleVideoMatchSchedulingState) ActiveResolution() bool {
	if state.ActiveOwnerCount != 1 || state.ResolutionID == nil {
		return false
	}
	switch state.ResolutionStatus {
	case string(AgentResolutionQueued), string(AgentResolutionRunning), string(AgentResolutionProposed),
		string(AgentResolutionReviewRequired):
	default:
		return false
	}
	switch state.OperationStatus {
	case "queued", "running":
		return true
	default:
		return false
	}
}

// CoversGeneration 表示唯一活动 owner 仍在承接当前 scope；正常 VideoReady
// 引起的 task.version 推进不改变该判定。
func (state SubtitleVideoMatchSchedulingState) CoversGeneration() bool {
	return state.ActiveResolution()
}

// CandidateID returns the stable identifier used to reference a subtitle
// candidate across scope persistence, the Agent context, and the inspection
// tool. Embedded streams are keyed by their stream index; external files use
// the persisted download-file UUID.
func CandidateID(plan SubtitlePlan) string {
	if plan.Source == SubtitleSourceEmbedded {
		return "stream:" + strconv.Itoa(plan.StreamIndex)
	}
	if plan.SourceFileID != uuid.Nil {
		return "external:" + plan.SourceFileID.String()
	}
	return LegacyCandidateID(plan)
}

// LegacyCandidateID reproduces the pre-UUID external subtitle identity so
// pending selections created before the upgrade can still be replayed.
func LegacyCandidateID(plan SubtitlePlan) string {
	if plan.Source == SubtitleSourceEmbedded {
		return "stream:" + strconv.Itoa(plan.StreamIndex)
	}
	return "file:" + path.Base(strings.ReplaceAll(plan.InputPath, `\\`, "/"))
}

func MatchesLegacyCandidateID(plan SubtitlePlan, candidateID string) bool {
	if LegacyCandidateID(plan) == candidateID {
		return true
	}
	normalizedPath := strings.ReplaceAll(plan.InputPath, "\\", "/")
	return candidateID == "file:"+path.Base(normalizedPath)
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
				Source: SubtitleSourceExternal, SourceFileID: candidate.SourceFileID,
				Action: SubtitleActionConvert, InputPath: candidate.Path,
				StreamIndex: -1, InputFormat: candidate.Format,
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
