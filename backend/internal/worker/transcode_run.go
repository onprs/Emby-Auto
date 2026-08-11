package worker

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type TranscodeStore interface {
	BeginTranscode(context.Context, uuid.UUID) (domain.TaskMediaCommand, error)
	CompleteArtifact(context.Context, domain.MediaArtifactCompletion) error
}

type TranscodeRunHandler struct {
	configuration MediaConfiguration
	tools         MediaTools
	store         TranscodeStore
}

func NewTranscodeRunHandler(configuration MediaConfiguration, tools MediaTools, store TranscodeStore) *TranscodeRunHandler {
	return &TranscodeRunHandler{configuration: configuration, tools: tools, store: store}
}

func (handler *TranscodeRunHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "episode_task" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_transcode_operation", "transcode.run requires an episode task resource", nil)
	}
	if handler.tools == nil || handler.store == nil {
		return permanentFailure("transcode_handler_not_configured", "transcode handler dependencies are unavailable", nil)
	}
	command, err := handler.store.BeginTranscode(ctx, operation.ResourceID)
	if err != nil {
		return mediaStoreFailure("transcode", err)
	}
	if command.TaskID != operation.ResourceID {
		return permanentFailure("transcode_resource_mismatch", "the operation does not match its episode task", nil)
	}
	if command.VideoState == domain.VideoReady {
		return nil
	}
	settings, err := loadMediaSettings(ctx, handler.configuration)
	if err != nil {
		return err
	}
	sourcePath, err := secureJoin(command.SavePath, command.SourceVideoRelativePath)
	if err != nil {
		return permanentFailure("source_video_path_invalid", "the source video path is unsafe", err)
	}
	paths, err := buildMediaOutputPaths(settings.Paths.StagingRoot, command.TaskID, operation.ID, taskMediaOutputPath(command, command.Names.VideoName))
	if err != nil {
		return permanentFailure("transcode_output_path_invalid", "the transcode output path is invalid", err)
	}
	if err := prepareOutputDirectory(paths); err != nil {
		return retryableFailure("staging_unavailable", "the transcode staging directory is unavailable", err)
	}
	defer func() { _ = os.Remove(paths.Temporary) }()

	inputProbe, err := handler.tools.Probe(ctx, settings.Paths.FFprobePath, sourcePath)
	if err != nil {
		return retryableFailure("source_video_probe_failed", "the source video could not be probed", err)
	}
	expectation, err := domain.BuildTranscodeProbeExpectation(command.TranscodeProfile)
	if err != nil {
		return permanentFailure("transcode_profile_invalid", "the task transcode profile is invalid", err)
	}

	outputPath := paths.Final
	if _, statErr := os.Stat(paths.Final); os.IsNotExist(statErr) {
		args, err := domain.BuildTranscodeFFmpegArgs(command.TranscodeProfile, sourcePath, paths.Temporary)
		if err != nil {
			return permanentFailure("transcode_profile_invalid", "the task transcode command is invalid", err)
		}
		if err := handler.tools.RunFFmpeg(ctx, settings.Paths.FFmpegPath, args); err != nil {
			return retryableFailure("ffmpeg_transcode_failed", "FFmpeg video transcoding failed", err)
		}
		outputProbe, err := handler.tools.Probe(ctx, settings.Paths.FFprobePath, paths.Temporary)
		if err != nil {
			return retryableFailure("transcode_output_probe_failed", "the temporary video output could not be probed", err)
		}
		if err := domain.ValidateTranscodeProbe(expectation, inputProbe, outputProbe, paths.Temporary); err != nil {
			return permanentFailure("transcode_output_invalid", "the temporary video output does not match its profile", err)
		}
		if err := commitTemporaryFile(paths.Temporary, paths.Final); err != nil {
			return retryableFailure("transcode_output_commit_failed", "the video output could not be committed atomically", err)
		}
	} else if statErr != nil {
		return retryableFailure("staging_unavailable", "the existing video output could not be inspected", statErr)
	} else {
		outputProbe, err := handler.tools.Probe(ctx, settings.Paths.FFprobePath, paths.Final)
		if err != nil {
			return retryableFailure("transcode_output_probe_failed", "the existing video output could not be probed", err)
		}
		if err := domain.ValidateTranscodeProbe(expectation, inputProbe, outputProbe, paths.Final); err != nil {
			return permanentFailure("transcode_output_conflict", "the existing video output does not match its profile", err)
		}
	}

	size, checksum, err := fileIdentity(outputPath)
	if err != nil {
		return retryableFailure("transcode_output_unavailable", "the committed video output is unavailable", err)
	}
	if err := handler.store.CompleteArtifact(ctx, domain.MediaArtifactCompletion{
		TaskID:             command.TaskID,
		OperationID:        operation.ID,
		SourceFileID:       command.SourceVideoFileID,
		TranscodeProfileID: command.TranscodeProfileID,
		Kind:               domain.MediaVideo,
		BaseName:           command.Names.BaseName,
		FilePath:           outputPath,
		Format:             command.TranscodeProfile.Container,
		SizeBytes:          size,
		ChecksumSHA256:     checksum,
		Metadata: map[string]any{
			"encoder":    command.TranscodeProfile.Encoder,
			"videoCodec": command.TranscodeProfile.VideoCodec,
		},
	}); err != nil {
		return mediaStoreFailure("transcode", fmt.Errorf("complete video artifact: %w", err))
	}
	return nil
}

func mediaStoreFailure(branch string, err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return permanentFailure("task_not_found", "the episode task no longer exists", err)
	}
	var workflowErr *domain.MediaWorkflowError
	if errors.As(err, &workflowErr) {
		return &Failure{Code: workflowErr.Code, Message: workflowErr.Message, Retryable: workflowErr.Retryable, Cause: err}
	}
	return retryableFailure("media_storage_unavailable", branch+" state could not be persisted", err)
}
