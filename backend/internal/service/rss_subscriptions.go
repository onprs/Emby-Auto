package service

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

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

func (workflow *RSSWorkflow) CreateSubscription(
	ctx context.Context,
	input domain.CreateRSSSubscription,
) (domain.RSSSubscription, error) {
	includeKeywords, err := normalizeRSSKeywords("includeKeywords", input.IncludeKeywords)
	if err != nil {
		return domain.RSSSubscription{}, err
	}
	excludeKeywords, err := normalizeRSSKeywords("excludeKeywords", input.ExcludeKeywords)
	if err != nil {
		return domain.RSSSubscription{}, err
	}
	input.IncludeKeywords = includeKeywords
	input.ExcludeKeywords = excludeKeywords
	if err := validateRSSSubscription(input.TMDbSeriesID, input.SeriesTitle, input.Name, input.FeedURL, input.SourceSeason, input.PollInterval); err != nil {
		return domain.RSSSubscription{}, err
	}
	subscriptionID := uuid.New()
	var result domain.RSSSubscription
	err = workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		tmdbID := input.TMDbSeriesID
		series, err := scope.Queries.UpsertRSSMediaSeries(ctx, db.UpsertRSSMediaSeriesParams{
			ID:           repository.UUIDToPG(uuid.New()),
			TmdbSeriesID: &tmdbID,
			Title:        strings.TrimSpace(input.SeriesTitle),
		})
		if err != nil {
			return fmt.Errorf("upsert RSS media series: %w", err)
		}
		mappingProfile, err := resolveRSSMappingProfile(ctx, scope.Queries, series.ID, input.SourceSeason, input.MappingProfileID)
		if err != nil {
			return err
		}
		row, err := scope.Queries.CreateRSSSubscription(ctx, db.CreateRSSSubscriptionParams{
			ID:                        repository.UUIDToPG(subscriptionID),
			SeriesID:                  series.ID,
			MappingProfileID:          nullableUUID(mappingProfile.ID),
			Name:                      strings.TrimSpace(input.Name),
			FeedUrl:                   strings.TrimSpace(input.FeedURL),
			IncludeKeywords:           input.IncludeKeywords,
			ExcludeKeywords:           input.ExcludeKeywords,
			Enabled:                   input.Enabled,
			AutoEpisodeMapping:        input.AutoEpisodeMapping,
			AutoReview:                input.AutoReview,
			CleanupSourceOnCompletion: input.CleanupSourceOnCompletion,
			PollIntervalSeconds:       int32(input.PollInterval / time.Second),
			SourceSeason:              int32(input.SourceSeason),
		})
		if err != nil {
			return fmt.Errorf("create RSS subscription: %w", err)
		}
		result = subscriptionFromRow(row, series.Title, input.TMDbSeriesID)
		if _, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindTMDbSync,
			ResourceType:   "media_series",
			ResourceID:     repository.UUIDFromPG(series.ID),
			IdempotencyKey: "tmdb.sync:rss-subscription:" + subscriptionID.String(),
			MaxAttempts:    4,
			Timeout:        10 * time.Minute,
			Payload:        map[string]any{"tmdbSeriesId": input.TMDbSeriesID},
			ActorUserID:    input.ActorUserID,
		}); err != nil {
			return fmt.Errorf("schedule RSS TMDb sync: %w", err)
		}
		eventData := map[string]any{
			"version":                   result.Version,
			"enabled":                   result.Enabled,
			"includeKeywords":           result.IncludeKeywords,
			"excludeKeywords":           result.ExcludeKeywords,
			"autoEpisodeMapping":        result.AutoEpisodeMapping,
			"autoReview":                result.AutoReview,
			"cleanupSourceOnCompletion": result.CleanupSourceOnCompletion,
		}
		if mappingProfile.ID != uuid.Nil {
			eventData["mappingProfileId"] = mappingProfile.ID
			eventData["mappingProfileAutoSelected"] = mappingProfile.AutoSelected
		}
		if err := appendRSSSubscriptionCommandEvent(ctx, scope.Queries, result.ID, input.ActorUserID, "rss.subscription.created", eventData); err != nil {
			return err
		}
		if result.Enabled {
			if err := workflow.scheduleContinuousPoll(ctx, scope, result); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.RSSSubscription{}, rssCommandError("create RSS subscription", err)
	}
	return result, nil
}

func (workflow *RSSWorkflow) ListSubscriptions(
	ctx context.Context,
	cursor *uuid.UUID,
	limit int,
	sortBy *string,
	sortOrder *string,
) (domain.RSSSubscriptionPage, error) {
	if limit <= 0 || limit > 100 {
		return domain.RSSSubscriptionPage{}, invalidRSSSubscription("limit", "must be between 1 and 100")
	}
	field := "name"
	if sortBy != nil {
		field = *sortBy
	}
	direction := listSortDirection(sortOrder, "asc")
	// progress 依赖实时聚合的进度值，无法在 SQL 层精确复刻排序键，
	// 保留全量加载 + 内存排序路径以维持既有排序语义；其余稳定字段
	// 走 SQL 层 cursor 分页，只加载并计算当前页订阅的进度。
	if field == "progress" {
		return workflow.listSubscriptionsByComputedProgress(ctx, cursor, limit, direction)
	}
	return workflow.listSubscriptionsBySQLSort(ctx, cursor, limit, field, direction)
}

// listSubscriptionsBySQLSort 在 SQL 层按稳定排序键分页，仅对当前页订阅
// 计算进度，避免与 limit 无关地扫描全表。
func (workflow *RSSWorkflow) listSubscriptionsBySQLSort(
	ctx context.Context,
	cursor *uuid.UUID,
	limit int,
	field string,
	direction int,
) (domain.RSSSubscriptionPage, error) {
	order := "asc"
	if direction < 0 {
		order = "desc"
	}
	params := db.ListRSSSubscriptionsSortedParams{
		SortKey:   &field,
		SortOrder: &order,
		PageSize:  int32(limit) + 1,
	}
	if cursor != nil {
		row, err := workflow.queries.GetRSSSubscription(ctx, repository.UUIDToPG(*cursor))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.RSSSubscriptionPage{}, NewError("invalid_cursor", "the list cursor was not found", ErrInvalidInput, map[string]any{"cursor": cursor.String()})
		}
		if err != nil {
			return domain.RSSSubscriptionPage{}, fmt.Errorf("load RSS subscription cursor: %w", err)
		}
		params.CursorID = repository.UUIDToPG(*cursor)
		params.CursorName = &row.Name
		params.CursorSeriesTitle = &row.SeriesTitle
		params.CursorSourceSeason = &row.SourceSeason
		params.CursorEnabled = &row.Enabled
		params.CursorNextPollAt = row.NextPollAt
		params.CursorCreatedAt = row.CreatedAt
	}
	rows, err := workflow.queries.ListRSSSubscriptionsSorted(ctx, params)
	if err != nil {
		return domain.RSSSubscriptionPage{}, fmt.Errorf("list RSS subscriptions: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	items := make([]domain.RSSSubscription, 0, len(rows))
	for _, row := range rows {
		items = append(items, subscriptionFromListRow(sortedSubscriptionToListRow(row)))
	}
	progressBySubscription, err := workflow.subscriptionProgressBySubscriptions(ctx, subscriptionIDsOf(items))
	if err != nil {
		return domain.RSSSubscriptionPage{}, err
	}
	for index := range items {
		applyRSSSubscriptionProgress(&items[index], progressBySubscription[items[index].ID])
	}
	return domain.RSSSubscriptionPage{
		Items:      items,
		NextCursor: pageCursor(hasMore, len(items), func(i int) uuid.UUID { return items[i].ID }),
	}, nil
}

// listSubscriptionsByComputedProgress 为 progress 排序保留既有语义：
// 全量加载订阅后计算所有进度，在内存中排序并按 cursor 窗口分页。
func (workflow *RSSWorkflow) listSubscriptionsByComputedProgress(
	ctx context.Context,
	cursor *uuid.UUID,
	limit int,
	direction int,
) (domain.RSSSubscriptionPage, error) {
	const batchSize = 200
	baseSort := "newest"
	var batchCursor *uuid.UUID
	items := make([]domain.RSSSubscription, 0, batchSize)
	for {
		params := db.ListRSSSubscriptionsParams{Sort: &baseSort, PageSize: batchSize}
		if batchCursor != nil {
			params.CursorID = repository.UUIDToPG(*batchCursor)
		}
		rows, err := workflow.queries.ListRSSSubscriptions(ctx, params)
		if err != nil {
			return domain.RSSSubscriptionPage{}, fmt.Errorf("list RSS subscriptions: %w", err)
		}
		for _, row := range rows {
			items = append(items, subscriptionFromListRow(row))
		}
		if len(rows) < batchSize {
			break
		}
		last := repository.UUIDFromPG(rows[len(rows)-1].ID)
		batchCursor = &last
	}
	progressBySubscription, err := workflow.subscriptionProgressBySubscriptions(ctx, subscriptionIDsOf(items))
	if err != nil {
		return domain.RSSSubscriptionPage{}, err
	}
	for index := range items {
		applyRSSSubscriptionProgress(&items[index], progressBySubscription[items[index].ID])
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		comparison := cmp.Compare(left.OverallProgress, right.OverallProgress)
		if comparison == 0 {
			comparison = strings.Compare(left.ID.String(), right.ID.String())
		}
		return direction*comparison < 0
	})
	window, hasMore, err := cursorWindow(items, cursor, limit, func(item domain.RSSSubscription) uuid.UUID { return item.ID })
	if err != nil {
		return domain.RSSSubscriptionPage{}, err
	}
	return domain.RSSSubscriptionPage{
		Items:      window,
		NextCursor: pageCursor(hasMore, len(window), func(i int) uuid.UUID { return window[i].ID }),
	}, nil
}

func subscriptionIDsOf(items []domain.RSSSubscription) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func (workflow *RSSWorkflow) GetSubscription(ctx context.Context, id uuid.UUID) (domain.RSSSubscription, error) {
	row, err := workflow.queries.GetRSSSubscription(ctx, repository.UUIDToPG(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RSSSubscription{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.RSSSubscription{}, fmt.Errorf("get RSS subscription: %w", err)
	}
	subscription := subscriptionFromGetRow(row)
	progress, err := workflow.subscriptionProgress(ctx, subscription.ID)
	if err != nil {
		return domain.RSSSubscription{}, fmt.Errorf("load RSS subscription progress: %w", err)
	}
	applyRSSSubscriptionProgress(&subscription, progress)
	return subscription, nil
}

func (workflow *RSSWorkflow) ReconcileAutoReviews(ctx context.Context) (int, error) {
	reviewed := 0
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		var err error
		reviewed, err = workflow.approvePendingAutoReviewsInTx(ctx, scope, pgtype.UUID{})
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("reconcile RSS automatic reviews: %w", err)
	}
	return reviewed, nil
}

func (workflow *RSSWorkflow) approvePendingAutoReviewsInTx(
	ctx context.Context,
	scope database.TxScope,
	subscriptionID pgtype.UUID,
) (int, error) {
	tasks, err := scope.Queries.ListRSSAutoReviewPendingTasks(ctx, subscriptionID)
	if err != nil {
		return 0, fmt.Errorf("list RSS automatic reviews: %w", err)
	}
	for _, task := range tasks {
		if err := approveRSSReviewInTx(
			ctx,
			scope,
			workflow.operations,
			repository.UUIDFromPG(task.ID),
			uuid.Nil,
			repository.UUIDFromPG(task.SubscriptionID),
			task.Version,
		); err != nil {
			return 0, err
		}
	}
	return len(tasks), nil
}

func (workflow *RSSWorkflow) UpdateSubscription(
	ctx context.Context,
	input domain.UpdateRSSSubscription,
) (domain.RSSSubscription, error) {
	if input.ID == uuid.Nil || input.ExpectedVersion <= 0 {
		return domain.RSSSubscription{}, invalidRSSSubscription("expectedVersion", "must be positive")
	}
	includeKeywords, err := normalizeRSSKeywords("includeKeywords", input.IncludeKeywords)
	if err != nil {
		return domain.RSSSubscription{}, err
	}
	excludeKeywords, err := normalizeRSSKeywords("excludeKeywords", input.ExcludeKeywords)
	if err != nil {
		return domain.RSSSubscription{}, err
	}
	input.IncludeKeywords = includeKeywords
	input.ExcludeKeywords = excludeKeywords
	if err := validateRSSSubscription(1, "existing", input.Name, input.FeedURL, input.SourceSeason, input.PollInterval); err != nil {
		return domain.RSSSubscription{}, err
	}
	progress, err := workflow.subscriptionProgress(ctx, input.ID)
	if err != nil {
		return domain.RSSSubscription{}, fmt.Errorf("load RSS subscription progress: %w", err)
	}
	var result domain.RSSSubscription
	err = workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		current, err := scope.Queries.GetRSSSubscription(ctx, repository.UUIDToPG(input.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load RSS subscription for update: %w", err)
		}
		if current.CompletedAt.Valid && input.Enabled {
			return NewError(
				"rss_subscription_completed",
				"a completed RSS subscription cannot be enabled",
				ErrStateConflict,
				map[string]any{"subscriptionId": input.ID},
			)
		}
		mappingProfile, err := resolveRSSMappingProfile(ctx, scope.Queries, current.SeriesID, input.SourceSeason, input.MappingProfileID)
		if err != nil {
			return err
		}
		updated, err := scope.Queries.UpdateRSSSubscription(ctx, db.UpdateRSSSubscriptionParams{
			Name:                      strings.TrimSpace(input.Name),
			FeedUrl:                   strings.TrimSpace(input.FeedURL),
			IncludeKeywords:           input.IncludeKeywords,
			ExcludeKeywords:           input.ExcludeKeywords,
			Enabled:                   input.Enabled,
			AutoEpisodeMapping:        input.AutoEpisodeMapping,
			AutoReview:                input.AutoReview,
			CleanupSourceOnCompletion: input.CleanupSourceOnCompletion,
			PollIntervalSeconds:       int32(input.PollInterval / time.Second),
			SourceSeason:              int32(input.SourceSeason),
			MappingProfileID:          nullableUUID(mappingProfile.ID),
			ID:                        repository.UUIDToPG(input.ID),
			ExpectedVersion:           input.ExpectedVersion,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrVersionConflict
		}
		if err != nil {
			return fmt.Errorf("update RSS subscription: %w", err)
		}
		tmdbID := int64(0)
		if current.TmdbSeriesID != nil {
			tmdbID = *current.TmdbSeriesID
		}
		result = subscriptionFromRow(updated, current.SeriesTitle, tmdbID)
		propagated, err := scope.Queries.PropagateRSSMappingProfile(ctx, db.PropagateRSSMappingProfileParams{
			MappingProfileID: nullableUUID(mappingProfile.ID),
			SubscriptionID:   repository.UUIDToPG(input.ID),
		})
		if err != nil {
			return fmt.Errorf("propagate RSS mapping profile: %w", err)
		}
		var corrected int64
		var scheduled int
		if mappingProfile.ID != uuid.Nil {
			acquisitions, err := scope.Queries.ListRSSSubscriptionAcquisitions(ctx, repository.UUIDToPG(input.ID))
			if err != nil {
				return fmt.Errorf("list RSS acquisitions for mapping recovery: %w", err)
			}
			if len(acquisitions) > 0 {
				anchorID := repository.UUIDFromPG(acquisitions[0].ID)
				corrected, err = repairMappingScopeCoordinates(ctx, scope.Queries, anchorID)
				if err != nil {
					return err
				}
				scheduled, err = scheduleMappingMaterializations(ctx, scope, workflow.operations, anchorID, mappingProfile.ID, input.ActorUserID)
				if err != nil {
					return err
				}
			}
		}
		autoReviewed := 0
		if result.AutoReview {
			autoReviewed, err = workflow.approvePendingAutoReviewsInTx(ctx, scope, repository.UUIDToPG(result.ID))
			if err != nil {
				return err
			}
		}
		eventData := map[string]any{
			"version":                   result.Version,
			"autoReviewedTasks":         autoReviewed,
			"enabled":                   result.Enabled,
			"includeKeywords":           result.IncludeKeywords,
			"excludeKeywords":           result.ExcludeKeywords,
			"autoEpisodeMapping":        result.AutoEpisodeMapping,
			"autoReview":                result.AutoReview,
			"cleanupSourceOnCompletion": result.CleanupSourceOnCompletion,
			"propagatedAcquisitions":    propagated,
			"correctedFiles":            corrected,
			"materializationOperations": scheduled,
		}
		if mappingProfile.ID != uuid.Nil {
			eventData["mappingProfileId"] = mappingProfile.ID
			eventData["mappingProfileAutoSelected"] = mappingProfile.AutoSelected
		}
		if err := appendRSSSubscriptionCommandEvent(ctx, scope.Queries, result.ID, input.ActorUserID, "rss.subscription.updated", eventData); err != nil {
			return err
		}
		if result.AutoEpisodeMapping && !current.AutoEpisodeMapping {
			if _, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
				Kind:           appqueue.KindTMDbSync,
				ResourceType:   "media_series",
				ResourceID:     repository.UUIDFromPG(current.SeriesID),
				IdempotencyKey: fmt.Sprintf("tmdb.sync:rss-auto-mapping:%s:%d", result.ID, result.Version),
				MaxAttempts:    4,
				Timeout:        10 * time.Minute,
				Payload:        map[string]any{"tmdbSeriesId": tmdbID},
				ActorUserID:    input.ActorUserID,
			}); err != nil {
				return fmt.Errorf("schedule RSS automatic Mapping backfill: %w", err)
			}
		}
		if result.Enabled {
			if err := workflow.scheduleContinuousPoll(ctx, scope, result); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.RSSSubscription{}, rssCommandError("update RSS subscription", err)
	}
	applyRSSSubscriptionProgress(&result, progress)
	return result, nil
}

func (workflow *RSSWorkflow) ArchiveSubscription(
	ctx context.Context,
	id uuid.UUID,
	expectedVersion int32,
	actorUserID uuid.UUID,
) error {
	if id == uuid.Nil || expectedVersion <= 0 {
		return invalidRSSSubscription("expectedVersion", "must be positive")
	}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		if _, err := scope.Queries.GetRSSSubscription(ctx, repository.UUIDToPG(id)); errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("load RSS subscription for archive: %w", err)
		}
		archived, err := scope.Queries.ArchiveRSSSubscription(ctx, db.ArchiveRSSSubscriptionParams{
			ID:              repository.UUIDToPG(id),
			ExpectedVersion: expectedVersion,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrVersionConflict
		}
		if err != nil {
			return fmt.Errorf("archive RSS subscription: %w", err)
		}
		return appendRSSSubscriptionCommandEvent(ctx, scope.Queries, id, actorUserID, "rss.subscription.archived", map[string]any{
			"version": archived.Version,
		})
	})
	if err != nil {
		return rssCommandError("archive RSS subscription", err)
	}
	return nil
}

// RequestSubscriptionDeletion archives the subscription and schedules a
// cascading cleanup operation. Imported library files are removed only when
// deleteImported is explicitly requested.
func (workflow *RSSWorkflow) RequestSubscriptionDeletion(
	ctx context.Context,
	id uuid.UUID,
	expectedVersion int32,
	idempotencyKey string,
	deleteImported bool,
	actorUserID uuid.UUID,
) (domain.Operation, error) {
	if id == uuid.Nil || expectedVersion <= 0 {
		return domain.Operation{}, invalidRSSSubscription("expectedVersion", "must be positive")
	}
	if len(idempotencyKey) == 0 {
		return domain.Operation{}, invalidRSSSubscription("idempotencyKey", "must not be blank")
	}
	var operation domain.Operation
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		existing, replayed, err := findIdempotentResourceCommand(
			ctx, scope, idempotencyKey, "rss_subscription", id, "delete", appqueue.KindRSSSubscriptionDelete,
		)
		if err != nil {
			return err
		}
		if replayed {
			var payload struct {
				DeleteImported bool `json:"deleteImported"`
			}
			if json.Unmarshal(existing.Payload, &payload) != nil || payload.DeleteImported != deleteImported {
				return NewError("idempotency_conflict", "the idempotency key was already used for a different command", ErrStateConflict, map[string]any{"idempotencyKey": idempotencyKey})
			}
			operation = existing
			return nil
		}
		if _, err := scope.Queries.GetRSSSubscription(ctx, repository.UUIDToPG(id)); errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("load RSS subscription for deletion: %w", err)
		}
		archived, err := scope.Queries.ArchiveRSSSubscription(ctx, db.ArchiveRSSSubscriptionParams{
			ID:              repository.UUIDToPG(id),
			ExpectedVersion: expectedVersion,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrVersionConflict
		}
		if err != nil {
			return fmt.Errorf("archive RSS subscription: %w", err)
		}
		operation, err = scheduleRSSSubscriptionDeletionInTx(
			ctx, scope, workflow.operations, id, idempotencyKey, deleteImported, actorUserID, uuid.Nil, "manual",
		)
		if err != nil {
			return err
		}
		return appendRSSSubscriptionCommandEvent(ctx, scope.Queries, id, actorUserID, "rss.subscription.delete_requested", map[string]any{
			"version": archived.Version, "deleteImported": deleteImported,
		})
	})
	if err != nil {
		return domain.Operation{}, rssCommandError("delete RSS subscription", err)
	}
	return operation, nil
}

func scheduleRSSSubscriptionDeletionInTx(
	ctx context.Context,
	scope database.TxScope,
	operations *OperationScheduler,
	subscriptionID uuid.UUID,
	idempotencyKey string,
	deleteImported bool,
	actorUserID uuid.UUID,
	excludedOperationID uuid.UUID,
	trigger string,
) (domain.Operation, error) {
	return scheduleRSSSubscriptionCleanupInTx(
		ctx, scope, operations, subscriptionID, idempotencyKey, deleteImported,
		actorUserID, excludedOperationID, trigger, appqueue.KindRSSSubscriptionDelete, "delete",
	)
}

func scheduleRSSSubscriptionCleanupInTx(
	ctx context.Context,
	scope database.TxScope,
	operations *OperationScheduler,
	subscriptionID uuid.UUID,
	idempotencyKey string,
	deleteImported bool,
	actorUserID uuid.UUID,
	excludedOperationID uuid.UUID,
	trigger string,
	kind string,
	command string,
) (domain.Operation, error) {
	if err := requestResourceOperationCancellations(ctx, scope, "rss_subscription", subscriptionID, actorUserID); err != nil {
		return domain.Operation{}, err
	}
	acquisitionIDs, err := scope.Queries.ListSubscriptionDeletionAcquisitionIDs(ctx, repository.UUIDToPG(subscriptionID))
	if err != nil {
		return domain.Operation{}, fmt.Errorf("list RSS acquisitions for cleanup: %w", err)
	}
	for _, acquisitionID := range acquisitionIDs {
		if err := prepareAcquisitionDeletionInTx(ctx, scope, repository.UUIDFromPG(acquisitionID), true, excludedOperationID); err != nil {
			return domain.Operation{}, fmt.Errorf("prepare RSS acquisition cleanup: %w", err)
		}
	}
	scheduled, err := operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
		Kind:           kind,
		ResourceType:   "rss_subscription",
		ResourceID:     subscriptionID,
		IdempotencyKey: idempotencyKey,
		MaxAttempts:    3,
		Timeout:        30 * time.Minute,
		Payload: map[string]any{
			"command": command, "subscriptionId": subscriptionID, "actorUserId": actorUserID,
			"deleteImported": deleteImported, "trigger": trigger,
		},
		ActorUserID: actorUserID,
	})
	if err != nil {
		return domain.Operation{}, fmt.Errorf("schedule RSS subscription cleanup: %w", err)
	}
	return scheduled.Operation, nil
}

func (workflow *RSSWorkflow) ScheduleManualPoll(
	ctx context.Context,
	id uuid.UUID,
	idempotencyKey string,
	actorUserID uuid.UUID,
) (domain.Operation, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return domain.Operation{}, NewError(
			"invalid_idempotency_key",
			"Idempotency-Key must not be blank",
			ErrInvalidInput,
			map[string]any{},
		)
	}
	subscription, err := workflow.GetSubscription(ctx, id)
	if err != nil {
		return domain.Operation{}, err
	}
	if subscription.CompletedAt != nil {
		return domain.Operation{}, NewError(
			"rss_subscription_completed",
			"a completed RSS subscription cannot be polled",
			ErrStateConflict,
			map[string]any{"subscriptionId": id},
		)
	}
	if !subscription.Enabled {
		return domain.Operation{}, NewError(
			"state_conflict",
			"the RSS subscription is disabled",
			ErrStateConflict,
			map[string]any{"subscriptionId": id},
		)
	}
	result, err := workflow.operations.Schedule(ctx, ScheduleOperationRequest{
		Kind:           appqueue.KindRSSPoll,
		ResourceType:   "rss_subscription",
		ResourceID:     id,
		IdempotencyKey: "rss.poll.manual:" + id.String() + ":" + strings.TrimSpace(idempotencyKey),
		MaxAttempts:    5,
		Timeout:        30 * time.Second,
		Payload: map[string]any{
			"continuous":          false,
			"subscriptionVersion": subscription.Version,
		},
		ActorUserID: actorUserID,
	})
	if err != nil {
		return domain.Operation{}, err
	}
	return result.Operation, nil
}

func (workflow *RSSWorkflow) scheduleContinuousPoll(
	ctx context.Context,
	scope database.TxScope,
	subscription domain.RSSSubscription,
) error {
	_, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
		Kind:           appqueue.KindRSSPoll,
		ResourceType:   "rss_subscription",
		ResourceID:     subscription.ID,
		IdempotencyKey: fmt.Sprintf("rss.poll:%s:v%d:continuous", subscription.ID, subscription.Version),
		MaxAttempts:    5,
		Timeout:        30 * time.Second,
		Payload: map[string]any{
			"continuous":          true,
			"subscriptionVersion": subscription.Version,
		},
	})
	if err != nil {
		return fmt.Errorf("schedule continuous RSS poll: %w", err)
	}
	return nil
}

type rssMappingProfileResolution struct {
	ID           uuid.UUID
	AutoSelected bool
}

func resolveRSSMappingProfile(
	ctx context.Context,
	queries *db.Queries,
	seriesID pgtype.UUID,
	sourceSeason int,
	requestedID uuid.UUID,
) (rssMappingProfileResolution, error) {
	if requestedID != uuid.Nil {
		_, err := queries.GetCompatibleActiveMappingProfile(ctx, db.GetCompatibleActiveMappingProfileParams{
			ProfileID:    repository.UUIDToPG(requestedID),
			SeriesID:     seriesID,
			SourceSeason: mappingInt32(sourceSeason),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return rssMappingProfileResolution{}, invalidRSSSubscription(
				"mappingProfileId",
				"must reference an active profile for the same series with a complete mapping for sourceSeason",
			)
		}
		if err != nil {
			return rssMappingProfileResolution{}, fmt.Errorf("validate RSS mapping profile: %w", err)
		}
		return rssMappingProfileResolution{ID: requestedID}, nil
	}

	profiles, err := queries.ListCompatibleActiveMappingProfiles(ctx, db.ListCompatibleActiveMappingProfilesParams{
		SeriesID:     seriesID,
		SourceSeason: mappingInt32(sourceSeason),
	})
	if err != nil {
		return rssMappingProfileResolution{}, fmt.Errorf("list compatible RSS mapping profiles: %w", err)
	}
	if len(profiles) != 1 {
		return rssMappingProfileResolution{}, nil
	}
	return rssMappingProfileResolution{
		ID:           repository.UUIDFromPG(profiles[0]),
		AutoSelected: true,
	}, nil
}

func normalizeRSSKeywords(field string, keywords []string) ([]string, error) {
	if len(keywords) > 20 {
		return nil, invalidRSSSubscription(field, "must contain at most 20 keywords")
	}
	normalized := make([]string, 0, len(keywords))
	seen := make(map[string]struct{}, len(keywords))
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			return nil, invalidRSSSubscription(field, "keywords must not be blank")
		}
		if utf8.RuneCountInString(keyword) > 128 {
			return nil, invalidRSSSubscription(field, "each keyword must contain at most 128 characters")
		}
		identity := strings.ToLower(keyword)
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		normalized = append(normalized, keyword)
	}
	return normalized, nil
}

func validateRSSSubscription(tmdbSeriesID int64, seriesTitle, name, feedURL string, sourceSeason int, pollInterval time.Duration) error {
	if tmdbSeriesID <= 0 {
		return invalidRSSSubscription("tmdbSeriesId", "must be positive")
	}
	if strings.TrimSpace(seriesTitle) == "" {
		return invalidRSSSubscription("seriesTitle", "must not be blank")
	}
	if strings.TrimSpace(name) == "" {
		return invalidRSSSubscription("name", "must not be blank")
	}
	parsed, err := url.ParseRequestURI(strings.TrimSpace(feedURL))
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return invalidRSSSubscription("feedUrl", "must be an HTTP(S) URL without embedded credentials")
	}
	if sourceSeason <= 0 || sourceSeason > math.MaxInt32 {
		return invalidRSSSubscription("sourceSeason", "must be between 1 and 2147483647")
	}
	if pollInterval < time.Minute || pollInterval > 24*time.Hour || pollInterval%time.Second != 0 {
		return invalidRSSSubscription("pollIntervalSeconds", "must be between 60 and 86400 whole seconds")
	}
	return nil
}

func invalidRSSSubscription(field, reason string) *Error {
	return NewError(
		"invalid_rss_subscription",
		"the RSS subscription is invalid",
		ErrInvalidInput,
		map[string]any{"field": field, "reason": reason},
	)
}

func rssCommandError(action string, err error) error {
	var serviceError *Error
	if errors.As(err, &serviceError) {
		return serviceError
	}
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrNotFound
	}
	if errors.Is(err, domain.ErrVersionConflict) {
		return NewError(
			"state_conflict",
			"the RSS subscription was modified by another request",
			ErrStateConflict,
			map[string]any{},
		)
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23505" {
		return NewError(
			"state_conflict",
			"an RSS subscription already exists for this series and feed URL",
			ErrStateConflict,
			map[string]any{},
		)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func appendRSSSubscriptionCommandEvent(
	ctx context.Context,
	queries *db.Queries,
	subscriptionID uuid.UUID,
	actorUserID uuid.UUID,
	topic string,
	data map[string]any,
) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode RSS subscription event: %w", err)
	}
	resourceType := "rss_subscription"
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:           repository.UUIDToPG(uuid.New()),
		Topic:        topic,
		ResourceType: &resourceType,
		ResourceID:   repository.UUIDToPG(subscriptionID),
		ActorUserID:  repository.UUIDToPG(actorUserID),
		Data:         encoded,
	}); err != nil {
		return fmt.Errorf("append RSS subscription event: %w", err)
	}
	return nil
}

type rssSubscriptionProgress struct {
	overallProgress    float64
	taskCount          int
	completedTaskCount int
	attentionTaskCount int
}

func (workflow *RSSWorkflow) subscriptionProgress(ctx context.Context, subscriptionID uuid.UUID) (rssSubscriptionProgress, error) {
	rows, err := workflow.queries.ListRSSSubscriptionAcquisitions(ctx, repository.UUIDToPG(subscriptionID))
	if err != nil {
		return rssSubscriptionProgress{}, fmt.Errorf("list subscription acquisitions: %w", err)
	}
	views, err := NewReadService(workflow.queries).acquisitionViews(ctx, rows)
	if err != nil {
		return rssSubscriptionProgress{}, err
	}
	importedCount, err := workflow.queries.GetRSSSubscriptionImportedCount(ctx, repository.UUIDToPG(subscriptionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return summarizeRSSSubscriptionProgress(views), nil
	}
	if err != nil {
		return rssSubscriptionProgress{}, fmt.Errorf("load RSS subscription imported count: %w", err)
	}
	return summarizeRSSSubscriptionImportedProgress(int(importedCount), views), nil
}

func summarizeRSSSubscriptionImportedProgress(importedCount int, views []domain.AcquisitionView) rssSubscriptionProgress {
	progress := rssSubscriptionProgress{
		taskCount:          max(0, importedCount),
		completedTaskCount: max(0, importedCount),
	}
	progress.overallProgress = float64(progress.completedTaskCount)
	for _, view := range views {
		if _, needsAttention := dashboardAttentionItem(view); needsAttention {
			progress.attentionTaskCount++
		}
		// Imported entries are already represented by importedCount even while
		// their acquisition remains visible before subscription cleanup.
		if view.AggregateStatus == "completed" {
			continue
		}
		progress.taskCount++
		progress.overallProgress += max(0, min(1, view.OverallProgress))
	}
	if progress.taskCount > 0 {
		progress.overallProgress = min(1, progress.overallProgress/float64(progress.taskCount))
	}
	return progress
}

func summarizeRSSSubscriptionProgress(views []domain.AcquisitionView) rssSubscriptionProgress {
	progress := rssSubscriptionProgress{taskCount: len(views)}
	if len(views) == 0 {
		return progress
	}
	for _, view := range views {
		progress.overallProgress += max(0, min(1, view.OverallProgress))
		if view.AggregateStatus == "completed" {
			progress.completedTaskCount++
		}
		if _, needsAttention := dashboardAttentionItem(view); needsAttention {
			progress.attentionTaskCount++
		}
	}
	progress.overallProgress /= float64(len(views))
	return progress
}

func applyRSSSubscriptionProgress(subscription *domain.RSSSubscription, progress rssSubscriptionProgress) {
	if subscription.CompletedAt != nil {
		progress.overallProgress = 1
	}
	subscription.OverallProgress = progress.overallProgress
	subscription.TaskCount = progress.taskCount
	subscription.CompletedTaskCount = progress.completedTaskCount
	subscription.AttentionTaskCount = progress.attentionTaskCount
}

func subscriptionFromRow(row db.RssSubscription, seriesTitle string, tmdbSeriesID int64) domain.RSSSubscription {
	return domain.RSSSubscription{
		ID:                        repository.UUIDFromPG(row.ID),
		SeriesID:                  repository.UUIDFromPG(row.SeriesID),
		SeriesTitle:               seriesTitle,
		TMDbSeriesID:              tmdbSeriesID,
		MappingProfileID:          repository.UUIDFromPG(row.MappingProfileID),
		Name:                      row.Name,
		FeedURL:                   row.FeedUrl,
		IncludeKeywords:           row.IncludeKeywords,
		ExcludeKeywords:           row.ExcludeKeywords,
		Enabled:                   row.Enabled,
		AutoEpisodeMapping:        row.AutoEpisodeMapping,
		AutoReview:                row.AutoReview,
		CleanupSourceOnCompletion: row.CleanupSourceOnCompletion,
		SourceSeason:              int(row.SourceSeason),
		PollInterval:              time.Duration(row.PollIntervalSeconds) * time.Second,
		LastPolledAt:              optionalTimePointer(row.LastPolledAt),
		NextPollAt:                optionalTimePointer(row.NextPollAt),
		CompletedAt:               optionalTimePointer(row.CompletedAt),
		Version:                   row.Version,
		CreatedAt:                 row.CreatedAt.Time,
		UpdatedAt:                 row.UpdatedAt.Time,
	}
}

func subscriptionFromGetRow(row db.GetRSSSubscriptionRow) domain.RSSSubscription {
	tmdbID := int64(0)
	if row.TmdbSeriesID != nil {
		tmdbID = *row.TmdbSeriesID
	}
	return domain.RSSSubscription{
		ID:                        repository.UUIDFromPG(row.ID),
		SeriesID:                  repository.UUIDFromPG(row.SeriesID),
		SeriesTitle:               row.SeriesTitle,
		TMDbSeriesID:              tmdbID,
		MappingProfileID:          repository.UUIDFromPG(row.MappingProfileID),
		Name:                      row.Name,
		FeedURL:                   row.FeedUrl,
		IncludeKeywords:           row.IncludeKeywords,
		ExcludeKeywords:           row.ExcludeKeywords,
		Enabled:                   row.Enabled,
		AutoEpisodeMapping:        row.AutoEpisodeMapping,
		AutoReview:                row.AutoReview,
		CleanupSourceOnCompletion: row.CleanupSourceOnCompletion,
		SourceSeason:              int(row.SourceSeason),
		PollInterval:              time.Duration(row.PollIntervalSeconds) * time.Second,
		LastPolledAt:              optionalTimePointer(row.LastPolledAt),
		NextPollAt:                optionalTimePointer(row.NextPollAt),
		CompletedAt:               optionalTimePointer(row.CompletedAt),
		Version:                   row.Version,
		CreatedAt:                 row.CreatedAt.Time,
		UpdatedAt:                 row.UpdatedAt.Time,
	}
}

// sortedSubscriptionToListRow converts the SQL-sorted row to the shared
// list row shape; both queries project the same columns.
func sortedSubscriptionToListRow(row db.ListRSSSubscriptionsSortedRow) db.ListRSSSubscriptionsRow {
	return db.ListRSSSubscriptionsRow(row)
}

func subscriptionFromListRow(row db.ListRSSSubscriptionsRow) domain.RSSSubscription {
	tmdbID := int64(0)
	if row.TmdbSeriesID != nil {
		tmdbID = *row.TmdbSeriesID
	}
	return domain.RSSSubscription{
		ID:                        repository.UUIDFromPG(row.ID),
		SeriesID:                  repository.UUIDFromPG(row.SeriesID),
		SeriesTitle:               row.SeriesTitle,
		TMDbSeriesID:              tmdbID,
		MappingProfileID:          repository.UUIDFromPG(row.MappingProfileID),
		Name:                      row.Name,
		FeedURL:                   row.FeedUrl,
		IncludeKeywords:           row.IncludeKeywords,
		ExcludeKeywords:           row.ExcludeKeywords,
		Enabled:                   row.Enabled,
		AutoEpisodeMapping:        row.AutoEpisodeMapping,
		AutoReview:                row.AutoReview,
		CleanupSourceOnCompletion: row.CleanupSourceOnCompletion,
		SourceSeason:              int(row.SourceSeason),
		PollInterval:              time.Duration(row.PollIntervalSeconds) * time.Second,
		LastPolledAt:              optionalTimePointer(row.LastPolledAt),
		NextPollAt:                optionalTimePointer(row.NextPollAt),
		CompletedAt:               optionalTimePointer(row.CompletedAt),
		RetryableTaskCount:        int(row.RetryableTaskCount),
		Version:                   row.Version,
		CreatedAt:                 row.CreatedAt.Time,
		UpdatedAt:                 row.UpdatedAt.Time,
	}
}

func optionalTimePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}

func pgtypeUUID(value *uuid.UUID) pgtype.UUID {
	if value == nil {
		return pgtype.UUID{}
	}
	return repository.UUIDToPG(*value)
}
