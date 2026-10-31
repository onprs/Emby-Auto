package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func (service *ReadService) ListDownloads(ctx context.Context, cursor *uuid.UUID, limit int, status, phase, query, sortBy, sortOrder *string) (domain.DownloadPage, error) {
	var result domain.DownloadPage
	err := service.withListSnapshot(ctx, "", func(reader *ReadService) error {
		ids, err := reader.queries.ListDownloadPageIDs(ctx, db.ListDownloadPageIDsParams{Status: status, Phase: phase, Query: query, SortBy: valueOrEmpty(sortBy), DescendingOrder: listSortDirection(sortOrder, "desc") < 0, Cursor: listCursorValue(cursor), RowLimit: fetchLimit(limit)})
		if err != nil {
			return fmt.Errorf("select download page: %w", err)
		}
		ids, next, err := listPageWindow(ids, limit, cursor)
		if err != nil {
			return err
		}
		rows, err := reader.queries.ListDownloadsByIDs(ctx, ids)
		if err != nil {
			return fmt.Errorf("load download page: %w", err)
		}
		rows, err = orderedListRows(ids, rows, func(row db.Download) pgtype.UUID { return row.ID })
		if err != nil {
			return err
		}
		views, err := reader.downloadViews(ctx, rows)
		if err != nil {
			return err
		}
		result = domain.DownloadPage{Items: views, NextCursor: next}
		return nil
	})
	return result, err
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
	if len(rows) == 0 {
		return views, nil
	}
	ids := make([]pgtype.UUID, len(rows))
	for index, row := range rows {
		ids[index] = row.ID
	}
	files, err := service.queries.ListDownloadFilesByDownloadIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list download files: %w", err)
	}
	filesByDownload := make(map[pgtype.UUID][]db.DownloadFile, len(rows))
	for _, file := range files {
		filesByDownload[file.DownloadID] = append(filesByDownload[file.DownloadID], file)
	}
	for _, row := range rows {
		views = append(views, downloadViewFromDB(row, filesByDownload[row.ID]))
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
			CanRetry: (row.Status == "cancelled" || (row.Status == "failed" && row.FailureStage != nil &&
				!isMappingMaterializationFailure(row.Status, valueOrEmpty(row.FailureStage), valueOrEmpty(row.ErrorCode)) && valueOrEmpty(row.ErrorCode) != "download_manifest_changed")) &&
				!row.DeletionRequestedAt.Valid && !row.DeletedAt.Valid,
			CanCancel:            (row.Status == "enqueue_pending" || row.Status == "file_resolution_pending" || row.Status == "downloading" || row.Status == "completed" || row.Status == "selecting_files" || row.Status == "failed") && !row.DeletionRequestedAt.Valid && !row.DeletedAt.Valid,
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
	resolutionSource := stringValue(row.FileResolutionSource)
	for _, file := range files {
		item := domain.DownloadFileView{
			ID: repository.UUIDFromPG(file.ID), FileIndex: int(file.FileIndex),
			RelativePath: file.RelativePath, SizeBytes: file.SizeBytes,
			MediaKind: file.MediaKind, Selected: file.Selected,
		}
		if coordinate, ok := effectiveDownloadFileSourceCoordinate(file, resolutionSource); ok {
			season, episode := coordinate.Season, coordinate.Episode
			item.SourceSeason = &season
			item.SourceEpisode = &episode
			item.SourceEpisodeFractionHundredths = coordinate.EpisodeFractionHundredths
		}
		if file.Language != nil {
			item.Language = *file.Language
		}
		item.ExclusionReason = downloadFileExclusionReason(file, files, resolutionSource)
		view.Files = append(view.Files, item)
	}
	return view
}

func effectiveDownloadFileSourceCoordinate(file db.DownloadFile, resolutionSource string) (domain.EpisodeCoordinate, bool) {
	resolved := resolveSourceCoordinate(
		file.RelativePath, file.SourceSeason, file.SourceEpisode,
		file.SourceEpisodeFractionHundredths, resolutionSource,
	)
	return resolved.coordinate, resolved.valid
}

func downloadFileExclusionReason(file db.DownloadFile, files []db.DownloadFile, resolutionSource string) string {
	if file.Selected {
		return ""
	}
	switch file.MediaKind {
	case "extra":
		return "extra_content"
	case "unknown", "other":
		return "unsupported_media"
	case "video", "subtitle":
		if _, ok := effectiveDownloadFileSourceCoordinate(file, resolutionSource); !ok {
			return "episode_not_detected"
		}
	}
	coordinate, coordinateOK := effectiveDownloadFileSourceCoordinate(file, resolutionSource)
	for _, selected := range files {
		if !selected.Selected || selected.MediaKind != file.MediaKind {
			continue
		}
		selectedCoordinate, selectedCoordinateOK := effectiveDownloadFileSourceCoordinate(selected, resolutionSource)
		if coordinateOK && selectedCoordinateOK && selectedCoordinate == coordinate {
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
