package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type EmbyCatalogWorkflow struct {
	queries    *db.Queries
	transactor *database.Transactor
	operations *OperationScheduler
}

func NewEmbyCatalogWorkflow(
	queries *db.Queries,
	transactor *database.Transactor,
	operations *OperationScheduler,
) *EmbyCatalogWorkflow {
	return &EmbyCatalogWorkflow{queries: queries, transactor: transactor, operations: operations}
}

func (workflow *EmbyCatalogWorkflow) ScheduleRefresh(ctx context.Context, input domain.CreateEmbyRefresh) (domain.Operation, error) {
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" || len(key) > 256 {
		return domain.Operation{}, invalidEmbyCatalog("idempotencyKey", "must contain between 1 and 256 characters")
	}
	if input.ActorUserID == uuid.Nil {
		return domain.Operation{}, invalidEmbyCatalog("actorUserId", "must be present")
	}
	result, err := workflow.operations.Schedule(ctx, ScheduleOperationRequest{
		Kind: appqueue.KindEmbyRefresh, ResourceType: "emby_catalog", ResourceID: deterministicResourceID("emby.catalog"),
		IdempotencyKey: "emby.refresh:manual:" + input.ActorUserID.String() + ":" + key,
		MaxAttempts:    5, Timeout: 2 * time.Minute, Payload: map[string]any{"source": "manual"}, ActorUserID: input.ActorUserID,
	})
	if err != nil {
		return domain.Operation{}, embyCatalogError("schedule Emby refresh", err)
	}
	return result.Operation, nil
}

func (workflow *EmbyCatalogWorkflow) ScheduleScan(
	ctx context.Context,
	input domain.CreateEmbyScan,
) (domain.EmbyScanCommandResult, error) {
	key := strings.TrimSpace(input.IdempotencyKey)
	if key == "" || len(key) > 256 {
		return domain.EmbyScanCommandResult{}, invalidEmbyCatalog("idempotencyKey", "must contain between 1 and 256 characters")
	}
	if input.ActorUserID == uuid.Nil {
		return domain.EmbyScanCommandResult{}, invalidEmbyCatalog("actorUserId", "must be present")
	}
	commandKey := "emby.scan:" + input.ActorUserID.String() + ":" + key
	scanID := deterministicResourceID(commandKey)
	result := domain.EmbyScanCommandResult{}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindEmbyScan,
			ResourceType:   "emby_scan",
			ResourceID:     scanID,
			IdempotencyKey: commandKey,
			MaxAttempts:    4,
			Timeout:        30 * time.Minute,
			Payload:        map[string]any{},
			ActorUserID:    input.ActorUserID,
		})
		if err != nil {
			return fmt.Errorf("schedule Emby scan: %w", err)
		}
		scan, err := scope.Queries.CreateEmbyScanRun(ctx, db.CreateEmbyScanRunParams{
			ID:          repository.UUIDToPG(scanID),
			OperationID: repository.UUIDToPG(scheduled.Operation.ID),
			CreatedBy:   repository.UUIDToPG(input.ActorUserID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			scan, err = scope.Queries.GetEmbyScanRun(ctx, repository.UUIDToPG(scanID))
		}
		if err != nil {
			return fmt.Errorf("create Emby scan run: %w", err)
		}
		if repository.UUIDFromPG(scan.OperationID) != scheduled.Operation.ID {
			return idempotencyConflict(key)
		}
		result = domain.EmbyScanCommandResult{Scan: embyScanFromDB(scan), Operation: scheduled.Operation}
		return nil
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "emby_scan_runs_one_active" {
			return domain.EmbyScanCommandResult{}, NewError("emby_scan_active", "another Emby catalog scan is already active", ErrStateConflict, nil)
		}
		return domain.EmbyScanCommandResult{}, embyCatalogError("schedule Emby catalog scan", err)
	}
	return result, nil
}

func (workflow *EmbyCatalogWorkflow) BeginScan(
	ctx context.Context,
	operation domain.Operation,
) (domain.EmbyScan, error) {
	if operation.ResourceType != "emby_scan" || operation.ResourceID == uuid.Nil {
		return domain.EmbyScan{}, fmt.Errorf("emby scan operation is invalid")
	}
	scan, err := workflow.queries.GetEmbyScanRunByOperation(ctx, repository.UUIDToPG(operation.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EmbyScan{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.EmbyScan{}, fmt.Errorf("load Emby scan run: %w", err)
	}
	if repository.UUIDFromPG(scan.ID) != operation.ResourceID {
		return domain.EmbyScan{}, fmt.Errorf("emby scan operation resource does not match scan run")
	}
	if scan.Status == string(domain.EmbyScanSucceeded) || scan.Status == string(domain.EmbyScanFailed) || scan.Status == string(domain.EmbyScanCancelled) {
		return embyScanFromDB(scan), nil
	}
	started, err := workflow.queries.StartEmbyScanRun(ctx, scan.ID)
	if err != nil {
		return domain.EmbyScan{}, fmt.Errorf("start Emby scan run: %w", err)
	}
	return embyScanFromDB(started), nil
}

func (workflow *EmbyCatalogWorkflow) CompleteScan(
	ctx context.Context,
	operation domain.Operation,
	snapshots []domain.EmbyLibrarySnapshot,
) error {
	if operation.ResourceType != "emby_scan" || operation.ResourceID == uuid.Nil {
		return fmt.Errorf("emby scan operation is invalid")
	}
	if len(snapshots) > math.MaxInt32 {
		return fmt.Errorf("emby library count exceeds database range")
	}
	seenLibraries := map[string]struct{}{}
	seenItems := map[string]struct{}{}
	itemCount := 0
	for _, snapshot := range snapshots {
		library := snapshot.Library
		if strings.TrimSpace(library.EmbyID) == "" || strings.TrimSpace(library.Name) == "" || !json.Valid(library.Payload) {
			return fmt.Errorf("emby catalog contains an incomplete library")
		}
		if _, duplicate := seenLibraries[library.EmbyID]; duplicate {
			return fmt.Errorf("emby catalog contains duplicate library ID %q", library.EmbyID)
		}
		seenLibraries[library.EmbyID] = struct{}{}
		for _, item := range snapshot.Items {
			if strings.TrimSpace(item.EmbyID) == "" || strings.TrimSpace(item.Name) == "" ||
				(item.ItemType != "Series" && item.ItemType != "Season" && item.ItemType != "Episode" && item.ItemType != "Movie") || !json.Valid(item.Payload) {
				return fmt.Errorf("emby catalog contains an incomplete item")
			}
			if _, duplicate := seenItems[item.EmbyID]; duplicate {
				return fmt.Errorf("emby catalog contains duplicate item ID %q", item.EmbyID)
			}
			seenItems[item.EmbyID] = struct{}{}
			itemCount++
			if itemCount > math.MaxInt32 {
				return fmt.Errorf("emby item count exceeds database range")
			}
		}
	}

	now := time.Now().UTC()
	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		scan, err := scope.Queries.GetEmbyScanRunByOperation(ctx, repository.UUIDToPG(operation.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load Emby scan for completion: %w", err)
		}
		if repository.UUIDFromPG(scan.ID) != operation.ResourceID {
			return fmt.Errorf("emby scan operation resource does not match scan run")
		}
		if scan.Status == string(domain.EmbyScanSucceeded) {
			return nil
		}
		if scan.Status != string(domain.EmbyScanRunning) {
			return fmt.Errorf("emby scan cannot complete from status %q", scan.Status)
		}
		for _, snapshot := range snapshots {
			libraryCatalog := snapshot.Library
			locations := make([]string, 0, len(libraryCatalog.Locations))
			for _, location := range libraryCatalog.Locations {
				if normalized := strings.TrimSpace(location); normalized != "" {
					locations = append(locations, normalized)
				}
			}
			library, err := scope.Queries.UpsertEmbyLibrary(ctx, db.UpsertEmbyLibraryParams{
				ID:              repository.UUIDToPG(deterministicResourceID("emby.library:" + libraryCatalog.EmbyID)),
				EmbyID:          libraryCatalog.EmbyID,
				Name:            libraryCatalog.Name,
				CollectionType:  optionalString(libraryCatalog.CollectionType),
				Locations:       locations,
				LastScanRunID:   scan.ID,
				LastSeenAt:      pgtype.Timestamptz{Time: now, Valid: true},
				UpstreamPayload: libraryCatalog.Payload,
			})
			if err != nil {
				return fmt.Errorf("persist Emby library %q: %w", libraryCatalog.EmbyID, err)
			}
			for _, item := range snapshot.Items {
				providerIDs, err := json.Marshal(item.ProviderIDs)
				if err != nil {
					return fmt.Errorf("encode Emby provider IDs: %w", err)
				}
				seasonNumber := (*int32)(nil)
				if item.SeasonNumber != nil {
					value := int32(*item.SeasonNumber)
					seasonNumber = &value
				}
				episodeNumber := (*int32)(nil)
				if item.EpisodeNumber != nil {
					value := int32(*item.EpisodeNumber)
					episodeNumber = &value
				}
				if _, err := scope.Queries.UpsertEmbyLibraryItem(ctx, db.UpsertEmbyLibraryItemParams{
					ID:              repository.UUIDToPG(deterministicResourceID("emby.item:" + item.EmbyID)),
					EmbyID:          item.EmbyID,
					LibraryID:       library.ID,
					ParentEmbyID:    optionalString(item.ParentEmbyID),
					ItemType:        item.ItemType,
					Name:            item.Name,
					FilePath:        optionalString(item.Path),
					ProviderIds:     providerIDs,
					SeasonNumber:    seasonNumber,
					EpisodeNumber:   episodeNumber,
					LastScanRunID:   scan.ID,
					LastSeenAt:      pgtype.Timestamptz{Time: now, Valid: true},
					UpstreamPayload: item.Payload,
				}); err != nil {
					return fmt.Errorf("persist Emby item %q: %w", item.EmbyID, err)
				}
			}
		}
		if _, err := scope.Queries.MarkEmbyLibraryItemsAbsent(ctx, scan.ID); err != nil {
			return fmt.Errorf("mark missing Emby items absent: %w", err)
		}
		if _, err := scope.Queries.MarkEmbyLibrariesAbsent(ctx, scan.ID); err != nil {
			return fmt.Errorf("mark missing Emby libraries absent: %w", err)
		}
		completed, err := scope.Queries.CompleteEmbyScanRun(ctx, db.CompleteEmbyScanRunParams{
			LibraryCount: int32(len(snapshots)),
			ItemCount:    int32(itemCount),
			ID:           scan.ID,
		})
		if err != nil {
			return fmt.Errorf("complete Emby scan run: %w", err)
		}
		return appendCatalogEvent(ctx, scope.Queries, "emby.scan_completed", "emby_scan", completed.ID, repository.UUIDToPG(operation.ID), uuid.Nil, map[string]any{
			"libraryCount": len(snapshots),
			"itemCount":    itemCount,
		})
	})
}

func (workflow *EmbyCatalogWorkflow) GetScan(ctx context.Context, id uuid.UUID) (domain.EmbyScan, error) {
	row, err := workflow.queries.GetEmbyScanRun(ctx, repository.UUIDToPG(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EmbyScan{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.EmbyScan{}, fmt.Errorf("get Emby scan run: %w", err)
	}
	return embyScanFromDB(row), nil
}

func (workflow *EmbyCatalogWorkflow) ListScans(
	ctx context.Context,
	cursor *uuid.UUID,
	limit int,
) (domain.EmbyScanPage, error) {
	if limit <= 0 || limit > 100 {
		return domain.EmbyScanPage{}, invalidEmbyCatalog("limit", "must be between 1 and 100")
	}
	cursorValue := pgtype.UUID{}
	if cursor != nil {
		if _, err := workflow.queries.GetEmbyScanRun(ctx, repository.UUIDToPG(*cursor)); errors.Is(err, pgx.ErrNoRows) {
			return domain.EmbyScanPage{}, domain.ErrNotFound
		} else if err != nil {
			return domain.EmbyScanPage{}, fmt.Errorf("validate Emby scan cursor: %w", err)
		}
		cursorValue = repository.UUIDToPG(*cursor)
	}
	rows, err := workflow.queries.ListEmbyScanRuns(ctx, db.ListEmbyScanRunsParams{Cursor: cursorValue, PageSize: int32(limit + 1)})
	if err != nil {
		return domain.EmbyScanPage{}, fmt.Errorf("list Emby scan runs: %w", err)
	}
	page := domain.EmbyScanPage{Items: make([]domain.EmbyScan, 0, min(len(rows), limit))}
	for index, row := range rows {
		if index == limit {
			value := page.Items[len(page.Items)-1].ID
			page.NextCursor = &value
			break
		}
		page.Items = append(page.Items, embyScanFromDB(row))
	}
	return page, nil
}

func (workflow *EmbyCatalogWorkflow) ListLibraries(ctx context.Context) ([]domain.EmbyLibrary, error) {
	rows, err := workflow.queries.ListEmbyLibraries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Emby libraries: %w", err)
	}
	libraries := make([]domain.EmbyLibrary, 0, len(rows))
	for _, row := range rows {
		libraries = append(libraries, embyLibraryFromDB(row))
	}
	return libraries, nil
}

func (workflow *EmbyCatalogWorkflow) ListLibraryItems(
	ctx context.Context,
	libraryID uuid.UUID,
	filter domain.EmbyLibraryItemFilter,
	cursor *uuid.UUID,
	limit int,
) (domain.EmbyLibraryItemPage, error) {
	if libraryID == uuid.Nil {
		return domain.EmbyLibraryItemPage{}, invalidEmbyCatalog("libraryId", "must be present")
	}
	if filter.ItemType != "" && filter.ItemType != "Series" && filter.ItemType != "Season" && filter.ItemType != "Episode" && filter.ItemType != "Movie" {
		return domain.EmbyLibraryItemPage{}, invalidEmbyCatalog("itemType", "must be Series, Season, Episode, or Movie")
	}
	if len(filter.Name) > 256 {
		return domain.EmbyLibraryItemPage{}, invalidEmbyCatalog("name", "must not exceed 256 characters")
	}
	if len(filter.ProviderID) > 256 {
		return domain.EmbyLibraryItemPage{}, invalidEmbyCatalog("providerId", "must not exceed 256 characters")
	}
	if limit <= 0 || limit > 200 {
		return domain.EmbyLibraryItemPage{}, invalidEmbyCatalog("limit", "must be between 1 and 200")
	}
	if _, err := workflow.queries.GetEmbyLibrary(ctx, repository.UUIDToPG(libraryID)); errors.Is(err, pgx.ErrNoRows) {
		return domain.EmbyLibraryItemPage{}, domain.ErrNotFound
	} else if err != nil {
		return domain.EmbyLibraryItemPage{}, fmt.Errorf("load Emby library: %w", err)
	}
	cursorValue := pgtype.UUID{}
	if cursor != nil {
		cursorValue = repository.UUIDToPG(*cursor)
	}
	rows, err := workflow.queries.ListEmbyLibraryItems(ctx, db.ListEmbyLibraryItemsParams{
		LibraryID:  repository.UUIDToPG(libraryID),
		ItemType:   optionalString(strings.TrimSpace(filter.ItemType)),
		Name:       optionalString(strings.TrimSpace(filter.Name)),
		Present:    filter.Present,
		ProviderID: optionalString(strings.TrimSpace(filter.ProviderID)),
		Cursor:     cursorValue,
		PageSize:   int32(limit + 1),
	})
	if err != nil {
		return domain.EmbyLibraryItemPage{}, fmt.Errorf("list Emby library items: %w", err)
	}
	page := domain.EmbyLibraryItemPage{Items: make([]domain.EmbyLibraryItem, 0, min(len(rows), limit))}
	for index, row := range rows {
		if index == limit {
			value := page.Items[len(page.Items)-1].ID
			page.NextCursor = &value
			break
		}
		item, err := embyLibraryItemFromDB(row)
		if err != nil {
			return domain.EmbyLibraryItemPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

func embyScanFromDB(row db.EmbyScanRun) domain.EmbyScan {
	return domain.EmbyScan{
		ID:           repository.UUIDFromPG(row.ID),
		OperationID:  repository.UUIDFromPG(row.OperationID),
		Status:       domain.EmbyScanStatus(row.Status),
		LibraryCount: int(row.LibraryCount),
		ItemCount:    int(row.ItemCount),
		ErrorCode:    stringValue(row.ErrorCode),
		ErrorMessage: stringValue(row.ErrorMessage),
		StartedAt:    timePointer(row.StartedAt),
		CompletedAt:  timePointer(row.CompletedAt),
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
	}
}

func embyLibraryFromDB(row db.EmbyLibrary) domain.EmbyLibrary {
	return domain.EmbyLibrary{
		ID:             repository.UUIDFromPG(row.ID),
		EmbyID:         row.EmbyID,
		Name:           row.Name,
		CollectionType: stringValue(row.CollectionType),
		Locations:      append([]string(nil), row.Locations...),
		Present:        row.Present,
		LastSeenAt:     row.LastSeenAt.Time,
	}
}

func embyLibraryItemFromDB(row db.ListEmbyLibraryItemsRow) (domain.EmbyLibraryItem, error) {
	providerIDs := map[string]string{}
	if err := json.Unmarshal(row.ProviderIds, &providerIDs); err != nil {
		return domain.EmbyLibraryItem{}, fmt.Errorf("decode Emby provider IDs: %w", err)
	}
	item := domain.EmbyLibraryItem{
		ID:            repository.UUIDFromPG(row.ID),
		EmbyID:        row.EmbyID,
		LibraryID:     repository.UUIDFromPG(row.LibraryID),
		ParentEmbyID:  stringValue(row.ParentEmbyID),
		ItemType:      row.ItemType,
		Name:          row.Name,
		Path:          stringValue(row.FilePath),
		ProviderIDs:   providerIDs,
		SeasonNumber:  intPointer(row.SeasonNumber),
		EpisodeNumber: intPointer(row.EpisodeNumber),
		Present:       row.Present,
		LastSeenAt:    row.LastSeenAt.Time,
	}
	if row.ImportedTaskID.Valid {
		value := repository.UUIDFromPG(row.ImportedTaskID)
		item.ImportedTaskID = &value
	}
	return item, nil
}

func timePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func intPointer(value *int32) *int {
	if value == nil {
		return nil
	}
	result := int(*value)
	return &result
}

func invalidEmbyCatalog(field, reason string) error {
	return NewError("invalid_request", "Emby catalog request is invalid", ErrInvalidInput, map[string]any{"field": field, "reason": reason})
}

func embyCatalogError(action string, err error) error {
	var serviceErr *Error
	if errors.As(err, &serviceErr) || errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return NewError("service_unavailable", "Emby catalog storage is unavailable", fmt.Errorf("%s: %w", action, err), map[string]any{"dependency": "postgresql"})
}
