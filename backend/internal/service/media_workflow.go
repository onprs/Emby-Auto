package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type MediaWorkflow struct {
	queries    *db.Queries
	transactor *database.Transactor
	operations *OperationScheduler
}

func NewMediaWorkflow(
	queries *db.Queries,
	transactor *database.Transactor,
	operations *OperationScheduler,
) *MediaWorkflow {
	return &MediaWorkflow{queries: queries, transactor: transactor, operations: operations}
}

func (workflow *MediaWorkflow) MaterializeDownload(
	ctx context.Context,
	downloadID uuid.UUID,
	operationID uuid.UUID,
) error {
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		lockedAcquisitionID, err := scope.Queries.LockMaterializeAcquisitionForDownload(ctx, repository.UUIDToPG(downloadID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock acquisition for materialization: %w", err)
		}
		locked, err := scope.Queries.LockDownloadForMaterialize(ctx, repository.UUIDToPG(downloadID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock download for materialization: %w", err)
		}
		if locked.WorkflowAcquisitionID != lockedAcquisitionID {
			return mediaWorkflowError("download_acquisition_conflict", "the download acquisition changed before materialization", true)
		}
		if locked.Status == string(domain.DownloadMaterialized) {
			return nil
		}
		if locked.Status != string(domain.DownloadCompleted) {
			return mediaWorkflowError("download_state_conflict", fmt.Sprintf("download cannot materialize from state %q", locked.Status), false)
		}
		mediaType := domain.TaskMediaEpisode
		switch locked.MediaType {
		case "tv":
		case "movie":
			mediaType = domain.TaskMediaMovie
		default:
			return mediaWorkflowError("media_type_invalid", "the acquisition has an unsupported media type", false)
		}
		if mediaType == domain.TaskMediaEpisode && !locked.MappingProfileID.Valid {
			return mediaWorkflowError("mapping_profile_required", "the episode acquisition requires a mapping profile before materialization", false)
		}
		if mediaType == domain.TaskMediaMovie && (locked.ReleaseYear == nil || locked.TmdbMovieID == nil) {
			return mediaWorkflowError("movie_metadata_required", "the movie acquisition requires canonical TMDb title and release year", false)
		}

		profileRow, err := scope.Queries.GetDefaultTranscodeProfile(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return mediaWorkflowError("transcode_profile_required", "an active default transcode profile is required", false)
		}
		if err != nil {
			return fmt.Errorf("load default transcode profile: %w", err)
		}
		profile, err := transcodeProfileFromDB(profileRow)
		if err != nil {
			return mediaWorkflowError("transcode_profile_invalid", err.Error(), false)
		}

		videos, err := scope.Queries.ListMaterializeVideos(ctx, repository.UUIDToPG(downloadID))
		if err != nil {
			return fmt.Errorf("list selected videos: %w", err)
		}
		if len(videos) == 0 {
			return mediaWorkflowError("download_no_selected_video", "the completed download has no selected episode video", false)
		}
		for _, video := range videos {
			if err := validateMaterializeVideo(video, profile); err != nil {
				return err
			}
		}
		if _, err := scope.Queries.MarkDownloadSelectingFiles(ctx, repository.UUIDToPG(downloadID)); err != nil {
			return fmt.Errorf("mark download selecting files: %w", err)
		}

		for _, video := range videos {
			taskID := deterministicResourceID(string(mediaType) + "-task:" + repository.UUIDFromPG(locked.WorkflowAcquisitionID).String() + ":" + repository.UUIDFromPG(video.FileID).String())
			created := true
			task, err := scope.Queries.CreateEpisodeTask(ctx, db.CreateEpisodeTaskParams{
				ID:                repository.UUIDToPG(taskID),
				AcquisitionID:     locked.WorkflowAcquisitionID,
				SourceVideoFileID: video.FileID,
				MappingID:         nullableUUIDForMediaType(mediaType, video.MappingID), TranscodeProfileID: profileRow.ID,
				MediaType: string(mediaType),
			})
			if errors.Is(err, pgx.ErrNoRows) {
				created = false
				task, err = scope.Queries.GetEpisodeTaskBySource(ctx, db.GetEpisodeTaskBySourceParams{
					AcquisitionID:     locked.WorkflowAcquisitionID,
					SourceVideoFileID: video.FileID,
				})
			}
			if err != nil {
				return fmt.Errorf("create episode task: %w", err)
			}
			if task.MappingID != nullableUUIDForMediaType(mediaType, video.MappingID) || task.TranscodeProfileID != profileRow.ID || task.MediaType != string(mediaType) {
				return mediaWorkflowError("task_materialization_conflict", "an episode task already exists with different mapping or transcode profile", false)
			}

			for _, job := range []struct {
				kind     string
				timeout  time.Duration
				attempts int
				key      string
			}{
				{kind: appqueue.KindSubtitlePrepare, timeout: 30 * time.Minute, attempts: 3, key: "subtitle.prepare:" + taskID.String()},
				{kind: appqueue.KindTranscodeRun, timeout: 24 * time.Hour, attempts: 3, key: "transcode.run:" + taskID.String() + ":" + repository.UUIDFromPG(profileRow.ID).String()},
			} {
				if _, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
					Kind:           job.kind,
					ResourceType:   "episode_task",
					ResourceID:     taskID,
					IdempotencyKey: job.key,
					MaxAttempts:    job.attempts,
					Timeout:        job.timeout,
					Payload:        map[string]any{},
				}); err != nil {
					return fmt.Errorf("schedule %s: %w", job.kind, err)
				}
			}
			if created {
				if err := appendResourceEvent(ctx, scope.Queries, "episode_task", taskID, operationID, uuid.Nil, "task.created", map[string]any{
					"downloadId": downloadID, "mediaType": mediaType,
					"sourceSeason": valueInt32(video.SourceSeason), "sourceEpisode": valueInt32(video.SourceEpisode),
					"targetSeason": valueInt32(video.TargetSeasonNumber), "targetEpisode": valueInt32(video.TargetEpisodeNumber),
					"transcodeProfile": profile.Name,
				}); err != nil {
					return err
				}
			}
		}
		if _, err := scope.Queries.MarkDownloadMaterialized(ctx, repository.UUIDToPG(downloadID)); err != nil {
			return fmt.Errorf("mark download materialized: %w", err)
		}
		return appendResourceEvent(ctx, scope.Queries, "download", downloadID, operationID, uuid.Nil, "download.materialized", map[string]any{
			"status":    domain.DownloadMaterialized,
			"taskCount": len(videos),
		})
	})
}

func nullableUUIDForMediaType(mediaType domain.TaskMediaType, mappingID pgtype.UUID) pgtype.UUID {
	if mediaType == domain.TaskMediaMovie {
		return pgtype.UUID{}
	}
	return mappingID
}

func validateMaterializeVideo(row db.ListMaterializeVideosRow, profile domain.TranscodeProfile) error {
	if row.SourceSeason == nil || row.SourceEpisode == nil {
		return mediaWorkflowError("source_coordinate_missing", "a selected video is missing its source season or episode", false)
	}
	if row.MediaType == "movie" {
		if strings.TrimSpace(row.SeriesTitle) == "" || row.ReleaseYear == nil || row.TmdbMovieID == nil {
			return mediaWorkflowError("movie_metadata_required", "the selected movie requires canonical TMDb title and release year", false)
		}
		if _, err := domain.BuildMovieFileNames(domain.MovieNamingRequest{
			MovieTitle: row.SeriesTitle, ReleaseYear: int(*row.ReleaseYear), VideoExtension: profile.FileExtension,
		}); err != nil {
			return mediaWorkflowError("movie_naming_invalid", err.Error(), false)
		}
		return nil
	}
	if row.MediaType != "tv" || !row.MappingID.Valid || row.MappingStatus == nil || *row.MappingStatus != "mapped" || !row.TargetEpisodeID.Valid || row.TargetSeasonNumber == nil || row.TargetEpisodeNumber == nil || row.TargetEpisodeTitle == nil || strings.TrimSpace(row.SeriesTitle) == "" {
		code := "episode_mapping_required"
		if row.MappingErrorCode != nil && *row.MappingErrorCode != "" {
			code = *row.MappingErrorCode
		}
		return mediaWorkflowError(code, fmt.Sprintf("selected video %s requires a completed episode mapping", row.RelativePath), false)
	}
	if _, err := domain.BuildEpisodeFileNames(domain.EpisodeNamingRequest{
		SeriesTitle:    row.SeriesTitle,
		Season:         int(*row.TargetSeasonNumber),
		Episode:        int(*row.TargetEpisodeNumber),
		EpisodeTitle:   *row.TargetEpisodeTitle,
		VideoExtension: profile.FileExtension,
	}); err != nil {
		return mediaWorkflowError("episode_naming_invalid", err.Error(), false)
	}
	return nil
}

func (workflow *MediaWorkflow) BeginTranscode(ctx context.Context, taskID uuid.UUID) (domain.TaskMediaCommand, error) {
	return workflow.beginTaskMedia(ctx, taskID, domain.MediaVideo)
}

func (workflow *MediaWorkflow) BeginSubtitle(ctx context.Context, taskID uuid.UUID) (domain.TaskMediaCommand, error) {
	return workflow.beginTaskMedia(ctx, taskID, domain.MediaSubtitle)
}

func (workflow *MediaWorkflow) beginTaskMedia(
	ctx context.Context,
	taskID uuid.UUID,
	kind domain.MediaKind,
) (domain.TaskMediaCommand, error) {
	command := domain.TaskMediaCommand{}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		task, err := scope.Queries.LockEpisodeTask(ctx, repository.UUIDToPG(taskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock episode task: %w", err)
		}
		if task.State != string(domain.TaskMediaQueued) && task.State != string(domain.TaskProcessing) && task.State != string(domain.TaskFailed) && task.State != string(domain.TaskFinalizing) && task.State != string(domain.TaskAwaitingReview) {
			return mediaWorkflowError("task_media_state_conflict", fmt.Sprintf("task media cannot start from state %q", task.State), false)
		}
		switch kind {
		case domain.MediaVideo:
			if task.VideoState != string(domain.VideoReady) {
				if task.VideoState == string(domain.VideoFailed) || task.VideoState == string(domain.VideoCancelled) {
					return mediaWorkflowError("video_state_conflict", fmt.Sprintf("video branch cannot start from state %q", task.VideoState), false)
				}
				if _, err := scope.Queries.StartTaskVideo(ctx, repository.UUIDToPG(taskID)); errors.Is(err, pgx.ErrNoRows) {
					return mediaWorkflowError("video_state_conflict", fmt.Sprintf("video branch cannot start from task state %q", task.State), false)
				} else if err != nil {
					return fmt.Errorf("start task video branch: %w", err)
				}
			}
		case domain.MediaSubtitle:
			if task.SubtitleState != string(domain.SubtitleASSReady) {
				if task.SubtitleState == string(domain.SubtitleFailed) || task.SubtitleState == string(domain.SubtitleCancelled) {
					return mediaWorkflowError("subtitle_state_conflict", fmt.Sprintf("subtitle branch cannot start from state %q", task.SubtitleState), false)
				}
				if _, err := scope.Queries.StartTaskSubtitle(ctx, repository.UUIDToPG(taskID)); errors.Is(err, pgx.ErrNoRows) {
					return mediaWorkflowError("subtitle_state_conflict", fmt.Sprintf("subtitle branch cannot start from task state %q", task.State), false)
				} else if err != nil {
					return fmt.Errorf("start task subtitle branch: %w", err)
				}
			}
		default:
			return fmt.Errorf("unsupported task media kind %q", kind)
		}

		row, err := scope.Queries.GetTaskMediaCommand(ctx, repository.UUIDToPG(taskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return mediaWorkflowError("task_mapping_unavailable", "the task mapping or transcode profile is unavailable", false)
		}
		if err != nil {
			return fmt.Errorf("load task media command: %w", err)
		}
		externalRows, err := scope.Queries.ListTaskExternalSubtitles(ctx, repository.UUIDToPG(taskID))
		if err != nil {
			return fmt.Errorf("list task external subtitles: %w", err)
		}
		command, err = taskMediaCommandFromDB(row, externalRows)
		return err
	})
	if err != nil {
		return domain.TaskMediaCommand{}, err
	}
	return command, nil
}

func taskMediaCommandFromDB(
	row db.GetTaskMediaCommandRow,
	externalRows []db.ListTaskExternalSubtitlesRow,
) (domain.TaskMediaCommand, error) {
	profile, err := transcodeProfileFromMediaRow(row)
	if err != nil {
		return domain.TaskMediaCommand{}, mediaWorkflowError("transcode_profile_invalid", err.Error(), false)
	}
	var names domain.EpisodeFileNames
	var outputPaths domain.LibraryRelativePaths
	switch domain.TaskMediaType(row.MediaType) {
	case domain.TaskMediaMovie:
		if row.ReleaseYear == nil || row.TmdbMovieID == nil {
			return domain.TaskMediaCommand{}, mediaWorkflowError("movie_metadata_required", "the movie task has incomplete TMDb metadata", false)
		}
		names, err = domain.BuildMovieFileNames(domain.MovieNamingRequest{
			MovieTitle: row.SeriesTitle, ReleaseYear: int(*row.ReleaseYear), VideoExtension: row.FileExtension,
		})
		if err != nil {
			return domain.TaskMediaCommand{}, mediaWorkflowError("movie_naming_invalid", err.Error(), false)
		}
		outputPaths, err = domain.BuildMovieLibraryRelativePaths(row.SeriesTitle, int(*row.ReleaseYear), names.VideoName, names.SubtitleName)
		if err != nil {
			return domain.TaskMediaCommand{}, mediaWorkflowError("movie_directory_invalid", err.Error(), false)
		}
	case domain.TaskMediaEpisode:
		if row.TargetSeasonNumber == nil || row.TargetEpisodeNumber == nil || row.TargetEpisodeTitle == nil {
			return domain.TaskMediaCommand{}, mediaWorkflowError("task_mapping_unavailable", "the episode task mapping is unavailable", false)
		}
		names, err = domain.BuildEpisodeFileNames(domain.EpisodeNamingRequest{
			SeriesTitle: row.SeriesTitle, Season: int(*row.TargetSeasonNumber), Episode: int(*row.TargetEpisodeNumber),
			EpisodeTitle: *row.TargetEpisodeTitle, VideoExtension: row.FileExtension,
		})
		if err != nil {
			return domain.TaskMediaCommand{}, mediaWorkflowError("episode_naming_invalid", err.Error(), false)
		}
		outputPaths, err = domain.BuildLibraryRelativePaths(row.SeriesTitle, int(*row.TargetSeasonNumber), names.VideoName, names.SubtitleName)
		if err != nil {
			return domain.TaskMediaCommand{}, mediaWorkflowError("episode_directory_invalid", err.Error(), false)
		}
	default:
		return domain.TaskMediaCommand{}, mediaWorkflowError("media_type_invalid", "the task has an unsupported media type", false)
	}
	if row.SavePath == nil || strings.TrimSpace(*row.SavePath) == "" {
		return domain.TaskMediaCommand{}, mediaWorkflowError("download_save_path_missing", "the task download has no save path", false)
	}
	command := domain.TaskMediaCommand{
		TaskID:                  repository.UUIDFromPG(row.TaskID),
		MediaType:               domain.TaskMediaType(row.MediaType),
		State:                   domain.TaskState(row.State),
		VideoState:              domain.VideoState(row.VideoState),
		SubtitleState:           domain.SubtitleState(row.SubtitleState),
		DownloadID:              repository.UUIDFromPG(row.DownloadID),
		SavePath:                *row.SavePath,
		SourceVideoFileID:       repository.UUIDFromPG(row.SourceVideoFileID),
		SourceVideoRelativePath: row.SourceVideoRelativePath,
		TranscodeProfileID:      repository.UUIDFromPG(row.TranscodeProfileID),
		TranscodeProfile:        profile,
		Names:                   names,
		OutputRelativeDirectory: outputPaths.Directory,
		ExternalSubtitles:       make([]domain.TaskExternalSubtitle, 0, len(externalRows)),
	}
	for _, subtitle := range externalRows {
		format := domain.SubtitleFormatFromPath(subtitle.RelativePath)
		if format == "" {
			continue
		}
		command.ExternalSubtitles = append(command.ExternalSubtitles, domain.TaskExternalSubtitle{
			SourceFileID: repository.UUIDFromPG(subtitle.ID),
			RelativePath: subtitle.RelativePath,
			Language:     stringValue(subtitle.Language),
			Format:       format,
		})
	}
	return command, nil
}

func (workflow *MediaWorkflow) CompleteArtifact(ctx context.Context, completion domain.MediaArtifactCompletion) error {
	if err := domain.ValidateArtifactCompletion(completion); err != nil {
		return err
	}
	metadata := completion.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode media artifact metadata: %w", err)
	}
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		task, err := scope.Queries.LockEpisodeTask(ctx, repository.UUIDToPG(completion.TaskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock artifact task: %w", err)
		}
		ready := (completion.Kind == domain.MediaVideo && task.VideoState == string(domain.VideoReady)) ||
			(completion.Kind == domain.MediaSubtitle && task.SubtitleState == string(domain.SubtitleASSReady))
		if ready {
			existing, err := scope.Queries.GetTaskArtifact(ctx, db.GetTaskArtifactParams{TaskID: repository.UUIDToPG(completion.TaskID), Kind: string(completion.Kind)})
			if err == nil && string(existing.ChecksumSha256) == string(completion.ChecksumSHA256) && existing.FilePath == completion.FilePath {
				return nil
			}
			return mediaWorkflowError("artifact_conflict", "the ready task branch already references a different artifact", false)
		}

		artifact, err := scope.Queries.UpsertMediaArtifact(ctx, db.UpsertMediaArtifactParams{
			ID:                 repository.UUIDToPG(deterministicResourceID("artifact:" + completion.TaskID.String() + ":" + string(completion.Kind))),
			TaskID:             repository.UUIDToPG(completion.TaskID),
			SourceFileID:       repository.UUIDToPG(completion.SourceFileID),
			TranscodeProfileID: nullableUUID(completion.TranscodeProfileID),
			Kind:               string(completion.Kind),
			Basename:           completion.BaseName,
			FilePath:           completion.FilePath,
			Format:             completion.Format,
			SizeBytes:          completion.SizeBytes,
			ChecksumSha256:     completion.ChecksumSHA256,
			Metadata:           metadataJSON,
		})
		if err != nil {
			return fmt.Errorf("persist media artifact: %w", err)
		}
		switch completion.Kind {
		case domain.MediaVideo:
			if _, err := scope.Queries.MarkTaskVideoReady(ctx, repository.UUIDToPG(completion.TaskID)); err != nil {
				return fmt.Errorf("mark task video ready: %w", err)
			}
		case domain.MediaSubtitle:
			if _, err := scope.Queries.MarkTaskSubtitleReady(ctx, repository.UUIDToPG(completion.TaskID)); err != nil {
				return fmt.Errorf("mark task subtitle ready: %w", err)
			}
		}
		if err := appendResourceEvent(ctx, scope.Queries, "episode_task", completion.TaskID, completion.OperationID, uuid.Nil, "task."+string(completion.Kind)+"_ready", map[string]any{
			"artifactId": repository.UUIDFromPG(artifact.ID),
			"checksum":   fmt.Sprintf("%x", completion.ChecksumSHA256),
		}); err != nil {
			return err
		}

		finalizing, err := scope.Queries.MarkTaskFinalizingIfReady(ctx, repository.UUIDToPG(completion.TaskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("mark task finalizing: %w", err)
		}
		if _, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindMediaFinalize,
			ResourceType:   "episode_task",
			ResourceID:     completion.TaskID,
			IdempotencyKey: "media.finalize:" + completion.TaskID.String(),
			MaxAttempts:    3,
			Timeout:        5 * time.Minute,
			Payload:        map[string]any{},
		}); err != nil {
			return fmt.Errorf("schedule media finalization: %w", err)
		}
		return appendResourceEvent(ctx, scope.Queries, "episode_task", completion.TaskID, completion.OperationID, uuid.Nil, "task.finalizing", map[string]any{
			"version": finalizing.Version,
		})
	})
}

func (workflow *MediaWorkflow) LoadFinalizeCommand(ctx context.Context, taskID uuid.UUID) (domain.FinalizeMediaCommand, error) {
	row, err := workflow.queries.GetTaskFinalizeCommand(ctx, repository.UUIDToPG(taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FinalizeMediaCommand{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.FinalizeMediaCommand{}, fmt.Errorf("load media finalization command: %w", err)
	}
	return finalizeCommandFromDB(row), nil
}

func (workflow *MediaWorkflow) CompleteFinalize(ctx context.Context, taskID uuid.UUID, operationID uuid.UUID) error {
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		task, err := scope.Queries.LockEpisodeTask(ctx, repository.UUIDToPG(taskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock task finalization: %w", err)
		}
		switch domain.TaskState(task.State) {
		case domain.TaskAwaitingReview, domain.TaskImportQueued, domain.TaskImporting, domain.TaskImported:
			return nil
		}
		if task.State != string(domain.TaskFinalizing) || task.VideoState != string(domain.VideoReady) || task.SubtitleState != string(domain.SubtitleASSReady) {
			return mediaWorkflowError("task_finalize_state_conflict", fmt.Sprintf("task cannot finalize from state %q", task.State), false)
		}
		row, err := scope.Queries.GetTaskFinalizeCommand(ctx, repository.UUIDToPG(taskID))
		if err != nil {
			return fmt.Errorf("load task artifacts for finalization: %w", err)
		}
		if row.VideoBasename != row.SubtitleBasename {
			return mediaWorkflowError("artifact_basename_mismatch", "video and subtitle artifacts do not share a basename", false)
		}
		if _, err := scope.Queries.CreateArtifactSet(ctx, db.CreateArtifactSetParams{
			ID:                 repository.UUIDToPG(deterministicResourceID("artifact-set:" + taskID.String())),
			TaskID:             repository.UUIDToPG(taskID),
			TranscodeProfileID: row.TranscodeProfileID,
			Basename:           row.VideoBasename,
			VideoArtifactID:    row.VideoArtifactID,
			SubtitleArtifactID: row.SubtitleArtifactID,
		}); err != nil {
			return fmt.Errorf("create artifact set: %w", err)
		}
		awaitingReview, err := scope.Queries.MarkTaskAwaitingReview(ctx, repository.UUIDToPG(taskID))
		if err != nil {
			return fmt.Errorf("mark task awaiting review: %w", err)
		}
		if err := appendResourceEvent(ctx, scope.Queries, "episode_task", taskID, operationID, uuid.Nil, "task.awaiting_review", map[string]any{
			"status":  domain.TaskAwaitingReview,
			"version": awaitingReview.Version,
		}); err != nil {
			return err
		}

		autoReview, err := scope.Queries.GetTaskRSSAutoReview(ctx, repository.UUIDToPG(taskID))
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !autoReview.AutoReview) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load task RSS auto-review policy: %w", err)
		}
		return approveRSSReviewInTx(ctx, scope, workflow.operations, taskID, operationID, repository.UUIDFromPG(autoReview.SubscriptionID), awaitingReview.Version)
	})
}

func approveRSSReviewInTx(
	ctx context.Context,
	scope database.TxScope,
	operations *OperationScheduler,
	taskID uuid.UUID,
	operationID uuid.UUID,
	subscriptionID uuid.UUID,
	expectedVersion int32,
) error {
	if operations == nil {
		return fmt.Errorf("RSS automatic review operation scheduler is unavailable")
	}
	reviewID := deterministicResourceID("rss-auto-review:" + taskID.String())
	if _, err := scope.Queries.CreateTaskReview(ctx, db.CreateTaskReviewParams{
		ID:                  repository.UUIDToPG(reviewID),
		TaskID:              repository.UUIDToPG(taskID),
		Decision:            string(domain.TaskApproved),
		Notes:               "Automatically approved by RSS subscription policy.",
		ReviewedBy:          pgtype.UUID{},
		IdempotencyKey:      "task.review:rss-auto:" + subscriptionID.String() + ":" + taskID.String(),
		ExpectedTaskVersion: expectedVersion,
	}); err != nil {
		return fmt.Errorf("create RSS automatic task review: %w", err)
	}
	reviewed, err := scope.Queries.MarkTaskReviewed(ctx, db.MarkTaskReviewedParams{
		Decision:        string(domain.TaskApproved),
		ID:              repository.UUIDToPG(taskID),
		ExpectedVersion: expectedVersion,
	})
	if err != nil {
		return fmt.Errorf("mark RSS task automatically reviewed: %w", err)
	}
	if err := appendResourceEvent(ctx, scope.Queries, "episode_task", taskID, operationID, uuid.Nil, "task.reviewed", map[string]any{
		"automatic":         true,
		"decision":          domain.TaskApproved,
		"reviewId":          reviewID,
		"rssSubscriptionId": subscriptionID,
		"version":           reviewed.Version,
	}); err != nil {
		return err
	}

	importID := deterministicResourceID("rss-auto-import:" + taskID.String())
	if _, err := scope.Queries.CreateTaskImport(ctx, db.CreateTaskImportParams{
		ID:     repository.UUIDToPG(importID),
		TaskID: repository.UUIDToPG(taskID),
	}); err != nil {
		return fmt.Errorf("create RSS automatic task import: %w", err)
	}
	queued, err := scope.Queries.MarkTaskImportQueued(ctx, db.MarkTaskImportQueuedParams{
		ID:              repository.UUIDToPG(taskID),
		ExpectedVersion: reviewed.Version,
	})
	if err != nil {
		return fmt.Errorf("queue RSS automatically reviewed task: %w", err)
	}
	scheduled, err := operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
		Kind:           appqueue.KindEmbyImport,
		ResourceType:   "episode_task",
		ResourceID:     taskID,
		IdempotencyKey: "emby.import:rss-auto:" + taskID.String(),
		MaxAttempts:    3,
		Timeout:        30 * time.Minute,
		Payload: map[string]any{
			"importId":        importID,
			"expectedVersion": reviewed.Version,
		},
	})
	if err != nil {
		return fmt.Errorf("schedule RSS automatic task import: %w", err)
	}
	return appendResourceEvent(ctx, scope.Queries, "episode_task", taskID, scheduled.Operation.ID, uuid.Nil, "task.import_queued", map[string]any{
		"automatic":         true,
		"importId":          importID,
		"reviewId":          reviewID,
		"rssSubscriptionId": subscriptionID,
		"version":           queued.Version,
	})
}

func transcodeProfileFromDB(row db.TranscodeProfile) (domain.TranscodeProfile, error) {
	value, err := row.QualityValue.Float64Value()
	if err != nil || !value.Valid {
		return domain.TranscodeProfile{}, fmt.Errorf("decode transcode profile quality value")
	}
	profile := domain.TranscodeProfile{
		Name:           row.Name,
		VideoCodec:     row.VideoCodec,
		Encoder:        row.Encoder,
		Container:      row.Container,
		FileExtension:  row.FileExtension,
		QualityMode:    row.QualityMode,
		QualityValue:   value.Float64,
		AudioPolicy:    row.AudioPolicy,
		AudioCodec:     stringValue(row.AudioCodec),
		Preset:         row.Preset,
		PixelFormat:    row.PixelFormat,
		ThreadCount:    int(row.ThreadCount),
		MaxConcurrency: int(row.MaxConcurrency),
	}
	if err := domain.ValidateTranscodeProfile(profile); err != nil {
		return domain.TranscodeProfile{}, err
	}
	return profile, nil
}

func transcodeProfileFromMediaRow(row db.GetTaskMediaCommandRow) (domain.TranscodeProfile, error) {
	value, err := row.QualityValue.Float64Value()
	if err != nil || !value.Valid {
		return domain.TranscodeProfile{}, fmt.Errorf("decode transcode profile quality value")
	}
	profile := domain.TranscodeProfile{
		Name:           row.ProfileName,
		VideoCodec:     row.VideoCodec,
		Encoder:        row.Encoder,
		Container:      row.Container,
		FileExtension:  row.FileExtension,
		QualityMode:    row.QualityMode,
		QualityValue:   value.Float64,
		AudioPolicy:    row.AudioPolicy,
		AudioCodec:     stringValue(row.AudioCodec),
		Preset:         row.Preset,
		PixelFormat:    row.PixelFormat,
		ThreadCount:    int(row.ThreadCount),
		MaxConcurrency: int(row.MaxConcurrency),
	}
	if err := domain.ValidateTranscodeProfile(profile); err != nil {
		return domain.TranscodeProfile{}, err
	}
	return profile, nil
}

func finalizeCommandFromDB(row db.GetTaskFinalizeCommandRow) domain.FinalizeMediaCommand {
	return domain.FinalizeMediaCommand{
		TaskID:             repository.UUIDFromPG(row.TaskID),
		State:              domain.TaskState(row.State),
		TranscodeProfileID: repository.UUIDFromPG(row.TranscodeProfileID),
		Video: domain.MediaArtifact{
			ID:                 repository.UUIDFromPG(row.VideoArtifactID),
			TaskID:             repository.UUIDFromPG(row.TaskID),
			TranscodeProfileID: repository.UUIDFromPG(row.TranscodeProfileID),
			Kind:               domain.MediaVideo,
			BaseName:           row.VideoBasename,
			FilePath:           row.VideoFilePath,
			SizeBytes:          row.VideoSizeBytes,
			ChecksumSHA256:     row.VideoChecksumSha256,
		},
		Subtitle: domain.MediaArtifact{
			ID:             repository.UUIDFromPG(row.SubtitleArtifactID),
			TaskID:         repository.UUIDFromPG(row.TaskID),
			Kind:           domain.MediaSubtitle,
			BaseName:       row.SubtitleBasename,
			FilePath:       row.SubtitleFilePath,
			SizeBytes:      row.SubtitleSizeBytes,
			ChecksumSHA256: row.SubtitleChecksumSha256,
		},
	}
}

func mediaWorkflowError(code, message string, retryable bool) *domain.MediaWorkflowError {
	return &domain.MediaWorkflowError{Code: code, Message: message, Retryable: retryable}
}

// CreateSubtitleVideoMatchScope persists a bounded set of subtitle candidates
// for a task so the Agent can later select which candidate matches the video.
// It is idempotent per task: an existing pending scope is reused.
func (workflow *MediaWorkflow) CreateSubtitleVideoMatchScope(
	ctx context.Context,
	taskID uuid.UUID,
	candidates []domain.SubtitleMatchCandidate,
) (uuid.UUID, error) {
	if taskID == uuid.Nil || len(candidates) < 2 {
		return uuid.Nil, mediaWorkflowError("subtitle_scope_invalid", "subtitle video match requires a task and at least two candidates", false)
	}
	seenCandidateIDs := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidateID := strings.TrimSpace(candidate.CandidateID)
		if candidateID == "" {
			return uuid.Nil, mediaWorkflowError("subtitle_scope_invalid", "subtitle candidates require stable identities", false)
		}
		if _, duplicate := seenCandidateIDs[candidateID]; duplicate {
			return uuid.Nil, mediaWorkflowError("subtitle_scope_invalid", "subtitle candidate identities must be unique", false)
		}
		seenCandidateIDs[candidateID] = struct{}{}
	}
	scopeID := uuid.New()
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		created, err := scope.Queries.CreateSubtitleVideoMatchScope(ctx, db.CreateSubtitleVideoMatchScopeParams{
			ID: repository.UUIDToPG(scopeID), TaskID: repository.UUIDToPG(taskID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// An existing scope under the same task is reused; the caller should
			// treat this as already-scheduled rather than creating a duplicate.
			return mediaWorkflowError("subtitle_scope_conflict", "a subtitle video match scope already exists for this task", false)
		}
		if err != nil {
			return mediaWorkflowError("subtitle_scope_unavailable", "the subtitle video match scope could not be created", true)
		}
		scopeID = repository.UUIDFromPG(created.ID)
		for _, candidate := range candidates {
			var streamIndex *int32
			if candidate.Source == domain.SubtitleSourceEmbedded {
				value := int32(candidate.StreamIndex)
				streamIndex = &value
			}
			if err := scope.Queries.InsertSubtitleVideoMatchCandidate(ctx, db.InsertSubtitleVideoMatchCandidateParams{
				ScopeID: repository.UUIDToPG(scopeID), CandidateID: candidate.CandidateID, Source: string(candidate.Source),
				StreamIndex: streamIndex, Format: optionalString(string(candidate.Format)), Language: optionalString(candidate.Language),
				Title: optionalString(candidate.Title), Path: optionalString(candidate.Path),
			}); err != nil {
				return mediaWorkflowError("subtitle_scope_unavailable", "a subtitle candidate could not be persisted", true)
			}
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return scopeID, nil
}

// GetSubtitleVideoMatchSelection returns the Agent-selected candidate for a
// task, or an empty selection when no selection has been applied.
func (workflow *MediaWorkflow) GetSubtitleVideoMatchSelection(ctx context.Context, taskID uuid.UUID) (domain.SubtitleMatchSelection, error) {
	row, err := workflow.queries.GetSubtitleVideoMatchSelection(ctx, repository.UUIDToPG(taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SubtitleMatchSelection{}, nil
	}
	if err != nil {
		return domain.SubtitleMatchSelection{}, mediaWorkflowError("subtitle_scope_unavailable", "the subtitle video match selection could not be loaded", true)
	}
	selection := domain.SubtitleMatchSelection{Path: stringValue(row.SelectedCandidatePath)}
	if row.SelectedCandidateID != nil {
		selection.CandidateID = *row.SelectedCandidateID
	}
	return selection, nil
}
