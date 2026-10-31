package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type acquisitionReadData struct {
	media     map[uuid.UUID]db.MediaSeries
	downloads map[uuid.UUID]db.Download
	tasks     map[uuid.UUID][]db.ListAcquisitionTaskSummariesByAcquisitionIDsRow
	mapping   map[uuid.UUID]db.GetAcquisitionMappingCompletenessByAcquisitionIDsRow
}

func loadAcquisitionReadData(ctx context.Context, queries *db.Queries, rows []db.Acquisition) (acquisitionReadData, error) {
	if len(rows) == 0 {
		return acquisitionReadData{}, nil
	}
	pgAcquisitionIDs := make([]pgtype.UUID, 0, len(rows))
	pgSeriesIDs := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		pgAcquisitionIDs = append(pgAcquisitionIDs, row.ID)
		pgSeriesIDs = append(pgSeriesIDs, row.SeriesID)
	}

	mediaRows, err := queries.ListMediaSeriesByIDs(ctx, pgSeriesIDs)
	if err != nil {
		return acquisitionReadData{}, fmt.Errorf("list media series: %w", err)
	}
	mediaByID := make(map[uuid.UUID]db.MediaSeries, len(mediaRows))
	for _, media := range mediaRows {
		mediaByID[repository.UUIDFromPG(media.ID)] = media
	}

	downloadRows, err := queries.ListLatestDownloadsByAcquisitionIDs(ctx, pgAcquisitionIDs)
	if err != nil {
		return acquisitionReadData{}, fmt.Errorf("list acquisition downloads: %w", err)
	}
	downloadByAcquisition := make(map[uuid.UUID]db.Download, len(downloadRows))
	for _, download := range downloadRows {
		downloadByAcquisition[repository.UUIDFromPG(download.AcquisitionID)] = download
	}

	taskRows, err := queries.ListAcquisitionTaskSummariesByAcquisitionIDs(ctx, pgAcquisitionIDs)
	if err != nil {
		return acquisitionReadData{}, fmt.Errorf("list acquisition tasks: %w", err)
	}
	tasksByAcquisition := make(map[uuid.UUID][]db.ListAcquisitionTaskSummariesByAcquisitionIDsRow)
	for _, task := range taskRows {
		acquisitionID := repository.UUIDFromPG(task.AcquisitionID)
		tasksByAcquisition[acquisitionID] = append(tasksByAcquisition[acquisitionID], task)
	}

	mappingRows, err := queries.GetAcquisitionMappingCompletenessByAcquisitionIDs(ctx, pgAcquisitionIDs)
	if err != nil {
		return acquisitionReadData{}, fmt.Errorf("get acquisition mapping completeness: %w", err)
	}
	if len(mappingRows) != len(rows) {
		// 逐项查询的 :one 语义在缺少媒体系列或映射数据时直接失败，
		// 批量路径保持同样的严格行为而不是静默降级为未完成。
		return acquisitionReadData{}, fmt.Errorf("get acquisition mapping completeness: %d of %d acquisitions resolved", len(mappingRows), len(rows))
	}
	mappingByAcquisition := make(map[uuid.UUID]db.GetAcquisitionMappingCompletenessByAcquisitionIDsRow, len(mappingRows))
	for _, mapping := range mappingRows {
		mappingByAcquisition[repository.UUIDFromPG(mapping.ID)] = mapping
	}

	return acquisitionReadData{media: mediaByID, downloads: downloadByAcquisition, tasks: tasksByAcquisition, mapping: mappingByAcquisition}, nil
}

func acquisitionViewFromDB(
	row db.Acquisition,
	media db.MediaSeries,
	download db.Download,
	tasks []db.ListAcquisitionTaskSummariesByAcquisitionIDsRow,
	mapping db.GetAcquisitionMappingCompletenessByAcquisitionIDsRow,
) domain.AcquisitionView {
	view := domain.AcquisitionView{
		ID:         repository.UUIDFromPG(row.ID),
		SeriesID:   repository.UUIDFromPG(row.SeriesID),
		SourceKind: row.SourceKind,
		CreatedAt:  row.CreatedAt.Time,
		UpdatedAt:  row.UpdatedAt.Time,
	}
	if row.MappingProfileID.Valid {
		value := repository.UUIDFromPG(row.MappingProfileID)
		view.MappingProfileID = &value
	}
	if row.ReleaseCandidateID.Valid {
		value := repository.UUIDFromPG(row.ReleaseCandidateID)
		view.ReleaseCandidateID = &value
	}
	if row.RssEntryID.Valid {
		value := repository.UUIDFromPG(row.RssEntryID)
		view.RSSEntryID = &value
	}
	var payload struct {
		SourceSeason                    *int  `json:"sourceSeason"`
		SourceEpisode                   *int  `json:"sourceEpisode"`
		SourceEpisodeFractionHundredths int   `json:"sourceEpisodeFractionHundredths"`
		SingleEpisode                   *bool `json:"singleEpisode"`
	}
	if err := json.Unmarshal(row.SourcePayload, &payload); err == nil {
		view.SourceSeason, view.SourceEpisode = payload.SourceSeason, payload.SourceEpisode
		view.SourceEpisodeFractionHundredths, view.SingleEpisode = payload.SourceEpisodeFractionHundredths, payload.SingleEpisode
	}

	if media.ID.Valid {
		switch media.MediaType {
		case "movie":
			view.MediaType = domain.TaskMediaMovie
			view.MovieTitle = media.Title
			view.TMDbMovieID = int64ValuePointer(media.TmdbMovieID)
			view.ReleaseYear = intValue(media.ReleaseYear)
		default:
			view.MediaType = domain.TaskMediaEpisode
			view.SeriesTitle = media.Title
			if media.TmdbSeriesID != nil {
				view.TMDbSeriesID = *media.TmdbSeriesID
			}
		}
	}

	var downloadStatus string
	if download.ID.Valid {
		downloadID := repository.UUIDFromPG(download.ID)
		view.DownloadID = &downloadID
		downloadStatus = download.Status
		view.Download = &domain.AcquisitionDownloadSummary{
			ID: downloadID, Attempt: int(download.Attempt), Status: download.Status, Progress: numericToFloat(download.Progress),
			FailureStage: valueOrEmpty(download.FailureStage), ClientState: valueOrEmpty(download.ClientState),
			ErrorCode: valueOrEmpty(download.ErrorCode), ErrorMessage: valueOrEmpty(download.ErrorMessage), UpdatedAt: download.UpdatedAt.Time,
		}
		view.UpdatedAt = laterTime(view.UpdatedAt, download.UpdatedAt.Time)
	}

	view.Tasks = make([]domain.AcquisitionTaskSummary, 0, len(tasks))
	for _, task := range tasks {
		summary := domain.AcquisitionTaskSummary{
			ID: repository.UUIDFromPG(task.ID), MediaType: domain.TaskMediaType(task.MediaType), DownloadID: repository.UUIDFromPG(task.DownloadID),
			State: task.State, VideoState: task.VideoState, SubtitleState: task.SubtitleState,
			ArtifactBasename: valueOrEmpty(task.ArtifactBasename), ReviewDecision: valueOrEmpty(task.ReviewDecision),
			ImportStatus: task.ImportStatus, DestinationVideoPath: valueOrEmpty(task.DestinationVideoPath),
			DestinationSubtitlePath: valueOrEmpty(task.DestinationSubtitlePath), EmbyRefreshStatus: task.EmbyRefreshStatus,
			CleanupStatus: task.CleanupStatus, FailureStage: valueOrEmpty(task.FailureStage), ErrorCode: valueOrEmpty(task.ErrorCode),
			ErrorMessage: valueOrEmpty(task.ErrorMessage), TargetEpisodeTitle: valueOrEmpty(task.TargetEpisodeTitle),
			UpdatedAt: task.UpdatedAt.Time,
			CanRetry:  isTaskRetryable(task.State, task.VideoState, task.SubtitleState, valueOrEmpty(task.FailureStage), task.CleanupStatus),
		}
		if task.SourceSeason != nil {
			summary.SourceSeason = int(*task.SourceSeason)
		}
		if task.SourceEpisode != nil {
			summary.SourceEpisode = int(*task.SourceEpisode)
		}
		summary.SourceEpisodeFractionHundredths = int(task.SourceEpisodeFractionHundredths)
		if task.TargetSeason != nil {
			value := int(*task.TargetSeason)
			summary.TargetSeason = &value
		}
		if task.TargetEpisode != nil {
			value := int(*task.TargetEpisode)
			summary.TargetEpisode = &value
		}
		if task.ReviewedAt.Valid {
			value := task.ReviewedAt.Time
			summary.ReviewedAt = &value
		}
		view.UpdatedAt = laterTime(view.UpdatedAt, summary.UpdatedAt)
		view.Tasks = append(view.Tasks, summary)
	}

	view.Mapping = domain.AcquisitionMappingCompleteness{
		SelectedVideoCount: saturatingInt(mapping.SelectedVideoCount),
		MappedVideoCount:   saturatingInt(mapping.MappedVideoCount),
		Complete:           mapping.SelectedVideoCount > 0 && mapping.SelectedVideoCount == mapping.MappedVideoCount,
	}
	deriveAcquisitionLifecycle(&view, downloadStatus)
	return view
}

func subscriptionAcquisition(row db.ListRSSSubscriptionAcquisitionsBySubscriptionIDsRow) db.Acquisition {
	return db.Acquisition{ID: row.ID, SeriesID: row.SeriesID, MappingProfileID: row.MappingProfileID, SourceKind: row.SourceKind, ReleaseCandidateID: row.ReleaseCandidateID, RssEntryID: row.RssEntryID, SourceUri: row.SourceUri, SourcePayload: row.SourcePayload, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, DeletionRequestedAt: row.DeletionRequestedAt}
}

// acquisitionViewsWithQueries 按批次加载关联数据，详情与列表投影共用生命周期组装。
func acquisitionViewsWithQueries(ctx context.Context, queries *db.Queries, rows []db.Acquisition, includeDetails bool) ([]domain.AcquisitionView, error) {
	data, err := loadAcquisitionReadData(ctx, queries, rows)
	if err != nil {
		return nil, err
	}
	titles := make(map[uuid.UUID]string)
	audits := make(map[uuid.UUID]db.ListMappingProfileAuditsByIDsRow)
	if includeDetails && len(rows) > 0 {
		ids := make([]pgtype.UUID, len(rows))
		profiles := make([]pgtype.UUID, 0, len(rows))
		for index, row := range rows {
			ids[index] = row.ID
			if row.MappingProfileID.Valid {
				profiles = append(profiles, row.MappingProfileID)
			}
		}
		sourceRows, err := queries.ListAcquisitionSourceTitles(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("load acquisition source titles: %w", err)
		}
		for _, row := range sourceRows {
			titles[repository.UUIDFromPG(row.AcquisitionID)] = strings.TrimSpace(row.SourceTitle)
		}
		if len(profiles) > 0 {
			auditRows, err := queries.ListMappingProfileAuditsByIDs(ctx, profiles)
			if err != nil {
				return nil, fmt.Errorf("load mapping profile audits: %w", err)
			}
			for _, row := range auditRows {
				audits[repository.UUIDFromPG(row.ID)] = row
			}
		}
	}
	views := make([]domain.AcquisitionView, 0, len(rows))
	for _, row := range rows {
		id := repository.UUIDFromPG(row.ID)
		view := acquisitionViewFromDB(row, data.media[repository.UUIDFromPG(row.SeriesID)], data.downloads[id], data.tasks[id], data.mapping[id])
		view.SourceTitle = titles[id]
		if audit, ok := audits[repository.UUIDFromPG(row.MappingProfileID)]; row.MappingProfileID.Valid && ok {
			view.MappingDecisionSource = audit.DecisionSource
			if audit.AgentResolutionID.Valid {
				value := repository.UUIDFromPG(audit.AgentResolutionID)
				view.MappingAgentResolutionID = &value
			}
		}
		views = append(views, view)
	}
	return views, nil
}
