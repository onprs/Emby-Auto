package service

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

// ReadService exposes paginated, database-backed read models for the API.
type ReadService struct {
	queries *db.Queries
}

func NewReadService(queries *db.Queries) *ReadService {
	return &ReadService{queries: queries}
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func pageCursor(hasMore bool, items int, idOf func(int) uuid.UUID) *uuid.UUID {
	if !hasMore || items == 0 {
		return nil
	}
	last := idOf(items - 1)
	return &last
}

func cursorWindow[T any](items []T, cursor *uuid.UUID, limit int, idOf func(T) uuid.UUID) ([]T, bool, error) {
	start := 0
	if cursor != nil {
		start = -1
		for index, item := range items {
			if idOf(item) == *cursor {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, false, NewError("invalid_cursor", "the list cursor was not found", ErrInvalidInput, map[string]any{"cursor": cursor.String()})
		}
	}
	lim := clampLimit(limit)
	end := min(len(items), start+lim)
	if start > len(items) {
		start = len(items)
	}
	return items[start:end], end < len(items), nil
}

func listSortDirection(order *string, defaultOrder string) int {
	value := defaultOrder
	if order != nil {
		value = *order
	}
	if value == "desc" {
		return -1
	}
	return 1
}

func compareTime(left, right time.Time) int {
	if left.Before(right) {
		return -1
	}
	if left.After(right) {
		return 1
	}
	return 0
}

// fetchLimit reads one extra row to determine whether another page exists.
func fetchLimit(limit int) int32 {
	return int32(clampLimit(limit) + 1)
}

// --- Downloads ---

func (service *ReadService) ListDownloads(ctx context.Context, cursor *uuid.UUID, limit int, status, phase, query, sortBy, sortOrder *string) (domain.DownloadPage, error) {
	const batchSize = 200
	baseSort := "newest"
	var batchCursor *uuid.UUID
	views := make([]domain.DownloadView, 0, batchSize)
	for {
		params := db.ListDownloadsParams{RowLimit: batchSize, Query: query, Sort: &baseSort, Status: status, Phase: phase}
		if batchCursor != nil {
			params.Cursor = repository.UUIDToPG(*batchCursor)
		}
		rows, err := service.queries.ListDownloads(ctx, params)
		if err != nil {
			return domain.DownloadPage{}, fmt.Errorf("list downloads: %w", err)
		}
		batch, err := service.downloadViews(ctx, rows)
		if err != nil {
			return domain.DownloadPage{}, err
		}
		views = append(views, batch...)
		if len(rows) < batchSize {
			break
		}
		last := repository.UUIDFromPG(rows[len(rows)-1].ID)
		batchCursor = &last
	}
	direction := listSortDirection(sortOrder, "desc")
	field := "updated_at"
	if sortBy != nil {
		field = *sortBy
	}
	sort.SliceStable(views, func(i, j int) bool {
		left, right := views[i], views[j]
		comparison := 0
		switch field {
		case "attempt":
			comparison = cmp.Compare(left.Attempt, right.Attempt)
		case "status":
			comparison = strings.Compare(left.Status, right.Status)
		case "client_state":
			comparison = strings.Compare(left.ClientState, right.ClientState)
		case "progress":
			comparison = cmp.Compare(left.Progress, right.Progress)
		default:
			comparison = compareTime(left.UpdatedAt, right.UpdatedAt)
		}
		if comparison == 0 {
			comparison = strings.Compare(left.ID.String(), right.ID.String())
		}
		return direction*comparison < 0
	})
	window, hasMore, err := cursorWindow(views, cursor, limit, func(item domain.DownloadView) uuid.UUID { return item.ID })
	if err != nil {
		return domain.DownloadPage{}, err
	}
	return domain.DownloadPage{
		Items:      window,
		NextCursor: pageCursor(hasMore, len(window), func(i int) uuid.UUID { return window[i].ID }),
	}, nil
}

func (service *ReadService) GetDownload(ctx context.Context, id uuid.UUID) (domain.DownloadView, error) {
	row, err := service.queries.GetDownloadByID(ctx, repository.UUIDToPG(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DownloadView{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.DownloadView{}, fmt.Errorf("get download: %w", err)
	}
	views, err := service.downloadViews(ctx, []db.Download{row})
	if err != nil {
		return domain.DownloadView{}, err
	}
	return views[0], nil
}

func (service *ReadService) downloadViews(ctx context.Context, rows []db.Download) ([]domain.DownloadView, error) {
	views := make([]domain.DownloadView, 0, len(rows))
	for _, row := range rows {
		files, err := service.queries.ListDownloadFiles(ctx, row.ID)
		if err != nil {
			return nil, fmt.Errorf("list download files: %w", err)
		}
		views = append(views, downloadViewFromDB(row, files))
	}
	return views, nil
}

func downloadViewFromDB(row db.Download, files []db.DownloadFile) domain.DownloadView {
	hasSupportedVideo := false
	for _, file := range files {
		if file.MediaKind == string(domain.MediaVideo) {
			hasSupportedVideo = true
			break
		}
	}
	view := domain.DownloadView{
		ID:            repository.UUIDFromPG(row.ID),
		AcquisitionID: repository.UUIDFromPG(row.AcquisitionID),
		Attempt:       int(row.Attempt),
		ClientName:    row.ClientName,
		Status:        row.Status,
		Progress:      numericToFloat(row.Progress),
		Version:       int(row.Version),
		CreatedAt:     row.CreatedAt.Time,
		UpdatedAt:     row.UpdatedAt.Time,
		Files:         make([]domain.DownloadFileView, 0, len(files)),
		Actions: domain.DownloadActions{
			CanRetry: row.Status == "failed" && row.FailureStage != nil &&
				!isMappingMaterializationFailure(row.Status, valueOrEmpty(row.FailureStage), valueOrEmpty(row.ErrorCode)) &&
				!row.DeletionRequestedAt.Valid,
			CanCancel:            (row.Status == "enqueue_pending" || row.Status == "file_resolution_pending" || row.Status == "downloading" || row.Status == "completed" || row.Status == "selecting_files") && !row.DeletionRequestedAt.Valid,
			CanDelete:            (row.Status == "completed" || row.Status == "materialized" || row.Status == "failed" || row.Status == "cancelled") && !row.DeletionRequestedAt.Valid && !row.DeletedAt.Valid,
			CanEditFileSelection: (row.Status == "completed" || row.Status == "selecting_files") && !row.DeletionRequestedAt.Valid,
			CanResolveFiles:      hasSupportedVideo && (row.Status == "file_resolution_pending" || (row.Status == "failed" && valueOrEmpty(row.FailureStage) == "file_resolution")) && !row.DeletionRequestedAt.Valid,
			CanRequestAgent:      row.FileResolutionSource == nil && hasSupportedVideo && (row.Status == "file_resolution_pending" || (row.Status == "failed" && valueOrEmpty(row.FailureStage) == "file_resolution")) && !row.DeletionRequestedAt.Valid,
		},
	}
	if row.ClientState != nil {
		view.ClientState = *row.ClientState
	}
	if row.TorrentHash != nil {
		view.TorrentHash = *row.TorrentHash
	}
	if row.FailureStage != nil {
		view.FailureStage = *row.FailureStage
	}
	if row.FileResolutionSource != nil {
		view.FileResolutionSource = *row.FileResolutionSource
	}
	if row.AgentResolutionID.Valid {
		value := repository.UUIDFromPG(row.AgentResolutionID)
		view.AgentResolutionID = &value
	}
	if row.LastSyncedAt.Valid {
		value := row.LastSyncedAt.Time
		view.LastSyncedAt = &value
	}
	if row.SavePath != nil {
		view.SavePath = *row.SavePath
	}
	if row.ErrorCode != nil {
		view.ErrorCode = *row.ErrorCode
	}
	if row.ErrorMessage != nil {
		view.ErrorMessage = *row.ErrorMessage
	}
	if row.StartedAt.Valid {
		value := row.StartedAt.Time
		view.StartedAt = &value
	}
	if row.CompletedAt.Valid {
		value := row.CompletedAt.Time
		view.CompletedAt = &value
	}
	for _, file := range files {
		item := domain.DownloadFileView{
			ID: repository.UUIDFromPG(file.ID), FileIndex: int(file.FileIndex),
			RelativePath: file.RelativePath, SizeBytes: file.SizeBytes,
			MediaKind: file.MediaKind, Selected: file.Selected,
			SourceEpisodeFractionHundredths: int(file.SourceEpisodeFractionHundredths),
		}
		if file.SourceSeason != nil {
			value := int(*file.SourceSeason)
			item.SourceSeason = &value
		}
		if file.SourceEpisode != nil {
			value := int(*file.SourceEpisode)
			item.SourceEpisode = &value
		}
		if file.Language != nil {
			item.Language = *file.Language
		}
		item.ExclusionReason = downloadFileExclusionReason(file, files)
		view.Files = append(view.Files, item)
	}
	return view
}

func downloadFileExclusionReason(file db.DownloadFile, files []db.DownloadFile) string {
	if file.Selected {
		return ""
	}
	switch file.MediaKind {
	case "extra":
		return "extra_content"
	case "unknown", "other":
		return "unsupported_media"
	case "video", "subtitle":
		if file.SourceSeason == nil || file.SourceEpisode == nil {
			return "episode_not_detected"
		}
	}
	for _, selected := range files {
		if !selected.Selected || selected.MediaKind != file.MediaKind || selected.SourceSeason == nil || selected.SourceEpisode == nil {
			continue
		}
		if *selected.SourceSeason == *file.SourceSeason && *selected.SourceEpisode == *file.SourceEpisode &&
			selected.SourceEpisodeFractionHundredths == file.SourceEpisodeFractionHundredths {
			if file.MediaKind == "video" {
				return "alternate_video"
			}
			return "alternate_subtitle"
		}
	}
	if file.MediaKind == "subtitle" {
		return "no_matching_video"
	}
	return "not_selected"
}

func numericToFloat(value pgtype.Numeric) float64 {
	if !value.Valid {
		return 0
	}
	float, err := value.Float64Value()
	if err != nil {
		return 0
	}
	return float.Float64
}

// --- Searches ---

func (service *ReadService) ListSearches(ctx context.Context, cursor *uuid.UUID, limit int, status *string, query *string) (domain.SearchRunSummaryPage, error) {
	params := db.ListSearchRunsParams{RowLimit: fetchLimit(limit)}
	if cursor != nil {
		params.Cursor = repository.UUIDToPG(*cursor)
	}
	if status != nil {
		params.Status = status
	}
	if query != nil && *query != "" {
		params.Query = query
	}
	rows, err := service.queries.ListSearchRuns(ctx, params)
	if err != nil {
		return domain.SearchRunSummaryPage{}, fmt.Errorf("list search runs: %w", err)
	}
	views := make([]domain.SearchRunSummary, 0, len(rows))
	for _, row := range rows {
		view := domain.SearchRunSummary{
			ID:        repository.UUIDFromPG(row.ID),
			Query:     row.Query,
			Status:    row.Status,
			CreatedAt: row.CreatedAt.Time,
			UpdatedAt: row.UpdatedAt.Time,
		}
		if row.ErrorCode != nil {
			view.ErrorCode = *row.ErrorCode
		}
		if row.ErrorMessage != nil {
			view.ErrorMessage = *row.ErrorMessage
		}
		views = append(views, view)
	}
	lim := clampLimit(limit)
	hasMore := len(views) > lim
	if hasMore {
		views = views[:lim]
	}
	return domain.SearchRunSummaryPage{
		Items:      views,
		NextCursor: pageCursor(hasMore, len(views), func(i int) uuid.UUID { return views[i].ID }),
	}, nil
}

// --- Acquisitions ---

func (service *ReadService) ListAcquisitions(ctx context.Context, cursor *uuid.UUID, limit int, sourceKind *string, tmdbSeriesID *int64, phase, sortBy, sortOrder *string) (domain.AcquisitionPage, error) {
	views, err := service.listAcquisitionViews(ctx, sourceKind, tmdbSeriesID, phase)
	if err != nil {
		return domain.AcquisitionPage{}, err
	}
	direction := listSortDirection(sortOrder, "desc")
	field := "updated_at"
	if sortBy != nil {
		field = *sortBy
	}
	sortAcquisitionViews(views, field, direction)
	window, hasMore, err := cursorWindow(views, cursor, limit, func(item domain.AcquisitionView) uuid.UUID { return item.ID })
	if err != nil {
		return domain.AcquisitionPage{}, err
	}
	return domain.AcquisitionPage{
		Items:      window,
		NextCursor: pageCursor(hasMore, len(window), func(i int) uuid.UUID { return window[i].ID }),
	}, nil
}

func (service *ReadService) listAcquisitionViews(ctx context.Context, sourceKind *string, tmdbSeriesID *int64, phase *string) ([]domain.AcquisitionView, error) {
	const batchSize = acquisitionListBatchSize
	baseSort := "newest"
	var batchCursor *uuid.UUID
	views := make([]domain.AcquisitionView, 0, batchSize)
	for {
		params := db.ListAcquisitionsParams{RowLimit: batchSize, Sort: &baseSort, SourceKind: sourceKind, TmdbSeriesID: tmdbSeriesID}
		if phase != nil && *phase == "mapping_pending" {
			params.Phase = phase
		}
		if batchCursor != nil {
			params.Cursor = repository.UUIDToPG(*batchCursor)
		}
		rows, err := service.queries.ListAcquisitions(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("list acquisitions: %w", err)
		}
		batch, err := service.acquisitionViews(ctx, rows)
		if err != nil {
			return nil, err
		}
		for _, view := range batch {
			if phase == nil || acquisitionStageMatches(*phase, view) {
				views = append(views, view)
			}
		}
		if len(rows) < batchSize {
			break
		}
		last := repository.UUIDFromPG(rows[len(rows)-1].ID)
		batchCursor = &last
	}
	return views, nil
}

func acquisitionSortTitle(view domain.AcquisitionView) string {
	if view.MediaType == domain.TaskMediaMovie {
		return view.MovieTitle
	}
	return view.SeriesTitle
}

func compareAcquisitionContent(left, right domain.AcquisitionView) int {
	if comparison := strings.Compare(strings.ToLower(acquisitionSortTitle(left)), strings.ToLower(acquisitionSortTitle(right))); comparison != 0 {
		return comparison
	}
	if left.MediaType == domain.TaskMediaMovie || right.MediaType == domain.TaskMediaMovie {
		if comparison := strings.Compare(string(left.MediaType), string(right.MediaType)); comparison != 0 {
			return comparison
		}
		return cmp.Compare(left.ReleaseYear, right.ReleaseYear)
	}
	return compareOptionalEpisode(left.SourceSeason, left.SourceEpisode, right.SourceSeason, right.SourceEpisode)
}

func sortAcquisitionViews(views []domain.AcquisitionView, field string, direction int) {
	sort.SliceStable(views, func(i, j int) bool {
		left, right := views[i], views[j]
		comparison := 0
		switch field {
		case "content":
			comparison = compareAcquisitionContent(left, right)
		case "source_kind":
			comparison = strings.Compare(left.SourceKind, right.SourceKind)
		case "progress":
			comparison = cmp.Compare(left.OverallProgress, right.OverallProgress)
		default:
			comparison = compareTime(left.UpdatedAt, right.UpdatedAt)
		}
		if comparison == 0 {
			comparison = strings.Compare(left.ID.String(), right.ID.String())
		}
		return direction*comparison < 0
	})
}

// acquisitionListBatchSize bounds each database round-trip while deriving
// lifecycle values used by global filtering and column sorting.
const acquisitionListBatchSize = 200

// acquisitionStageMatches groups the user-facing aggregate statuses into the
// task stages presented by the interface.
func acquisitionStageMatches(phase string, view domain.AcquisitionView) bool {
	switch phase {
	case "downloading":
		return view.AggregateStatus == "pending" || view.AggregateStatus == "downloading"
	case "processing":
		return view.AggregateStatus == "materializing" || view.AggregateStatus == "processing"
	case "awaiting_review":
		return view.AggregateStatus == "awaiting_review"
	case "importing":
		return view.AggregateStatus == "importing"
	case "completed":
		return view.AggregateStatus == "completed"
	case "mapping_pending":
		return view.AggregateStatus == "mapping_pending"
	case "attention":
		_, needsAttention := dashboardAttentionItem(view)
		return needsAttention
	default:
		return false
	}
}

func dashboardAttentionItem(view domain.AcquisitionView) (domain.DashboardAttentionItem, bool) {
	item := domain.DashboardAttentionItem{Acquisition: view}
	switch view.AggregateStatus {
	case "failed":
		item.Reason = "workflow_failed"
		if view.Download != nil && view.Download.Status == "failed" {
			item.ErrorCode = view.Download.ErrorCode
			item.ErrorMessage = view.Download.ErrorMessage
			return item, true
		}
		for _, task := range view.Tasks {
			if task.State == "failed" || task.ImportStatus == "failed" {
				item.ErrorCode = task.ErrorCode
				item.ErrorMessage = task.ErrorMessage
				break
			}
		}
		return item, true
	case "mapping_pending":
		item.Reason = "mapping_required"
		return item, true
	}
	for _, task := range view.Tasks {
		if task.CleanupStatus == "failed" {
			item.Reason = "cleanup_failed"
			return item, true
		}
	}
	for _, task := range view.Tasks {
		if task.EmbyRefreshStatus == "failed" {
			item.Reason = "emby_refresh_failed"
			return item, true
		}
	}
	if view.AggregateStatus == "rejected" {
		item.Reason = "review_rejected"
		return item, true
	}
	return domain.DashboardAttentionItem{}, false
}

func (service *ReadService) GetAcquisition(ctx context.Context, id uuid.UUID) (domain.AcquisitionView, error) {
	row, err := service.queries.GetAcquisitionByID(ctx, repository.UUIDToPG(id))
	if errors.Is(err, pgx.ErrNoRows) {
		archived, archiveErr := service.queries.GetArchivedRSSAcquisitionByID(ctx, repository.UUIDToPG(id))
		if errors.Is(archiveErr, pgx.ErrNoRows) {
			return domain.AcquisitionView{}, domain.ErrNotFound
		}
		if archiveErr != nil {
			return domain.AcquisitionView{}, fmt.Errorf("get archived RSS acquisition: %w", archiveErr)
		}
		return archivedRSSAcquisitionView(archived), nil
	}
	if err != nil {
		return domain.AcquisitionView{}, fmt.Errorf("get acquisition: %w", err)
	}
	views, err := service.acquisitionViews(ctx, []db.Acquisition{row})
	if err != nil {
		return domain.AcquisitionView{}, err
	}
	return views[0], nil
}

func (service *ReadService) acquisitionViews(ctx context.Context, rows []db.Acquisition) ([]domain.AcquisitionView, error) {
	acquisitionIDs := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		acquisitionIDs = append(acquisitionIDs, row.ID)
	}
	sourceTitles := make(map[uuid.UUID]string, len(rows))
	if len(acquisitionIDs) > 0 {
		titleRows, err := service.queries.ListAcquisitionSourceTitles(ctx, acquisitionIDs)
		if err != nil {
			return nil, fmt.Errorf("load acquisition source titles: %w", err)
		}
		for _, title := range titleRows {
			sourceTitles[repository.UUIDFromPG(title.AcquisitionID)] = strings.TrimSpace(title.SourceTitle)
		}
	}

	views := make([]domain.AcquisitionView, 0, len(rows))
	for _, row := range rows {
		view := domain.AcquisitionView{
			ID:          repository.UUIDFromPG(row.ID),
			SeriesID:    repository.UUIDFromPG(row.SeriesID),
			SourceKind:  row.SourceKind,
			SourceTitle: sourceTitles[repository.UUIDFromPG(row.ID)],
			CreatedAt:   row.CreatedAt.Time,
			UpdatedAt:   row.UpdatedAt.Time,
		}
		if row.MappingProfileID.Valid {
			value := repository.UUIDFromPG(row.MappingProfileID)
			view.MappingProfileID = &value
			audit, auditErr := service.queries.GetMappingProfileAudit(ctx, row.MappingProfileID)
			if auditErr == nil {
				view.MappingDecisionSource = audit.DecisionSource
				if audit.AgentResolutionID.Valid {
					resolutionID := repository.UUIDFromPG(audit.AgentResolutionID)
					view.MappingAgentResolutionID = &resolutionID
				}
			} else if !errors.Is(auditErr, pgx.ErrNoRows) {
				return nil, fmt.Errorf("load Mapping profile audit: %w", auditErr)
			}
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
			view.SourceSeason = payload.SourceSeason
			view.SourceEpisode = payload.SourceEpisode
			view.SourceEpisodeFractionHundredths = payload.SourceEpisodeFractionHundredths
			view.SingleEpisode = payload.SingleEpisode
		}

		media, err := service.queries.GetMediaSeriesByID(ctx, row.SeriesID)
		if err == nil {
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
		download, err := service.queries.GetLatestDownloadByAcquisition(ctx, row.ID)
		if err == nil {
			downloadID := repository.UUIDFromPG(download.ID)
			view.DownloadID = &downloadID
			downloadStatus = download.Status
			view.Download = &domain.AcquisitionDownloadSummary{
				ID: downloadID, Attempt: int(download.Attempt), Status: download.Status, Progress: numericToFloat(download.Progress),
				FailureStage: valueOrEmpty(download.FailureStage), ClientState: valueOrEmpty(download.ClientState),
				ErrorCode: valueOrEmpty(download.ErrorCode), ErrorMessage: valueOrEmpty(download.ErrorMessage), UpdatedAt: download.UpdatedAt.Time,
			}
			view.UpdatedAt = laterTime(view.UpdatedAt, download.UpdatedAt.Time)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("load acquisition download: %w", err)
		}

		taskRows, err := service.queries.ListAcquisitionTaskSummaries(ctx, row.ID)
		if err != nil {
			return nil, fmt.Errorf("list acquisition tasks: %w", err)
		}
		view.Tasks = make([]domain.AcquisitionTaskSummary, 0, len(taskRows))
		for _, task := range taskRows {
			summary := domain.AcquisitionTaskSummary{
				ID: repository.UUIDFromPG(task.ID), MediaType: domain.TaskMediaType(task.MediaType), DownloadID: repository.UUIDFromPG(task.DownloadID),
				State: task.State, VideoState: task.VideoState, SubtitleState: task.SubtitleState,
				ArtifactBasename: valueOrEmpty(task.ArtifactBasename), ReviewDecision: valueOrEmpty(task.ReviewDecision),
				ImportStatus: task.ImportStatus, DestinationVideoPath: valueOrEmpty(task.DestinationVideoPath),
				DestinationSubtitlePath: valueOrEmpty(task.DestinationSubtitlePath), EmbyRefreshStatus: task.EmbyRefreshStatus,
				CleanupStatus: task.CleanupStatus, FailureStage: valueOrEmpty(task.FailureStage), ErrorCode: valueOrEmpty(task.ErrorCode),
				ErrorMessage: valueOrEmpty(task.ErrorMessage), TargetEpisodeTitle: valueOrEmpty(task.TargetEpisodeTitle),
				CanRetry:  isTaskRetryable(task.State, task.VideoState, task.SubtitleState, valueOrEmpty(task.FailureStage), task.CleanupStatus),
				UpdatedAt: task.UpdatedAt.Time,
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
		mapping, err := service.queries.GetAcquisitionMappingCompleteness(ctx, row.ID)
		if err != nil {
			return nil, fmt.Errorf("get acquisition mapping completeness: %w", err)
		}
		view.Mapping = domain.AcquisitionMappingCompleteness{
			SelectedVideoCount: saturatingInt(mapping.SelectedVideoCount),
			MappedVideoCount:   saturatingInt(mapping.MappedVideoCount),
			Complete:           mapping.SelectedVideoCount > 0 && mapping.SelectedVideoCount == mapping.MappedVideoCount,
		}
		deriveAcquisitionLifecycle(&view, downloadStatus)
		views = append(views, view)
	}
	return views, nil
}

func archivedRSSAcquisitionView(row db.GetArchivedRSSAcquisitionByIDRow) domain.AcquisitionView {
	createdAt := row.AcquisitionCreatedAt.Time.UTC()
	importedAt := row.ImportedAt.Time.UTC()
	archivedAt := row.ArchivedAt.Time.UTC()
	entryID := repository.UUIDFromPG(row.EntryID)
	mappingProfileID := repository.UUIDFromPG(row.MappingProfileID)
	view := domain.AcquisitionView{
		ID:               repository.UUIDFromPG(row.AcquisitionID),
		Archived:         true,
		ArchivedAt:       &archivedAt,
		MediaType:        domain.TaskMediaEpisode,
		SeriesID:         repository.UUIDFromPG(row.SeriesID),
		SeriesTitle:      row.SeriesTitle,
		SourceKind:       "rss",
		SourceTitle:      row.SourceTitle,
		MappingProfileID: &mappingProfileID,
		RSSEntryID:       &entryID,
		Tasks:            []domain.AcquisitionTaskSummary{},
		Mapping: domain.AcquisitionMappingCompleteness{
			SelectedVideoCount: 1,
			MappedVideoCount:   1,
			Complete:           true,
		},
		AggregateStatus: "completed",
		CurrentStage:    "import",
		OverallProgress: 1,
		CreatedAt:       createdAt,
		UpdatedAt:       archivedAt,
	}
	if row.TmdbSeriesID != nil {
		view.TMDbSeriesID = *row.TmdbSeriesID
	}
	if row.SourceSeason != nil {
		value := int(*row.SourceSeason)
		view.SourceSeason = &value
	}
	if row.SourceEpisode != nil {
		value := int(*row.SourceEpisode)
		view.SourceEpisode = &value
	}
	view.SourceEpisodeFractionHundredths = int(row.SourceEpisodeFractionHundredths)
	view.Stages = []domain.AcquisitionStageView{
		archivedCompletedStage("source", row.AcquisitionCreatedAt, importedAt),
		archivedCompletedStage("download", row.TaskCreatedAt, importedAt),
		archivedCompletedStage("mapping", row.TaskCreatedAt, importedAt),
		archivedCompletedStage("transcode", row.VideoReadyAt, importedAt),
		archivedCompletedStage("subtitle", row.SubtitleReadyAt, importedAt),
		archivedCompletedStage("rename", row.ArtifactReadyAt, importedAt),
		archivedCompletedStage("organize", row.ArtifactReadyAt, importedAt),
		archivedCompletedStage("review", row.ReviewedAt, importedAt),
		archivedCompletedStage("import", row.ImportedAt, importedAt),
	}
	return view
}

func archivedCompletedStage(key string, occurredAt pgtype.Timestamptz, fallback time.Time) domain.AcquisitionStageView {
	updatedAt := fallback
	if occurredAt.Valid {
		updatedAt = occurredAt.Time.UTC()
	}
	return domain.AcquisitionStageView{
		Key: key, Status: stageCompleted, Progress: 1,
		CompletedItems: 1, TotalItems: 1, UpdatedAt: &updatedAt,
	}
}

const (
	stagePending   = "pending"
	stageBlocked   = "blocked"
	stageRunning   = "running"
	stageWaiting   = "waiting"
	stageCompleted = "completed"
	stageFailed    = "failed"
	stageRejected  = "rejected"
	stageCancelled = "cancelled"
	stageSkipped   = "skipped"

	acquisitionSourceWeight    = 0.02
	acquisitionDownloadWeight  = 0.28
	acquisitionMappingWeight   = 0.05
	acquisitionTranscodeWeight = 0.35
	acquisitionSubtitleWeight  = 0.10
	acquisitionRenameWeight    = 0.03
	acquisitionOrganizeWeight  = 0.03
	acquisitionReviewWeight    = 0.05
	acquisitionImportWeight    = 0.09
)

func deriveAcquisitionLifecycle(view *domain.AcquisitionView, downloadStatus string) {
	mappingDownloadStatus := downloadStatus
	if acquisitionWaitsForMapping(*view) {
		mappingDownloadStatus = "completed"
	}
	stages := []domain.AcquisitionStageView{
		{Key: "source", Status: stageCompleted, Progress: 1, CompletedItems: 1, TotalItems: 1, UpdatedAt: timeValue(view.CreatedAt)},
		downloadAcquisitionStage(view.Download),
		mappingAcquisitionStage(*view, mappingDownloadStatus),
		mediaBranchAcquisitionStage("transcode", view.Tasks, func(task domain.AcquisitionTaskSummary) string { return task.VideoState }),
		mediaBranchAcquisitionStage("subtitle", view.Tasks, func(task domain.AcquisitionTaskSummary) string { return task.SubtitleState }),
		artifactAcquisitionStage("rename", view.Tasks),
		artifactAcquisitionStage("organize", view.Tasks),
		reviewAcquisitionStage(view.Tasks),
		importAcquisitionStage(view.Tasks),
	}
	view.Stages = stages
	view.CurrentStage = currentAcquisitionStage(stages)
	view.OverallProgress = acquisitionProgress(stages)
	view.AggregateStatus = acquisitionAggregateStatus(*view, downloadStatus)
}

func downloadAcquisitionStage(download *domain.AcquisitionDownloadSummary) domain.AcquisitionStageView {
	stage := domain.AcquisitionStageView{Key: "download", Status: stagePending, TotalItems: 1}
	if download == nil {
		return stage
	}
	stage.UpdatedAt = timeValue(download.UpdatedAt)
	stage.Progress = max(0, min(1, download.Progress))
	switch download.Status {
	case "completed", "selecting_files", "materialized":
		stage.Status, stage.Progress, stage.CompletedItems = stageCompleted, 1, 1
	case "failed":
		if download.FailureStage == "materialize" && download.Progress >= 1 {
			stage.Status, stage.Progress, stage.CompletedItems = stageCompleted, 1, 1
		} else {
			stage.Status = stageFailed
		}
	case "cancelled":
		stage.Status = stageCancelled
	case "enqueue_pending", "file_resolution_pending", "downloading":
		stage.Status = stageRunning
	}
	return stage
}

func mappingAcquisitionStage(view domain.AcquisitionView, downloadStatus string) domain.AcquisitionStageView {
	if view.MediaType == domain.TaskMediaMovie {
		return domain.AcquisitionStageView{Key: "mapping", Status: stageSkipped, Progress: 1}
	}
	stage := domain.AcquisitionStageView{
		Key: "mapping", Status: stagePending, CompletedItems: view.Mapping.MappedVideoCount,
		TotalItems: view.Mapping.SelectedVideoCount, UpdatedAt: latestTaskTime(view.Tasks),
	}
	if stage.TotalItems > 0 {
		stage.Progress = float64(stage.CompletedItems) / float64(stage.TotalItems)
	}
	switch {
	case view.Mapping.Complete:
		stage.Status, stage.Progress = stageCompleted, 1
	case downloadStatus == "failed":
		stage.Status = stageBlocked
	case downloadStatus == "cancelled":
		stage.Status = stageCancelled
	case downloadStatus == "completed" || downloadStatus == "selecting_files" || downloadStatus == "materialized":
		stage.Status = stageWaiting
	}
	return stage
}

func mediaBranchAcquisitionStage(
	key string,
	tasks []domain.AcquisitionTaskSummary,
	stateOf func(domain.AcquisitionTaskSummary) string,
) domain.AcquisitionStageView {
	stage := domain.AcquisitionStageView{Key: key, Status: stagePending, TotalItems: len(tasks), UpdatedAt: latestTaskTime(tasks)}
	if len(tasks) == 0 {
		return stage
	}
	failed, cancelled, running := 0, 0, 0
	progressUnits := float64(0)
	for _, task := range tasks {
		state := stateOf(task)
		ready := (key == "transcode" && state == "video_ready") || (key == "subtitle" && state == "ass_ready")
		switch {
		case ready:
			stage.CompletedItems++
			progressUnits++
		case state == "failed" || (task.State == "failed" && task.FailureStage == branchFailureStage(key)):
			failed++
		case state == "cancelled":
			cancelled++
		case state == "transcoding" || state == "extracting_or_converting":
			running++
			progressUnits += 0.5
		}
	}
	stage.Progress = progressUnits / float64(stage.TotalItems)
	switch {
	case failed > 0:
		stage.Status = stageFailed
	case stage.CompletedItems == stage.TotalItems:
		stage.Status, stage.Progress = stageCompleted, 1
	case cancelled > 0 && stage.CompletedItems+cancelled == stage.TotalItems:
		stage.Status = stageCancelled
	case running > 0 || stage.CompletedItems > 0:
		stage.Status = stageRunning
	}
	return stage
}

func branchFailureStage(key string) string {
	if key == "transcode" {
		return "video"
	}
	return "subtitle"
}

func artifactAcquisitionStage(key string, tasks []domain.AcquisitionTaskSummary) domain.AcquisitionStageView {
	stage := domain.AcquisitionStageView{Key: key, Status: stagePending, TotalItems: len(tasks), UpdatedAt: latestTaskTime(tasks)}
	if len(tasks) == 0 {
		return stage
	}
	failed, cancelled, running := 0, 0, 0
	for _, task := range tasks {
		switch {
		case task.ArtifactBasename != "":
			stage.CompletedItems++
		case task.State == "failed" && task.FailureStage == "finalize":
			failed++
		case task.State == "cancelled":
			cancelled++
		case task.State == "finalizing":
			running++
		}
	}
	stage.Progress = float64(stage.CompletedItems) / float64(stage.TotalItems)
	switch {
	case failed > 0:
		stage.Status = stageFailed
	case stage.CompletedItems == stage.TotalItems:
		stage.Status, stage.Progress = stageCompleted, 1
	case cancelled > 0 && stage.CompletedItems+cancelled == stage.TotalItems:
		stage.Status = stageCancelled
	case running > 0 || stage.CompletedItems > 0:
		stage.Status = stageRunning
	}
	return stage
}

func reviewAcquisitionStage(tasks []domain.AcquisitionTaskSummary) domain.AcquisitionStageView {
	stage := domain.AcquisitionStageView{Key: "review", Status: stagePending, TotalItems: len(tasks), UpdatedAt: latestTaskTime(tasks)}
	if len(tasks) == 0 {
		return stage
	}
	rejected, cancelled, waiting := 0, 0, 0
	for _, task := range tasks {
		switch task.ReviewDecision {
		case "approved":
			stage.CompletedItems++
			if task.ReviewedAt != nil {
				stage.UpdatedAt = laterTimePointer(stage.UpdatedAt, *task.ReviewedAt)
			}
		case "rejected":
			stage.CompletedItems++
			rejected++
			if task.ReviewedAt != nil {
				stage.UpdatedAt = laterTimePointer(stage.UpdatedAt, *task.ReviewedAt)
			}
		default:
			if task.State == "awaiting_review" {
				waiting++
			}
			if task.State == "cancelled" {
				cancelled++
			}
		}
	}
	stage.Progress = float64(stage.CompletedItems) / float64(stage.TotalItems)
	switch {
	case rejected > 0:
		stage.Status = stageRejected
	case stage.CompletedItems == stage.TotalItems:
		stage.Status, stage.Progress = stageCompleted, 1
	case cancelled > 0 && stage.CompletedItems+cancelled == stage.TotalItems:
		stage.Status = stageCancelled
	case waiting > 0 || stage.CompletedItems > 0:
		stage.Status = stageWaiting
	}
	return stage
}

func importAcquisitionStage(tasks []domain.AcquisitionTaskSummary) domain.AcquisitionStageView {
	stage := domain.AcquisitionStageView{Key: "import", Status: stagePending, TotalItems: len(tasks), UpdatedAt: latestTaskTime(tasks)}
	if len(tasks) == 0 {
		return stage
	}
	failed, rejected, cancelled, active, waiting := 0, 0, 0, 0, 0
	progressUnits := float64(0)
	for _, task := range tasks {
		switch {
		case task.ReviewDecision == "rejected" || task.State == "rejected":
			rejected++
		case task.State == "imported" && task.ImportStatus == "succeeded":
			stage.CompletedItems++
			progressUnits++
		case task.State == "failed" && task.FailureStage == "import" || task.ImportStatus == "failed":
			failed++
		case task.State == "cancelled" || task.ImportStatus == "cancelled":
			cancelled++
		case task.State == "importing" || task.ImportStatus == "running":
			active++
			progressUnits += 0.5
		case task.State == "import_queued" || task.ImportStatus == "queued":
			active++
			progressUnits += 0.25
		case task.State == "approved":
			waiting++
		}
	}
	stage.Progress = progressUnits / float64(stage.TotalItems)
	switch {
	case failed > 0:
		stage.Status = stageFailed
	case rejected == stage.TotalItems:
		stage.Status, stage.Progress = stageSkipped, 1
	case rejected > 0:
		stage.Status = stageRejected
	case stage.CompletedItems == stage.TotalItems:
		stage.Status, stage.Progress = stageCompleted, 1
	case cancelled > 0 && stage.CompletedItems+cancelled == stage.TotalItems:
		stage.Status = stageCancelled
	case active > 0 || stage.CompletedItems > 0:
		stage.Status = stageRunning
	case waiting > 0:
		stage.Status = stageWaiting
	}
	return stage
}

func currentAcquisitionStage(stages []domain.AcquisitionStageView) string {
	for _, status := range []string{stageFailed, stageRejected, stageCancelled} {
		for _, stage := range stages {
			if stage.Status == status {
				return stage.Key
			}
		}
	}
	for _, stage := range stages {
		switch stage.Status {
		case stageRunning, stageWaiting, stageBlocked, stagePending:
			return stage.Key
		}
	}
	return "import"
}

func acquisitionProgress(stages []domain.AcquisitionStageView) float64 {
	totalWeight, weightedProgress := float64(0), float64(0)
	for _, stage := range stages {
		if stage.Status == stageSkipped {
			continue
		}
		weight := acquisitionStageWeight(stage.Key)
		totalWeight += weight
		weightedProgress += weight * max(0, min(1, stage.Progress))
	}
	if totalWeight == 0 {
		return 0
	}
	return max(0, min(1, weightedProgress/totalWeight))
}

func acquisitionStageWeight(key string) float64 {
	switch key {
	case "source":
		return acquisitionSourceWeight
	case "download":
		return acquisitionDownloadWeight
	case "mapping":
		return acquisitionMappingWeight
	case "transcode":
		return acquisitionTranscodeWeight
	case "subtitle":
		return acquisitionSubtitleWeight
	case "rename":
		return acquisitionRenameWeight
	case "organize":
		return acquisitionOrganizeWeight
	case "review":
		return acquisitionReviewWeight
	case "import":
		return acquisitionImportWeight
	default:
		return 0
	}
}

func acquisitionAggregateStatus(view domain.AcquisitionView, downloadStatus string) string {
	if (downloadStatus == "failed" && !acquisitionWaitsForMapping(view)) || acquisitionStageStatus(view.Stages, stageFailed) {
		return "failed"
	}
	if acquisitionStageKeyStatus(view.Stages, "review") == stageRejected || acquisitionStageKeyStatus(view.Stages, "import") == stageRejected {
		return "rejected"
	}
	if downloadStatus == "cancelled" || acquisitionStageStatus(view.Stages, stageCancelled) {
		return "cancelled"
	}
	if acquisitionStageKeyStatus(view.Stages, "import") == stageCompleted {
		return "completed"
	}
	switch view.CurrentStage {
	case "source":
		return "pending"
	case "download":
		if downloadStatus == "" {
			return "pending"
		}
		return "downloading"
	case "mapping":
		return "mapping_pending"
	case "transcode", "subtitle", "rename", "organize":
		if downloadStatus == "completed" || downloadStatus == "selecting_files" {
			return "materializing"
		}
		return "processing"
	case "review":
		return "awaiting_review"
	case "import":
		return "importing"
	default:
		return "processing"
	}
}

var mappingMaterializationErrorCodes = map[string]struct{}{
	"mapping_profile_required":    {},
	"episode_mapping_required":    {},
	"mapping_source_invalid":      {},
	"mapping_source_out_of_range": {},
	"mapping_context_incomplete":  {},
	"mapping_target_out_of_range": {},
	"mapping_title_missing":       {},
}

func isMappingMaterializationFailure(status, failureStage, errorCode string) bool {
	if status != "failed" || failureStage != "materialize" {
		return false
	}
	_, ok := mappingMaterializationErrorCodes[errorCode]
	return ok
}

func acquisitionWaitsForMapping(view domain.AcquisitionView) bool {
	return view.MediaType == domain.TaskMediaEpisode && !view.Mapping.Complete && view.Download != nil &&
		isMappingMaterializationFailure(view.Download.Status, view.Download.FailureStage, view.Download.ErrorCode)
}

func acquisitionStageStatus(stages []domain.AcquisitionStageView, status string) bool {
	for _, stage := range stages {
		if stage.Status == status {
			return true
		}
	}
	return false
}

func acquisitionStageKeyStatus(stages []domain.AcquisitionStageView, key string) string {
	for _, stage := range stages {
		if stage.Key == key {
			return stage.Status
		}
	}
	return ""
}

func latestTaskTime(tasks []domain.AcquisitionTaskSummary) *time.Time {
	var latest *time.Time
	for _, task := range tasks {
		latest = laterTimePointer(latest, task.UpdatedAt)
	}
	return latest
}

func laterTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func laterTimePointer(current *time.Time, candidate time.Time) *time.Time {
	if candidate.IsZero() || (current != nil && !candidate.After(*current)) {
		return current
	}
	value := candidate
	return &value
}

func timeValue(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value
	return &copy
}

// --- RSS entries ---

func (service *ReadService) ListRSSEntries(ctx context.Context, subscriptionID uuid.UUID, cursor *uuid.UUID, limit int, status, group, query, rejectReason, sortBy, sortOrder *string) (domain.RSSEntryPage, error) {
	if _, err := service.queries.GetRSSSubscription(ctx, repository.UUIDToPG(subscriptionID)); errors.Is(err, pgx.ErrNoRows) {
		return domain.RSSEntryPage{}, domain.ErrNotFound
	} else if err != nil {
		return domain.RSSEntryPage{}, fmt.Errorf("get rss subscription: %w", err)
	}
	const batchSize = 200
	baseSort := "newest"
	var batchCursor *uuid.UUID
	rows := make([]db.ListRSSEntriesRow, 0, batchSize)
	for {
		params := db.ListRSSEntriesParams{SubscriptionID: repository.UUIDToPG(subscriptionID), RowLimit: batchSize, Status: status, Sort: &baseSort}
		if batchCursor != nil {
			params.Cursor = repository.UUIDToPG(*batchCursor)
		}
		batch, err := service.queries.ListRSSEntries(ctx, params)
		if err != nil {
			return domain.RSSEntryPage{}, fmt.Errorf("list rss entries: %w", err)
		}
		rows = append(rows, batch...)
		if len(batch) < batchSize {
			break
		}
		last := repository.UUIDFromPG(batch[len(batch)-1].ID)
		batchCursor = &last
	}
	acquisitionRows, err := service.queries.ListRSSSubscriptionAcquisitions(ctx, repository.UUIDToPG(subscriptionID))
	if err != nil {
		return domain.RSSEntryPage{}, fmt.Errorf("list RSS entry acquisitions: %w", err)
	}
	acquisitions, err := service.acquisitionViews(ctx, acquisitionRows)
	if err != nil {
		return domain.RSSEntryPage{}, err
	}
	acquisitionsByEntry := make(map[uuid.UUID]domain.AcquisitionView, len(acquisitions))
	for _, acquisition := range acquisitions {
		if acquisition.RSSEntryID != nil {
			acquisitionsByEntry[*acquisition.RSSEntryID] = acquisition
		}
	}
	archivedRows, err := service.queries.ListRSSImportedEntryAcquisitions(ctx, repository.UUIDToPG(subscriptionID))
	if err != nil {
		return domain.RSSEntryPage{}, fmt.Errorf("list archived RSS entry acquisitions: %w", err)
	}
	archivedByEntry := make(map[uuid.UUID]db.ListRSSImportedEntryAcquisitionsRow, len(archivedRows))
	for _, archived := range archivedRows {
		archivedByEntry[repository.UUIDFromPG(archived.EntryID)] = archived
	}

	views := make([]domain.RSSEntryView, 0, len(rows))
	reasonsByEntry := make(map[uuid.UUID][]string, len(rows))
	for _, row := range rows {
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
			reasonsByEntry[view.ID] = row.RejectionReasons
		}
		if acquisition, ok := acquisitionsByEntry[view.ID]; ok {
			acquisitionID := acquisition.ID
			view.AcquisitionID = &acquisitionID
			view.DownloadID = acquisition.DownloadID
			view.AcquisitionProgress = &domain.AcquisitionProgressView{
				AggregateStatus: acquisition.AggregateStatus,
				CurrentStage:    acquisition.CurrentStage,
				OverallProgress: acquisition.OverallProgress,
			}
		} else if archived, ok := archivedByEntry[view.ID]; ok {
			acquisitionID := repository.UUIDFromPG(archived.AcquisitionID)
			view.AcquisitionID = &acquisitionID
			view.AcquisitionProgress = &domain.AcquisitionProgressView{
				AggregateStatus: "completed",
				CurrentStage:    "import",
				OverallProgress: 1,
			}
		}
		views = append(views, view)
	}
	if group != nil {
		filtered := views[:0]
		for _, view := range views {
			if rssEntryMatchesGroup(view, *group) {
				filtered = append(filtered, view)
			}
		}
		views = filtered
	}
	if query != nil && strings.TrimSpace(*query) != "" {
		needle := strings.ToLower(strings.TrimSpace(*query))
		filtered := views[:0]
		for _, view := range views {
			if strings.Contains(strings.ToLower(view.Title), needle) {
				filtered = append(filtered, view)
			}
		}
		views = filtered
	}
	if rejectReason != nil && strings.TrimSpace(*rejectReason) != "" {
		needle := strings.TrimSpace(*rejectReason)
		filtered := views[:0]
		for _, view := range views {
			if rssEntryHasRejectReason(reasonsByEntry[view.ID], needle) {
				filtered = append(filtered, view)
			}
		}
		views = filtered
	}
	direction := listSortDirection(sortOrder, "desc")
	field := "discovered_at"
	if sortBy != nil {
		field = *sortBy
	}
	sort.SliceStable(views, func(i, j int) bool {
		left, right := views[i], views[j]
		comparison := 0
		switch field {
		case "title":
			comparison = strings.Compare(strings.ToLower(left.Title), strings.ToLower(right.Title))
		case "episode":
			comparison = compareOptionalEpisode(left.SourceSeason, left.SourceEpisode, right.SourceSeason, right.SourceEpisode)
		case "progress":
			leftProgress, rightProgress := float64(0), float64(0)
			if left.AcquisitionProgress != nil {
				leftProgress = left.AcquisitionProgress.OverallProgress
			}
			if right.AcquisitionProgress != nil {
				rightProgress = right.AcquisitionProgress.OverallProgress
			}
			comparison = cmp.Compare(leftProgress, rightProgress)
		default:
			comparison = compareTime(left.CreatedAt, right.CreatedAt)
		}
		if comparison == 0 {
			comparison = strings.Compare(left.ID.String(), right.ID.String())
		}
		return direction*comparison < 0
	})
	window, hasMore, err := cursorWindow(views, cursor, limit, func(item domain.RSSEntryView) uuid.UUID { return item.ID })
	if err != nil {
		return domain.RSSEntryPage{}, err
	}
	return domain.RSSEntryPage{
		Items:      window,
		NextCursor: pageCursor(hasMore, len(window), func(i int) uuid.UUID { return window[i].ID }),
	}, nil
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

// rssEntryHasRejectReason 检查条目的完整拒绝原因列表是否包含指定原因。
func rssEntryHasRejectReason(reasons []string, reason string) bool {
	for _, part := range reasons {
		if strings.TrimSpace(part) == reason {
			return true
		}
	}
	return false
}

func compareOptionalEpisode(leftSeason, leftEpisode, rightSeason, rightEpisode *int) int {
	leftSeasonValue, rightSeasonValue := math.MaxInt, math.MaxInt
	leftEpisodeValue, rightEpisodeValue := math.MaxInt, math.MaxInt
	if leftSeason != nil {
		leftSeasonValue = *leftSeason
	}
	if rightSeason != nil {
		rightSeasonValue = *rightSeason
	}
	if comparison := cmp.Compare(leftSeasonValue, rightSeasonValue); comparison != 0 {
		return comparison
	}
	if leftEpisode != nil {
		leftEpisodeValue = *leftEpisode
	}
	if rightEpisode != nil {
		rightEpisodeValue = *rightEpisode
	}
	return cmp.Compare(leftEpisodeValue, rightEpisodeValue)
}

func classifyRSSEntry(row db.ListRSSEntriesRow) string {
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

func rssEntryHasTargetOccupancy(row db.ListRSSEntriesRow) bool {
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

// --- Operations ---

func (service *ReadService) ListOperations(ctx context.Context, cursor *uuid.UUID, limit int, resourceType *string, resourceID *uuid.UUID, status *string) (domain.OperationPage, error) {
	params := db.ListOperationsParams{RowLimit: fetchLimit(limit)}
	if cursor != nil {
		params.Cursor = repository.UUIDToPG(*cursor)
	}
	if resourceType != nil {
		params.ResourceType = resourceType
	}
	if resourceID != nil {
		params.ResourceID = repository.UUIDToPG(*resourceID)
	}
	if status != nil {
		params.Status = status
	}
	rows, err := service.queries.ListOperations(ctx, params)
	if err != nil {
		return domain.OperationPage{}, fmt.Errorf("list operations: %w", err)
	}
	views := operationViews(rows, nil)
	lim := clampLimit(limit)
	hasMore := len(views) > lim
	if hasMore {
		views = views[:lim]
	}
	return domain.OperationPage{
		Items:      views,
		NextCursor: pageCursor(hasMore, len(views), func(i int) uuid.UUID { return views[i].ID }),
	}, nil
}

func (service *ReadService) GetOperation(ctx context.Context, id uuid.UUID) (domain.OperationView, error) {
	row, err := service.queries.GetOperationByID(ctx, repository.UUIDToPG(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.OperationView{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.OperationView{}, fmt.Errorf("get operation: %w", err)
	}
	attempts, err := service.queries.ListOperationAttempts(ctx, row.ID)
	if err != nil {
		return domain.OperationView{}, fmt.Errorf("list operation attempts: %w", err)
	}
	views := operationViews([]db.Operation{row}, attempts)
	return views[0], nil
}

func operationViews(rows []db.Operation, attempts []db.OperationAttempt) []domain.OperationView {
	byOperation := make(map[uuid.UUID][]domain.OperationAttemptView, len(rows))
	for _, attempt := range attempts {
		operationID := repository.UUIDFromPG(attempt.OperationID)
		byOperation[operationID] = append(byOperation[operationID], operationAttemptView(attempt))
	}
	views := make([]domain.OperationView, 0, len(rows))
	for _, row := range rows {
		id := repository.UUIDFromPG(row.ID)
		view := domain.OperationView{
			ID:             id,
			Kind:           row.Kind,
			Status:         row.Status,
			IdempotencyKey: row.IdempotencyKey,
			MaxAttempts:    int(row.MaxAttempts),
			AttemptCount:   int(row.AttemptCount),
			CreatedAt:      row.CreatedAt.Time,
			UpdatedAt:      row.UpdatedAt.Time,
			Attempts:       byOperation[id],
		}
		if view.Attempts == nil {
			view.Attempts = []domain.OperationAttemptView{}
		}
		if row.ResourceType != nil {
			view.ResourceType = *row.ResourceType
		}
		if row.ResourceID.Valid {
			value := repository.UUIDFromPG(row.ResourceID)
			view.ResourceID = &value
			view.ResourceHref = resourceHref(view.ResourceType, value)
		}
		if row.HeartbeatAt.Valid {
			value := row.HeartbeatAt.Time
			view.HeartbeatAt = &value
		}
		if row.ErrorCode != nil {
			view.ErrorCode = *row.ErrorCode
		}
		if row.ErrorMessage != nil {
			view.ErrorMessage = *row.ErrorMessage
		}
		if row.StartedAt.Valid {
			value := row.StartedAt.Time
			view.StartedAt = &value
		}
		if row.FinishedAt.Valid {
			value := row.FinishedAt.Time
			view.FinishedAt = &value
		}
		views = append(views, view)
	}
	return views
}

func resourceHref(resourceType string, resourceID uuid.UUID) string {
	switch resourceType {
	case "agent_resolution":
		return "/agent/resolutions/" + resourceID.String()
	case "download":
		return "/downloads/" + resourceID.String()
	case "episode_task":
		return "/tasks/" + resourceID.String()
	case "acquisition":
		return "/acquisitions/" + resourceID.String()
	case "rss_subscription":
		return "/rss/" + resourceID.String()
	case "search", "search_run":
		return "/searches/" + resourceID.String()
	case "emby_scan":
		return "/emby/scans/" + resourceID.String()
	case "emby_catalog":
		return "/emby"
	default:
		return ""
	}
}

func operationAttemptView(row db.OperationAttempt) domain.OperationAttemptView {
	view := domain.OperationAttemptView{
		ID:        repository.UUIDFromPG(row.ID),
		Attempt:   int(row.Attempt),
		Status:    row.Status,
		StartedAt: row.StartedAt.Time,
	}
	if row.WorkerID != nil {
		view.WorkerID = *row.WorkerID
	}
	if row.ErrorCode != nil {
		view.ErrorCode = *row.ErrorCode
	}
	if row.ErrorMessage != nil {
		view.ErrorMessage = *row.ErrorMessage
	}
	if row.HeartbeatAt.Valid {
		value := row.HeartbeatAt.Time
		view.HeartbeatAt = &value
	}
	if row.FinishedAt.Valid {
		value := row.FinishedAt.Time
		view.FinishedAt = &value
	}
	return view
}

// --- Events ---

func (service *ReadService) ListResourceEvents(ctx context.Context, resourceType string, resourceID uuid.UUID, cursor *uuid.UUID, limit int) (domain.EventRecordPage, error) {
	params := db.ListResourceEventsParams{
		ResourceType: &resourceType,
		ResourceID:   repository.UUIDToPG(resourceID),
		RowLimit:     fetchLimit(limit),
	}
	if cursor != nil {
		params.Cursor = repository.UUIDToPG(*cursor)
	}
	rows, err := service.queries.ListResourceEvents(ctx, params)
	if err != nil {
		return domain.EventRecordPage{}, fmt.Errorf("list resource events: %w", err)
	}
	views := make([]domain.EventRecordView, 0, len(rows))
	for _, row := range rows {
		view := domain.EventRecordView{
			ID:         repository.UUIDFromPG(row.ID),
			Topic:      row.Topic,
			Data:       row.Data,
			OccurredAt: row.OccurredAt.Time,
		}
		if row.ResourceType != nil {
			view.ResourceType = *row.ResourceType
		}
		if row.ResourceID.Valid {
			value := repository.UUIDFromPG(row.ResourceID)
			view.ResourceID = &value
		}
		if row.OperationID.Valid {
			value := repository.UUIDFromPG(row.OperationID)
			view.OperationID = &value
		}
		views = append(views, view)
	}
	lim := clampLimit(limit)
	hasMore := len(views) > lim
	if hasMore {
		views = views[:lim]
	}
	return domain.EventRecordPage{
		Items:      views,
		NextCursor: pageCursor(hasMore, len(views), func(i int) uuid.UUID { return views[i].ID }),
	}, nil
}

// --- Dashboard ---

func (service *ReadService) DashboardSummary(ctx context.Context) (domain.DashboardSummary, error) {
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
	attentionPhase := "attention"
	attention, err := service.listAcquisitionViews(ctx, nil, nil, &attentionPhase)
	if err != nil {
		return domain.DashboardSummary{}, fmt.Errorf("list dashboard attention items: %w", err)
	}
	sortAcquisitionViews(attention, "updated_at", -1)
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
			Attention:      len(attention),
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
		AttentionItems:   make([]domain.DashboardAttentionItem, 0, min(5, len(attention))),
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
	for _, view := range attention[:min(5, len(attention))] {
		if item, ok := dashboardAttentionItem(view); ok {
			summary.AttentionItems = append(summary.AttentionItems, item)
		}
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

func saturatingInt(value int64) int {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(value)
}

var _ = time.Now
