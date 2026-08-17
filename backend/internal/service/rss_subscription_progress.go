package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

// subscriptionProgressBySubscriptionsWithQueries 批量计算多个 RSS 订阅的进度统计。
// 持久化 read model 的唯一重算入口使用固定数量的批量查询（acquisitions、
// imported counts、媒体系列、下载、任务与映射完整性各一条），分组后复用
// `deriveAcquisitionLifecycle` 与订阅汇总规则，避免 SQL 维护第二套 lifecycle。
func subscriptionProgressBySubscriptionsWithQueries(
	ctx context.Context,
	queries *db.Queries,
	subscriptionIDs []uuid.UUID,
) (map[uuid.UUID]rssSubscriptionProgress, error) {
	progress := make(map[uuid.UUID]rssSubscriptionProgress, len(subscriptionIDs))
	if len(subscriptionIDs) == 0 {
		return progress, nil
	}
	pgSubscriptionIDs := make([]pgtype.UUID, len(subscriptionIDs))
	for index, id := range subscriptionIDs {
		pgSubscriptionIDs[index] = repository.UUIDToPG(id)
	}

	acquisitionRows, err := queries.ListRSSSubscriptionAcquisitionsBySubscriptionIDs(ctx, pgSubscriptionIDs)
	if err != nil {
		return nil, fmt.Errorf("list subscription acquisitions: %w", err)
	}
	viewsBySubscription, err := subscriptionProgressViews(ctx, queries, acquisitionRows)
	if err != nil {
		return nil, err
	}

	importedRows, err := queries.ListRSSSubscriptionImportedCountsBySubscriptionIDs(ctx, pgSubscriptionIDs)
	if err != nil {
		return nil, fmt.Errorf("load RSS subscription imported counts: %w", err)
	}
	importedBySubscription := make(map[uuid.UUID]int64, len(importedRows))
	for _, row := range importedRows {
		importedBySubscription[repository.UUIDFromPG(row.ID)] = row.ImportedCount
	}

	for _, subscriptionID := range subscriptionIDs {
		progress[subscriptionID] = summarizeRSSSubscriptionImportedProgress(
			int(importedBySubscription[subscriptionID]),
			viewsBySubscription[subscriptionID],
		)
	}
	return progress, nil
}

// subscriptionProgressViews 批量组装订阅进度汇总所需的 acquisition 视图，
// 按订阅 ID 分组返回。与单订阅路径（acquisitionViews）相比只填充进度计算
// （summarize*、deriveAcquisitionLifecycle、dashboardAttentionItem）实际使用
// 的字段，不加载 MappingDecisionSource 等视图详情字段。
func subscriptionProgressViews(
	ctx context.Context,
	queries *db.Queries,
	rows []db.ListRSSSubscriptionAcquisitionsBySubscriptionIDsRow,
) (map[uuid.UUID][]domain.AcquisitionView, error) {
	if len(rows) == 0 {
		return map[uuid.UUID][]domain.AcquisitionView{}, nil
	}
	pgAcquisitionIDs := make([]pgtype.UUID, 0, len(rows))
	pgSeriesIDs := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		pgAcquisitionIDs = append(pgAcquisitionIDs, row.ID)
		pgSeriesIDs = append(pgSeriesIDs, row.SeriesID)
	}

	mediaRows, err := queries.ListMediaSeriesByIDs(ctx, pgSeriesIDs)
	if err != nil {
		return nil, fmt.Errorf("list media series: %w", err)
	}
	mediaByID := make(map[uuid.UUID]db.MediaSeries, len(mediaRows))
	for _, media := range mediaRows {
		mediaByID[repository.UUIDFromPG(media.ID)] = media
	}

	downloadRows, err := queries.ListLatestDownloadsByAcquisitionIDs(ctx, pgAcquisitionIDs)
	if err != nil {
		return nil, fmt.Errorf("list acquisition downloads: %w", err)
	}
	downloadByAcquisition := make(map[uuid.UUID]db.Download, len(downloadRows))
	for _, download := range downloadRows {
		downloadByAcquisition[repository.UUIDFromPG(download.AcquisitionID)] = download
	}

	taskRows, err := queries.ListAcquisitionTaskSummariesByAcquisitionIDs(ctx, pgAcquisitionIDs)
	if err != nil {
		return nil, fmt.Errorf("list acquisition tasks: %w", err)
	}
	tasksByAcquisition := make(map[uuid.UUID][]db.ListAcquisitionTaskSummariesByAcquisitionIDsRow)
	for _, task := range taskRows {
		acquisitionID := repository.UUIDFromPG(task.AcquisitionID)
		tasksByAcquisition[acquisitionID] = append(tasksByAcquisition[acquisitionID], task)
	}

	mappingRows, err := queries.GetAcquisitionMappingCompletenessByAcquisitionIDs(ctx, pgAcquisitionIDs)
	if err != nil {
		return nil, fmt.Errorf("get acquisition mapping completeness: %w", err)
	}
	if len(mappingRows) != len(rows) {
		// 逐项查询的 :one 语义在缺少媒体系列或映射数据时直接失败，
		// 批量路径保持同样的严格行为而不是静默降级为未完成。
		return nil, fmt.Errorf("get acquisition mapping completeness: %d of %d acquisitions resolved", len(mappingRows), len(rows))
	}
	mappingByAcquisition := make(map[uuid.UUID]db.GetAcquisitionMappingCompletenessByAcquisitionIDsRow, len(mappingRows))
	for _, mapping := range mappingRows {
		mappingByAcquisition[repository.UUIDFromPG(mapping.ID)] = mapping
	}
	return groupSubscriptionProgressViews(rows, mediaByID, downloadByAcquisition, tasksByAcquisition, mappingByAcquisition), nil
}

// groupSubscriptionProgressViews 把批量加载的行按订阅 ID 分组组装为进度视图。
func groupSubscriptionProgressViews(
	rows []db.ListRSSSubscriptionAcquisitionsBySubscriptionIDsRow,
	mediaByID map[uuid.UUID]db.MediaSeries,
	downloadByAcquisition map[uuid.UUID]db.Download,
	tasksByAcquisition map[uuid.UUID][]db.ListAcquisitionTaskSummariesByAcquisitionIDsRow,
	mappingByAcquisition map[uuid.UUID]db.GetAcquisitionMappingCompletenessByAcquisitionIDsRow,
) map[uuid.UUID][]domain.AcquisitionView {
	viewsBySubscription := make(map[uuid.UUID][]domain.AcquisitionView)
	for _, row := range rows {
		subscriptionID := repository.UUIDFromPG(row.SubscriptionID)
		view := acquisitionProgressView(
			row,
			mediaByID[repository.UUIDFromPG(row.SeriesID)],
			downloadByAcquisition[repository.UUIDFromPG(row.ID)],
			tasksByAcquisition[repository.UUIDFromPG(row.ID)],
			mappingByAcquisition[repository.UUIDFromPG(row.ID)],
		)
		viewsBySubscription[subscriptionID] = append(viewsBySubscription[subscriptionID], view)
	}
	return viewsBySubscription
}

// acquisitionProgressView 从批量加载的行组装单个订阅进度视图。
// 字段选择与单订阅路径（read_service.acquisitionViews）保持一致，
// 仅保留进度汇总使用的部分。
func acquisitionProgressView(
	row db.ListRSSSubscriptionAcquisitionsBySubscriptionIDsRow,
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
	if media.ID.Valid {
		switch media.MediaType {
		case "movie":
			view.MediaType = domain.TaskMediaMovie
		default:
			view.MediaType = domain.TaskMediaEpisode
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
		}
		if task.SourceSeason != nil {
			summary.SourceSeason = int(*task.SourceSeason)
		}
		if task.SourceEpisode != nil {
			summary.SourceEpisode = int(*task.SourceEpisode)
		}
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
