package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

// SubtitleTextInspector reads a bounded subtitle text sample from a scoped
// candidate so the Agent can judge whether its content matches the video
// episode. It is a read-only inspection tool and never mutates media.
type SubtitleTextInspector struct {
	configuration MediaConfiguration
	tools         MediaTools
}

func NewSubtitleTextInspector(configuration MediaConfiguration, tools MediaTools) *SubtitleTextInspector {
	return &SubtitleTextInspector{configuration: configuration, tools: tools}
}

func (inspector *SubtitleTextInspector) InspectSubtitleText(
	ctx context.Context,
	request domain.SubtitleInspectionRequest,
) (domain.SubtitleInspection, error) {
	if inspector.tools == nil {
		return domain.SubtitleInspection{}, fmt.Errorf("media tools are unavailable")
	}
	settings, err := loadMediaSettings(ctx, inspector.configuration)
	if err != nil {
		return domain.SubtitleInspection{}, err
	}
	plan := domain.SubtitlePlan{
		Source: request.Source, StreamIndex: request.StreamIndex,
		InputFormat: request.Format, InputPath: request.Path,
	}
	if plan.Source == domain.SubtitleSourceEmbedded {
		plan.Action = domain.SubtitleActionExtract
	} else {
		plan.Action = domain.SubtitleActionConvert
	}
	if plan.InputPath == "" {
		plan.InputPath = request.VideoPath
	}
	dir, err := os.MkdirTemp("", "subtitle-inspect-*")
	if err != nil {
		return domain.SubtitleInspection{}, fmt.Errorf("create subtitle inspection directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	output := filepath.Join(dir, "sample.ass")
	args, err := domain.BuildSubtitleFFmpegArgs(plan, request.VideoPath, output)
	if err != nil {
		return domain.SubtitleInspection{}, err
	}
	if err := inspector.tools.RunFFmpeg(ctx, settings.Paths.FFmpegPath, args); err != nil {
		return domain.SubtitleInspection{}, fmt.Errorf("extract subtitle candidate: %w", err)
	}
	content, err := readASSFileContent(output)
	if err != nil {
		return domain.SubtitleInspection{}, err
	}
	analysis, err := domain.AnalyzeASSChineseScript(content)
	if err != nil {
		return domain.SubtitleInspection{}, err
	}
	sample := subtitleTextSample(content)
	return domain.SubtitleInspection{
		CandidateID: request.CandidateID, Found: true, Script: analysis.Script,
		Sample: sample, HanCount: analysis.HanCharacters,
	}, nil
}

func subtitleTextSample(content []byte) string {
	dialogue, err := domain.ASSDialogueText(content)
	if err != nil {
		return ""
	}
	const maxSampleBytes = 4000
	runes := []rune(dialogue)
	if len(runes) > maxSampleBytes {
		runes = runes[:maxSampleBytes]
	}
	return string(runes)
}
