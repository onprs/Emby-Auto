package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func (service *ReadService) ListAcquisitions(ctx context.Context, cursor *uuid.UUID, limit int, sourceKind *string, tmdbSeriesID *int64, phase, sortBy, sortOrder *string) (domain.AcquisitionPage, error) {
	var result domain.AcquisitionPage
	err := service.withListSnapshot(ctx, acquisitionListProjection, func(reader *ReadService) error {
		ids, err := reader.queries.ListAcquisitionPageIDs(ctx, db.ListAcquisitionPageIDsParams{SourceKind: sourceKind, TmdbSeriesID: tmdbSeriesID, Phase: phase, SortBy: valueOrEmpty(sortBy), DescendingOrder: listSortDirection(sortOrder, "desc") < 0, Cursor: listCursorValue(cursor), RowLimit: fetchLimit(limit)})
		if err != nil {
			return fmt.Errorf("select acquisition page: %w", err)
		}
		ids, next, err := listPageWindow(ids, limit, cursor)
		if err != nil {
			return err
		}
		rows, err := reader.queries.ListAcquisitionsByIDs(ctx, ids)
		if err != nil {
			return fmt.Errorf("load acquisition page: %w", err)
		}
		rows, err = orderedListRows(ids, rows, func(row db.Acquisition) pgtype.UUID { return row.ID })
		if err != nil {
			return err
		}
		views, err := reader.acquisitionViews(ctx, rows)
		if err != nil {
			return err
		}
		result = domain.AcquisitionPage{Items: views, NextCursor: next}
		return nil
	})
	return result, err
}

func acquisitionSortTitle(view domain.AcquisitionView) string {
	if view.MediaType == domain.TaskMediaMovie {
		return view.MovieTitle
	}
	return view.SeriesTitle
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
	return acquisitionViewsWithQueries(ctx, service.queries, rows, true)
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
