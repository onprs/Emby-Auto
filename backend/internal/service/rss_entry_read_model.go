package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func rssEntryVersion(enqueueAttempts int32) int {
	if enqueueAttempts < 1 {
		return 1
	}
	return int(enqueueAttempts)
}

func rssEntryCanRetry(acquisition domain.AcquisitionView) bool {
	if acquisition.Download != nil && acquisition.Download.Status == "cancelled" && len(acquisition.Tasks) == 0 {
		return true
	}
	if acquisition.Download != nil &&
		acquisition.Download.Status == "failed" && acquisition.Download.ErrorCode != "download_manifest_changed" &&
		acquisition.Download.FailureStage != "" &&
		!isMappingMaterializationFailure(acquisition.Download.Status, acquisition.Download.FailureStage, acquisition.Download.ErrorCode) {
		return true
	}
	for _, task := range acquisition.Tasks {
		if task.CanRetry {
			return true
		}
	}
	return false
}

func (service *ReadService) ListRSSEntries(ctx context.Context, subscriptionID uuid.UUID, cursor *uuid.UUID, limit int, status, group, query, rejectReason, sortBy, sortOrder *string) (domain.RSSEntryPage, error) {
	groupFilter := group
	if group != nil && *group != "confirmed" && *group != "skipped" {
		groupFilter = nil
	}
	var queryFilter, reasonFilter *string
	if query != nil {
		value := strings.ToLower(strings.TrimSpace(*query))
		if value != "" {
			queryFilter = &value
		}
	}
	if rejectReason != nil {
		value := strings.TrimSpace(*rejectReason)
		if value != "" {
			reasonFilter = &value
		}
	}
	var result domain.RSSEntryPage
	err := service.withListSnapshot(ctx, rssEntryListProjection, func(reader *ReadService) error {
		if _, err := reader.queries.GetRSSSubscription(ctx, repository.UUIDToPG(subscriptionID)); errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("get rss subscription: %w", err)
		}
		ids, err := reader.queries.ListRSSEntryPageIDs(ctx, db.ListRSSEntryPageIDsParams{SubscriptionID: repository.UUIDToPG(subscriptionID), Status: status, EntryGroup: groupFilter, Query: queryFilter, RejectReason: reasonFilter, SortBy: valueOrEmpty(sortBy), DescendingOrder: listSortDirection(sortOrder, "desc") < 0, Cursor: listCursorValue(cursor), RowLimit: fetchLimit(limit)})
		if err != nil {
			return fmt.Errorf("select RSS entry page: %w", err)
		}
		ids, next, err := listPageWindow(ids, limit, cursor)
		if err != nil {
			return err
		}
		rows, err := reader.queries.ListRSSEntryReadModelsByIDs(ctx, ids)
		if err != nil {
			return fmt.Errorf("load RSS entry page: %w", err)
		}
		rows, err = orderedListRows(ids, rows, func(row db.ListRSSEntryReadModelsByIDsRow) pgtype.UUID { return row.ID })
		if err != nil {
			return err
		}
		views, err := reader.rssEntryViews(ctx, rows)
		if err != nil {
			return err
		}
		result = domain.RSSEntryPage{Items: views, NextCursor: next}
		return nil
	})
	return result, err
}

func (service *ReadService) rssEntryViews(ctx context.Context, rows []db.ListRSSEntryReadModelsByIDsRow) ([]domain.RSSEntryView, error) {
	if len(rows) == 0 {
		return []domain.RSSEntryView{}, nil
	}
	entryIDs := make([]pgtype.UUID, len(rows))
	for index, row := range rows {
		entryIDs[index] = row.ID
	}
	acquisitionRows, err := service.queries.ListAcquisitionsByRSSEntryIDs(ctx, entryIDs)
	if err != nil {
		return nil, fmt.Errorf("list RSS entry acquisitions: %w", err)
	}
	acquisitions, err := service.acquisitionViews(ctx, acquisitionRows)
	if err != nil {
		return nil, err
	}
	acquisitionsByEntry := make(map[uuid.UUID]domain.AcquisitionView, len(acquisitions))
	for _, acquisition := range acquisitions {
		if acquisition.RSSEntryID != nil {
			acquisitionsByEntry[*acquisition.RSSEntryID] = acquisition
		}
	}
	archivedRows, err := service.queries.ListArchivedRSSEntryAcquisitionsByEntryIDs(ctx, entryIDs)
	if err != nil {
		return nil, fmt.Errorf("list archived RSS entry acquisitions: %w", err)
	}
	archivedByEntry := make(map[uuid.UUID]db.ListArchivedRSSEntryAcquisitionsByEntryIDsRow, len(archivedRows))
	for _, archived := range archivedRows {
		archivedByEntry[repository.UUIDFromPG(archived.EntryID)] = archived
	}

	pendingRetryIDs, err := service.queries.ListPendingRSSEntryRetriesByEntryIDs(ctx, entryIDs)
	if err != nil {
		return nil, fmt.Errorf("list pending RSS entry retries: %w", err)
	}
	pendingRetries := make(map[uuid.UUID]bool, len(pendingRetryIDs))
	for _, acquisition := range acquisitionRows {
		if acquisition.DeletionRequestedAt.Valid && acquisition.RssEntryID.Valid {
			pendingRetries[repository.UUIDFromPG(acquisition.RssEntryID)] = true
		}
	}
	for _, id := range pendingRetryIDs {
		pendingRetries[repository.UUIDFromPG(id)] = true
	}
	views := make([]domain.RSSEntryView, 0, len(rows))
	for _, source := range rows {
		row := source
		view := domain.RSSEntryView{
			ID:             repository.UUIDFromPG(row.ID),
			SubscriptionID: repository.UUIDFromPG(row.SubscriptionID),
			Title:          row.Title,
			Status:         row.Status,
			DuplicateCount: int(row.DuplicateCount),
			CreatedAt:      row.DiscoveredAt.Time,
			UpdatedAt:      row.UpdatedAt.Time,
		}
		if row.ReleaseCandidateID.Valid {
			value := repository.UUIDFromPG(row.ReleaseCandidateID)
			view.ReleaseCandidateID = &value
		}
		if row.PublishedAt.Valid {
			value := row.PublishedAt.Time
			view.PublishedAt = &value
		}
		if row.LastErrorCode != nil {
			view.ErrorCode = *row.LastErrorCode
		}
		if row.LastErrorMessage != nil {
			view.ErrorMessage = *row.LastErrorMessage
		}
		view.DownloadURIAvailable = row.Downloadable
		view.Version = rssEntryVersion(row.EnqueueAttempts)
		view.CanRetry = row.LastErrorRetryable || (row.Downloadable && (row.Status == string(domain.RSSDiscovered) || row.Status == string(domain.RSSEnqueued) || row.Status == string(domain.RSSEnqueueing)) && !row.ImportedAt.Valid)
		if row.SourceSeason != nil {
			value := int(*row.SourceSeason)
			view.SourceSeason = &value
		}
		if row.SourceEpisode != nil {
			value := int(*row.SourceEpisode)
			view.SourceEpisode = &value
		}
		view.SourceEpisodeFractionHundredths = int(row.SourceEpisodeFractionHundredths)
		if row.CoordinateSource != nil {
			view.CoordinateSource = *row.CoordinateSource
		}
		if row.AgentResolutionID.Valid {
			value := repository.UUIDFromPG(row.AgentResolutionID)
			view.AgentResolutionID = &value
		}
		if row.AdjudicationBatchID.Valid {
			value := repository.UUIDFromPG(row.AdjudicationBatchID)
			view.AdjudicationBatchID = &value
		}
		view.AdjudicationState = row.AdjudicationState
		if row.AdjudicationSource != nil {
			view.AdjudicationSource = *row.AdjudicationSource
		}
		if row.AdjudicationResolutionID.Valid {
			value := repository.UUIDFromPG(row.AdjudicationResolutionID)
			view.AdjudicationResolutionID = &value
		}
		if row.RelatedEntryID.Valid {
			value := repository.UUIDFromPG(row.RelatedEntryID)
			view.RelatedEntryID = &value
		}
		if row.ImportedAt.Valid {
			value := row.ImportedAt.Time.UTC()
			view.ImportedAt = &value
		}
		view.Classification = classifyRSSEntry(row)
		if len(row.RejectionReasons) > 0 {
			view.RejectReason = strings.Join(row.RejectionReasons, ",")
		}
		if acquisition, ok := acquisitionsByEntry[view.ID]; ok {
			acquisitionID := acquisition.ID
			view.AcquisitionID = &acquisitionID
			view.DownloadID = acquisition.DownloadID
			view.CanRetry = rssEntryCanRetry(acquisition)
			view.AcquisitionProgress = &domain.AcquisitionProgressView{
				AggregateStatus: acquisition.AggregateStatus,
				CurrentStage:    acquisition.CurrentStage,
				OverallProgress: acquisition.OverallProgress,
			}
		} else if archived, ok := archivedByEntry[view.ID]; ok {
			view.CanRetry = false
			acquisitionID := repository.UUIDFromPG(archived.AcquisitionID)
			view.AcquisitionID = &acquisitionID
			view.AcquisitionProgress = &domain.AcquisitionProgressView{
				AggregateStatus: "completed",
				CurrentStage:    "import",
				OverallProgress: 1,
			}
		}
		if view.Classification == "rejected" || view.Classification == "unconsumable" {
			view.CanRetry = len(row.RejectionReasons) == 1 && row.RejectionReasons[0] == rssTargetProcessingReason &&
				view.AcquisitionID == nil && !row.ImportedAt.Valid && row.AdjudicationState != "ignored"
		}
		if pendingRetries[view.ID] {
			view.CanRetry = false
		}
		views = append(views, view)
	}
	return views, nil
}

func rssEntryMatchesGroup(view domain.RSSEntryView, group string) bool {
	pending := view.AdjudicationState == "pending" && view.Classification == "pending"
	skipped := view.Classification == "rejected" || view.Classification == "unconsumable"
	switch group {
	case "confirmed":
		return !pending && !skipped
	case "skipped":
		return !pending && skipped
	default:
		return true
	}
}

func classifyRSSEntry(row db.ListRSSEntryReadModelsByIDsRow) string {
	if rssEntryHasTargetOccupancy(row) {
		return "rejected"
	}
	if row.AdjudicationState == "pending" {
		return "pending"
	}
	if row.AdjudicationState == "ignored" {
		return "rejected"
	}
	if valueOrEmpty(row.LastErrorCode) == "duplicate_torrent" {
		return "duplicate"
	}
	switch row.Status {
	case "enqueued":
		return "enqueued"
	case "enqueue_failed":
		return "enqueue_failed"
	case "enqueueing":
		return "pending"
	}
	if !row.Downloadable {
		if len(row.RejectionReasons) == 0 || (len(row.RejectionReasons) == 1 && row.RejectionReasons[0] == "download_uri_missing") {
			return "unconsumable"
		}
		return "rejected"
	}
	if row.DuplicateCount > 0 {
		return "duplicate"
	}
	return "pending"
}

func rssEntryHasTargetOccupancy(row db.ListRSSEntryReadModelsByIDsRow) bool {
	// 只有真实成功导入过的条目（结构化 provenance 存在 task.imported 事实）
	// 才保留完成历史：即使被占用核验残留了拒绝原因，也不再归入"已跳过"。
	// 被回收的冲突条目虽然会被写入 imported_at + managed_import 满足标记，
	// 但没有成功导入 provenance，仍按占用硬拒绝归入"已跳过"。
	if row.SuccessfulImportPresent && row.ImportedAt.Valid && valueOrEmpty(row.FulfillmentSource) == rssFulfillmentManagedImport {
		return false
	}
	if valueOrEmpty(row.FulfillmentSource) == rssFulfillmentEmbyCatalog {
		return true
	}
	for _, reason := range row.RejectionReasons {
		switch reason {
		case rssTargetInLibraryReason, rssTargetImportedReason, rssTargetProcessingReason:
			return true
		}
	}
	return false
}
