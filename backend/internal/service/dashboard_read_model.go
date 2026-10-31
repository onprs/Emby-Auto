package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func (service *ReadService) DashboardSummary(ctx context.Context) (domain.DashboardSummary, error) {
	var summary domain.DashboardSummary
	err := service.withListSnapshot(ctx, acquisitionListProjection, func(reader *ReadService) error {
		var err error
		summary, err = reader.dashboardSummary(ctx)
		return err
	})
	return summary, err
}

func (service *ReadService) dashboardSummary(ctx context.Context) (domain.DashboardSummary, error) {
	taskCounts, err := service.queries.DashboardTaskCounts(ctx)
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("count dashboard tasks: %w", err)
	}
	downloadCounts, err := service.queries.DashboardDownloadCounts(ctx)
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("count dashboard downloads: %w", err)
	}
	cleanupFailed, err := service.queries.DashboardCleanupFailedCount(ctx)
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("count dashboard cleanup: %w", err)
	}
	mappingPending, err := service.queries.DashboardMappingPendingCount(ctx)
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("count dashboard mapping: %w", err)
	}
	attentionCount, err := service.queries.CountAcquisitionListAttention(ctx)
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("count dashboard attention: %w", err)
	}
	attentionPhase := "attention"
	attentionIDs, err := service.queries.ListAcquisitionPageIDs(ctx, db.ListAcquisitionPageIDsParams{Phase: &attentionPhase, SortBy: "updated_at", DescendingOrder: true, RowLimit: 5})
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("select dashboard attention: %w", err)
	}
	attentionRows, err := service.queries.ListAcquisitionsByIDs(ctx, attentionIDs)
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("load dashboard attention: %w", err)
	}
	attentionRows, err = orderedListRows(attentionIDs, attentionRows, func(row db.Acquisition) pgtype.UUID { return row.ID })
	if err != nil {
		return domain.DashboardSummary{}, err
	}
	attention, err := service.acquisitionViews(ctx, attentionRows)
	if err != nil {
		return domain.DashboardSummary{}, err
	}

	recentTaskIDs, err := service.queries.DashboardRecentTaskIDs(ctx, 5)
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("list dashboard video tasks: %w", err)
	}
	recent, err := service.queries.DashboardRecentOperations(ctx, 10)
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("list dashboard operations: %w", err)
	}
	imports, err := service.queries.DashboardRecentImports(ctx, 5)
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("list dashboard imports: %w", err)
	}
	scans, err := service.queries.DashboardRecentScans(ctx, 5)
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("list dashboard scans: %w", err)
	}
	agentStats, err := service.queries.GetAgentResolutionDashboardStats(ctx)
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("count dashboard Agent resolutions: %w", err)
	}
	connectivity, err := service.queries.ListConnectivityTestResults(ctx)
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("list connectivity test results: %w", err)
	}
	summary := domain.DashboardSummary{
		Counts: domain.DashboardStatusCounts{
			Downloading:    saturatingInt(downloadCounts.Downloading),
			Processing:     saturatingInt(taskCounts.Processing),
			AwaitingReview: saturatingInt(taskCounts.AwaitingReview),
			Importing:      saturatingInt(taskCounts.Importing),
			Attention:      saturatingInt(attentionCount),
			Failed:         saturatingInt(taskCounts.Failed + downloadCounts.Failed),
			CleanupFailed:  saturatingInt(cleanupFailed),
			MappingPending: saturatingInt(mappingPending),
		},
		AgentResolutions: domain.DashboardAgentResolutionStats{
			Total: saturatingInt(agentStats.Total), ReviewPending: saturatingInt(agentStats.ReviewPending),
			Applied: saturatingInt(agentStats.Applied), AutoApplied: saturatingInt(agentStats.AutoApplied),
			Accepted: saturatingInt(agentStats.Accepted), Rejected: saturatingInt(agentStats.Rejected), Failed: saturatingInt(agentStats.Failed),
			InputTokens: agentStats.InputTokens, OutputTokens: agentStats.OutputTokens,
			AverageLatencyMilliseconds: agentStats.AverageLatencyMilliseconds,
		},
		AttentionItems:   make([]domain.DashboardAttentionItem, 0, len(attention)),
		RecentTasks:      make([]domain.EpisodeTask, 0, len(recentTaskIDs)),
		RecentOperations: make([]domain.DashboardRecentOperation, 0, len(recent)),
		RecentImports:    make([]domain.DashboardRecentImport, 0, len(imports)),
		RecentScans:      make([]domain.DashboardRecentScan, 0, len(scans)),
		Links: domain.DashboardLinks{
			Downloading: "/acquisitions?phase=downloading", Processing: "/acquisitions?phase=processing",
			AwaitingReview: "/acquisitions?phase=awaiting_review", Importing: "/acquisitions?phase=importing",
			Failed: "/acquisitions?phase=attention", CleanupFailed: "/operations?status=failed",
			MappingPending: "/acquisitions?phase=mapping_pending",
		},
	}
	for _, view := range attention {
		if item, ok := dashboardAttentionItem(view); ok {
			summary.AttentionItems = append(summary.AttentionItems, item)
		}
	}
	for _, id := range recentTaskIDs {
		row, err := service.queries.GetTaskView(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return domain.DashboardSummary{}, fmt.Errorf("load dashboard video task: %w", err)
		}
		summary.RecentTasks = append(summary.RecentTasks, taskFromGetRow(row))
	}
	for _, row := range recent {
		summary.RecentOperations = append(summary.RecentOperations, dashboardOperation(row))
	}
	for _, row := range imports {
		item := domain.DashboardRecentImport{
			TaskID: repository.UUIDFromPG(row.TaskID), AcquisitionID: repository.UUIDFromPG(row.AcquisitionID),
			MediaType: domain.TaskMediaType(row.MediaType), SeriesTitle: row.SeriesTitle,
			DestinationPath: valueOrEmpty(row.DestinationVideoPath), CompletedAt: row.CompletedAt.Time,
		}
		if item.MediaType == domain.TaskMediaMovie {
			item.MovieTitle, item.SeriesTitle = row.SeriesTitle, ""
			item.ReleaseYear = intValue(row.ReleaseYear)
		}
		if row.SeasonNumber != nil {
			value := int(*row.SeasonNumber)
			item.SeasonNumber = &value
		}
		if row.EpisodeNumber != nil {
			value := int(*row.EpisodeNumber)
			item.EpisodeNumber = &value
		}
		summary.RecentImports = append(summary.RecentImports, item)
	}
	for _, row := range scans {
		item := domain.DashboardRecentScan{
			ID: repository.UUIDFromPG(row.ID), OperationID: repository.UUIDFromPG(row.OperationID), Status: row.Status,
			LibraryCount: int(row.LibraryCount), ItemCount: int(row.ItemCount), ErrorCode: valueOrEmpty(row.ErrorCode),
			ErrorMessage: valueOrEmpty(row.ErrorMessage), CreatedAt: row.CreatedAt.Time,
		}
		if row.CompletedAt.Valid {
			value := row.CompletedAt.Time
			item.CompletedAt = &value
		}
		summary.RecentScans = append(summary.RecentScans, item)
	}
	for _, row := range connectivity {
		status := domain.DashboardDependencyStatus{
			Success: row.Success, Code: row.Code, Message: row.Message, TestedAt: row.TestedAt.Time, HasTest: true,
		}
		switch row.Target {
		case "qbittorrent":
			summary.Dependencies.QBittorrent = status
		case "tmdb":
			summary.Dependencies.TMDb = status
		case "emby":
			summary.Dependencies.Emby = status
		case "media_tools":
			summary.Dependencies.MediaTools = status
		case "network_proxy":
			summary.Dependencies.NetworkProxy = status
		case "agent":
			summary.Dependencies.Agent = status
		}
	}
	return summary, nil
}

func dashboardOperation(row db.Operation) domain.DashboardRecentOperation {
	item := domain.DashboardRecentOperation{
		ID: repository.UUIDFromPG(row.ID), Kind: row.Kind, Status: row.Status, UpdatedAt: row.UpdatedAt.Time,
		ErrorCode: valueOrEmpty(row.ErrorCode), ErrorMessage: valueOrEmpty(row.ErrorMessage),
	}
	if row.ResourceType != nil {
		item.ResourceType = *row.ResourceType
	}
	if row.ResourceID.Valid {
		value := repository.UUIDFromPG(row.ResourceID)
		item.ResourceID = &value
		item.ResourceHref = resourceHref(item.ResourceType, value)
	}
	return item
}
