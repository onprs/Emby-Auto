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
func subscriptionProgressViews(ctx context.Context, queries *db.Queries, rows []db.ListRSSSubscriptionAcquisitionsBySubscriptionIDsRow) (map[uuid.UUID][]domain.AcquisitionView, error) {
	acquisitions := make([]db.Acquisition, len(rows))
	for index, row := range rows {
		acquisitions[index] = subscriptionAcquisition(row)
	}
	data, err := loadAcquisitionReadData(ctx, queries, acquisitions)
	if err != nil {
		return nil, err
	}
	return groupSubscriptionProgressViews(rows, data.media, data.downloads, data.tasks, data.mapping), nil
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
	return acquisitionViewFromDB(subscriptionAcquisition(row), media, download, tasks, mapping)
}
