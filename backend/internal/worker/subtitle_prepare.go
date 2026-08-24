package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

const (
	maxASSBytes                  = 64 << 20
	maxSubtitleCandidateAttempts = 16
)

type SubtitleStore interface {
	BeginSubtitle(context.Context, uuid.UUID) (domain.TaskMediaCommand, error)
	CompleteArtifact(context.Context, domain.MediaArtifactCompletion) error
	CreateSubtitleVideoMatchScope(context.Context, uuid.UUID, []domain.SubtitleMatchCandidate) (uuid.UUID, error)
	GetSubtitleVideoMatchSelection(context.Context, uuid.UUID) (string, error)
}

type SubtitleMatchAgentResolutionCreator interface {
	CapabilityEnabled(context.Context, domain.AgentCapability) (bool, error)
	CreateAutomatic(context.Context, service.AutomaticAgentResolutionRequest) (service.AgentResolutionCommandResult, error)
}

type SubtitlePrepareHandler struct {
	configuration MediaConfiguration
	tools         MediaTools
	store         SubtitleStore
	agentResolutions SubtitleMatchAgentResolutionCreator
}

func NewSubtitlePrepareHandler(configuration MediaConfiguration, tools MediaTools, store SubtitleStore, agentResolutions ...SubtitleMatchAgentResolutionCreator) *SubtitlePrepareHandler {
	handler := &SubtitlePrepareHandler{configuration: configuration, tools: tools, store: store}
	if len(agentResolutions) > 0 {
		handler.agentResolutions = agentResolutions[0]
	}
	return handler
}

func (handler *SubtitlePrepareHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "episode_task" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_subtitle_operation", "subtitle.prepare requires an episode task resource", nil)
	}
	if handler.tools == nil || handler.store == nil {
		return permanentFailure("subtitle_handler_not_configured", "subtitle handler dependencies are unavailable", nil)
	}
	command, err := handler.store.BeginSubtitle(ctx, operation.ResourceID)
	if err != nil {
		return mediaStoreFailure("subtitle", err)
	}
	if command.TaskID != operation.ResourceID {
		return permanentFailure("subtitle_resource_mismatch", "the operation does not match its episode task", nil)
	}
	if command.SubtitleState == domain.SubtitleASSReady {
		return nil
	}
	settings, err := loadMediaSettings(ctx, handler.configuration)
	if err != nil {
		return err
	}
	videoPath, err := secureJoin(command.SavePath, command.SourceVideoRelativePath)
	if err != nil {
		return permanentFailure("source_video_path_invalid", "the source video path is unsafe", err)
	}
	probe, err := handler.tools.Probe(ctx, settings.Paths.FFprobePath, videoPath)
	if err != nil {
		return retryableFailure("source_video_probe_failed", "the source video could not be probed for subtitles", err)
	}

	external := make([]domain.SubtitleCandidate, 0, len(command.ExternalSubtitles))
	externalIDs := make(map[string]uuid.UUID, len(command.ExternalSubtitles))
	for _, subtitle := range command.ExternalSubtitles {
		filePath, err := secureJoin(command.SavePath, subtitle.RelativePath)
		if err != nil {
			return permanentFailure("external_subtitle_path_invalid", "an external subtitle path is unsafe", err)
		}
		external = append(external, domain.SubtitleCandidate{Path: filePath, Format: subtitle.Format, Language: subtitle.Language})
		externalIDs[filePath] = subtitle.SourceFileID
	}
	paths, err := buildMediaOutputPaths(settings.Paths.StagingRoot, command.TaskID, operation.ID, taskMediaOutputPath(command, command.Names.SubtitleName))
	if err != nil {
		return permanentFailure("subtitle_output_path_invalid", "the subtitle output path is invalid", err)
	}
	if err := prepareOutputDirectory(paths); err != nil {
		return retryableFailure("staging_unavailable", "the subtitle staging directory is unavailable", err)
	}
	defer func() { _ = os.Remove(paths.Temporary) }()

	selected := domain.SubtitlePlan{
		Source: domain.SubtitleSourceEmbedded, Action: domain.SubtitleActionExtract,
		InputPath: videoPath, StreamIndex: -1, InputFormat: domain.SubtitleASS,
		Language: "zh-Hans", Evidence: domain.SubtitleEvidenceSimplified,
	}
	analysis := domain.ChineseScriptAnalysis{Script: domain.ChineseScriptSimplified}
	attempted := 0

	if _, statErr := os.Stat(paths.Final); os.IsNotExist(statErr) {
		plans, planErr := domain.RankSubtitleCandidates(domain.SubtitleSelectionRequest{
			VideoPath: videoPath,
			External:  external,
			Embedded:  probe.SubtitleStreams(),
		})
		if planErr != nil {
			var subtitleErr *domain.SubtitleError
			if errors.As(planErr, &subtitleErr) {
				return permanentFailure(subtitleErr.Code, subtitleErr.Message, planErr)
			}
			return permanentFailure("subtitle_selection_failed", "subtitle candidates could not be ranked", planErr)
		}

		if len(plans) >= 2 && handler.agentResolutions != nil {
			enabled, capErr := handler.agentResolutions.CapabilityEnabled(ctx, domain.AgentCapabilitySubtitleVideoMatch)
			if capErr != nil {
				return retryableFailure("agent_configuration_unavailable", "Agent subtitle matching capability could not be loaded", capErr)
			}
			if enabled {
				applied, selectionErr := handler.store.GetSubtitleVideoMatchSelection(ctx, command.TaskID)
				if selectionErr != nil {
					return mediaStoreFailure("subtitle", selectionErr)
				}
				if applied != "" {
					plans = filterPlansByCandidate(plans, applied)
					if len(plans) == 0 {
						return permanentFailure("subtitle_agent_selection_invalid", "the Agent-selected subtitle candidate is no longer available", nil)
					}
				} else {
					// No selection yet: persist scope + candidates and ask the Agent.
						scopeID, scopeErr := handler.store.CreateSubtitleVideoMatchScope(ctx, command.TaskID, subtitleMatchCandidates(plans, probe.SubtitleStreams(), external))
					if scopeErr != nil {
						var mediaErr *domain.MediaWorkflowError
						if errors.As(scopeErr, &mediaErr) && mediaErr.Code == "subtitle_scope_conflict" {
							// Scope already exists; the Agent resolution is already scheduled.
							return nil
						}
						return mediaStoreFailure("subtitle", scopeErr)
					}
					if scopeID == uuid.Nil {
						return permanentFailure("subtitle_scope_invalid", "the subtitle video match scope could not be created", nil)
					}
					if _, agentErr := handler.agentResolutions.CreateAutomatic(ctx, service.AutomaticAgentResolutionRequest{
						Capability: domain.AgentCapabilitySubtitleVideoMatch, ResourceID: scopeID,
					}); agentErr != nil && !errors.Is(agentErr, service.ErrStateConflict) {
						return retryableFailure("agent_resolution_schedule_failed", "the Agent subtitle matching resolution could not be scheduled", agentErr)
					}
					// Wait for the Agent to select a candidate; the apply step schedules a
					// fresh subtitle.prepare that carries the applied selection.
					return nil
				}
			}
		}

		var retryable bool
		selected, analysis, attempted, retryable, err = handler.prepareFirstUsableSubtitle(
			ctx, settings.Paths.FFmpegPath, videoPath, paths.Temporary, plans,
		)
		if err != nil {
			if retryable {
				return retryableFailure("ffmpeg_subtitle_candidates_failed", "all subtitle candidates failed during FFmpeg preparation", err)
			}
			var subtitleErr *domain.SubtitleError
			if errors.As(err, &subtitleErr) {
				return permanentFailure(subtitleErr.Code, subtitleErr.Message, err)
			}
			return permanentFailure("subtitle_candidates_exhausted", "no subtitle candidate produced a usable simplified ASS", err)
		}
		if err := commitTemporaryFile(paths.Temporary, paths.Final); err != nil {
			return retryableFailure("subtitle_output_commit_failed", "the subtitle output could not be committed atomically", err)
		}
	} else if statErr != nil {
		return retryableFailure("staging_unavailable", "the existing subtitle output could not be inspected", statErr)
	} else if err := validateASSFile(paths.Final); err != nil {
		return permanentFailure("subtitle_output_conflict", "the existing ASS subtitle is invalid", err)
	}

	sourceFileID := command.SourceVideoFileID
	if selected.Source == domain.SubtitleSourceExternal {
		var ok bool
		sourceFileID, ok = externalIDs[selected.InputPath]
		if !ok {
			return permanentFailure("subtitle_plan_invalid", "the selected external subtitle has no source identity", nil)
		}
	}
	size, checksum, err := fileIdentity(paths.Final)
	if err != nil {
		return retryableFailure("subtitle_output_unavailable", "the committed subtitle output is unavailable", err)
	}
	if err := handler.store.CompleteArtifact(ctx, domain.MediaArtifactCompletion{
		TaskID:         command.TaskID,
		OperationID:    operation.ID,
		SourceFileID:   sourceFileID,
		Kind:           domain.MediaSubtitle,
		BaseName:       command.Names.BaseName,
		FilePath:       paths.Final,
		Format:         "ass",
		SizeBytes:      size,
		ChecksumSHA256: checksum,
		Metadata: map[string]any{
			"action":              selected.Action,
			"language":            selected.Language,
			"source":              selected.Source,
			"sourceFormat":        selected.InputFormat,
			"languageEvidence":    selected.Evidence,
			"candidateAttempts":   attempted,
			"simplifiedEvidence":  analysis.SimplifiedEvidence,
			"traditionalEvidence": analysis.TraditionalEvidence,
			"hanCharacters":       analysis.HanCharacters,
			"kanaCharacters":      analysis.KanaCharacters,
		},
	}); err != nil {
		return mediaStoreFailure("subtitle", fmt.Errorf("complete subtitle artifact: %w", err))
	}
	return nil
}

func (handler *SubtitlePrepareHandler) prepareFirstUsableSubtitle(
	ctx context.Context,
	ffmpegPath string,
	videoPath string,
	temporaryPath string,
	plans []domain.SubtitlePlan,
) (domain.SubtitlePlan, domain.ChineseScriptAnalysis, int, bool, error) {
	if len(plans) > maxSubtitleCandidateAttempts {
		plans = plans[:maxSubtitleCandidateAttempts]
	}
	failures := make([]error, 0, len(plans))
	ffmpegFailures := 0
	for index, plan := range plans {
		attempt := index + 1
		if err := os.Remove(temporaryPath); err != nil && !os.IsNotExist(err) {
			return domain.SubtitlePlan{}, domain.ChineseScriptAnalysis{}, attempt, true, fmt.Errorf("remove stale subtitle candidate output: %w", err)
		}
		args, err := domain.BuildSubtitleFFmpegArgs(plan, videoPath, temporaryPath)
		if err != nil {
			failures = append(failures, fmt.Errorf("candidate %d plan: %w", attempt, err))
			continue
		}
		if err := handler.tools.RunFFmpeg(ctx, ffmpegPath, args); err != nil {
			ffmpegFailures++
			failures = append(failures, fmt.Errorf("candidate %d FFmpeg: %w", attempt, err))
			continue
		}
		content, err := readSubtitleCandidateFile(temporaryPath)
		if err != nil {
			failures = append(failures, fmt.Errorf("candidate %d output: %w", attempt, err))
			continue
		}
		analysis, err := domain.AnalyzeASSChineseScript(content)
		if err != nil {
			failures = append(failures, fmt.Errorf("candidate %d analysis: %w", attempt, err))
			continue
		}
		if !domain.SubtitleContentIsLikelyChinese(analysis, plan.Evidence) {
			failures = append(failures, fmt.Errorf("candidate %d content is not confidently Chinese", attempt))
			continue
		}
		normalized, err := domain.NormalizeASSToSimplified(content)
		if err != nil {
			failures = append(failures, fmt.Errorf("candidate %d normalization: %w", attempt, err))
			continue
		}
		if err := os.WriteFile(temporaryPath, normalized, 0o640); err != nil {
			return domain.SubtitlePlan{}, domain.ChineseScriptAnalysis{}, attempt, true, fmt.Errorf("write normalized subtitle candidate: %w", err)
		}
		if err := validateASSFile(temporaryPath); err != nil {
			failures = append(failures, fmt.Errorf("candidate %d normalized output: %w", attempt, err))
			continue
		}
		return plan, analysis, attempt, false, nil
	}

	joined := errors.Join(failures...)
	if ffmpegFailures > 0 {
		return domain.SubtitlePlan{}, domain.ChineseScriptAnalysis{}, len(plans), true, joined
	}
	return domain.SubtitlePlan{}, domain.ChineseScriptAnalysis{}, len(plans), false, errors.Join(
		&domain.SubtitleError{Code: "subtitle_candidates_exhausted", Message: "no subtitle candidate produced a usable simplified ASS"},
		joined,
	)
}

func readSubtitleCandidateFile(filePath string) ([]byte, error) {
	content, err := readASSFileContent(filePath)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateASSCandidate(content); err != nil {
		return nil, err
	}
	return content, nil
}

func readASSFileContent(filePath string) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 || info.Size() > maxASSBytes {
		return nil, fmt.Errorf("ASS subtitle size must be between 1 and %d bytes", maxASSBytes)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxASSBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxASSBytes {
		return nil, fmt.Errorf("ASS subtitle exceeds %d bytes", maxASSBytes)
	}
	return content, nil
}

func validateASSFile(filePath string) error {
	content, err := readASSFileContent(filePath)
	if err != nil {
		return err
	}
	return domain.ValidateASS(content)
}

func subtitleMatchCandidates(plans []domain.SubtitlePlan, embedded []domain.SubtitleStream, external []domain.SubtitleCandidate) []domain.SubtitleMatchCandidate {
	streamByIndex := make(map[int]domain.SubtitleStream, len(embedded))
	for _, stream := range embedded {
		streamByIndex[stream.Index] = stream
	}
	candidates := make([]domain.SubtitleMatchCandidate, 0, len(plans))
	for _, plan := range plans {
		candidate := domain.SubtitleMatchCandidate{
			CandidateID: domain.CandidateID(plan), Source: plan.Source, StreamIndex: plan.StreamIndex,
			Format: plan.InputFormat, Language: plan.Language,
		}
		if plan.Source == domain.SubtitleSourceExternal {
			candidate.Title = plan.InputPath
			candidate.Path = plan.InputPath
			for _, externalCandidate := range external {
				if externalCandidate.Path == plan.InputPath {
					candidate.Language = externalCandidate.Language
					break
				}
			}
		} else if stream, ok := streamByIndex[plan.StreamIndex]; ok {
			candidate.Title = stream.Title
			candidate.Language = stream.Language
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}

func filterPlansByCandidate(plans []domain.SubtitlePlan, candidateID string) []domain.SubtitlePlan {
	filtered := make([]domain.SubtitlePlan, 0, 1)
	for _, plan := range plans {
		if domain.CandidateID(plan) == candidateID {
			filtered = append(filtered, plan)
		}
	}
	return filtered
}
