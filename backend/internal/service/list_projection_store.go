package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type listProjectionKind string

const (
	acquisitionListProjection listProjectionKind = "acquisition"
	rssEntryListProjection    listProjectionKind = "rss_entry"
	listProjectionVersion     int32              = 1
	listProjectionBatchSize   int32              = 100
)

// RegisterProgressCommitHook 将索引字段与业务状态置于同一提交边界。
// 按获取任务、RSS 条目、订阅的依赖顺序计算，重复注册保持幂等。
func RegisterProgressCommitHook(transactor *database.Transactor) {
	if transactor == nil {
		return
	}
	transactor.RegisterBeforeCommitHook("progress_read_models", func(ctx context.Context, scope database.TxScope) error {
		for _, kind := range []listProjectionKind{acquisitionListProjection, rssEntryListProjection} {
			for {
				count, err := refreshListProjectionBatch(ctx, scope.Queries, kind, true)
				if err != nil {
					return err
				}
				if count == 0 {
					break
				}
			}
		}
		return (&rssSubscriptionProgressReadModel{}).recalculateCurrentTransaction(ctx, scope)
	})
}

// ReconcileListProjections 用于启动恢复和受控维护写入后的补算。
// 每个事务仅处理固定大小的批次，生命周期规则仍由 Go 统一计算。
func (service *ReadService) ReconcileListProjections(ctx context.Context) error {
	for _, kind := range []listProjectionKind{acquisitionListProjection, rssEntryListProjection} {
		state, err := service.queries.GetListProjectionReadiness(ctx, db.GetListProjectionReadinessParams{
			Kind: string(kind), TargetModelVersion: listProjectionVersion,
		})
		if err != nil {
			return fmt.Errorf("inspect %s list projection: %w", kind, err)
		}
		if state.HasNewerModel {
			return newerListProjectionError()
		}
		if !state.NeedsRefresh {
			continue
		}
		if _, err := service.queries.MarkOutdatedListProjectionsDirty(ctx, db.MarkOutdatedListProjectionsDirtyParams{
			Kind: string(kind), TargetModelVersion: listProjectionVersion,
		}); err != nil {
			return fmt.Errorf("invalidate outdated %s list projection: %w", kind, err)
		}
		for {
			var count int
			err := service.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
				var err error
				count, err = refreshListProjectionBatch(ctx, scope.Queries, kind, false)
				return err
			})
			if err != nil {
				return fmt.Errorf("reconcile %s list projection: %w", kind, err)
			}
			if count == 0 {
				break
			}
		}
	}
	return nil
}

func newerListProjectionError() error {
	return NewError("list_projection_newer_version", "列表数据由更新版本的服务维护，请使用匹配的服务版本", ErrUnavailable, nil)
}

func refreshListProjectionBatch(ctx context.Context, queries *db.Queries, kind listProjectionKind, transactionOnly bool) (int, error) {
	switch kind {
	case acquisitionListProjection:
		rows, err := queries.LockAcquisitionListProjectionBatch(ctx, db.LockAcquisitionListProjectionBatchParams{
			TransactionOnly: transactionOnly, BatchSize: listProjectionBatchSize,
		})
		if err != nil {
			return 0, fmt.Errorf("lock acquisition list projections: %w", err)
		}
		return len(rows), calculateAcquisitionListProjections(ctx, queries, rows)
	case rssEntryListProjection:
		ids, err := queries.ListDirtyRSSEntryListProjectionIDs(ctx, db.ListDirtyRSSEntryListProjectionIDsParams{
			TransactionOnly: transactionOnly, BatchSize: listProjectionBatchSize,
		})
		if err != nil || len(ids) == 0 {
			return 0, err
		}
		// 先锁父投影，再锁条目；父投影可能由之前的维护事务标记过期。
		parents, err := queries.LockRSSEntryParentListProjections(ctx, ids)
		if err != nil {
			return 0, fmt.Errorf("lock RSS entry parent projections: %w", err)
		}
		dirtyParents := make([]db.AcquisitionListProjection, 0, len(parents))
		for _, parent := range parents {
			if parent.ModelVersion > listProjectionVersion {
				return 0, newerListProjectionError()
			}
			if parent.Dirty || parent.ModelVersion < listProjectionVersion {
				dirtyParents = append(dirtyParents, parent)
			}
		}
		if err := calculateAcquisitionListProjections(ctx, queries, dirtyParents); err != nil {
			return 0, err
		}
		rows, err := queries.LockRSSEntryListProjectionsByIDs(ctx, ids)
		if err != nil {
			return 0, fmt.Errorf("lock RSS entry list projections: %w", err)
		}
		return len(rows), calculateRSSEntryListProjections(ctx, queries, rows)
	default:
		return 0, fmt.Errorf("unsupported list projection %q", kind)
	}
}

type acquisitionProjectionCalculation struct {
	ID                uuid.UUID `json:"acquisition_id"`
	SourceRevision    int64     `json:"source_revision"`
	ModelVersion      int32     `json:"model_version"`
	ListVisible       bool      `json:"list_visible"`
	Phases            []string  `json:"phases"`
	OverallProgress   float64   `json:"overall_progress"`
	ContentTitle      string    `json:"content_title"`
	MediaType         string    `json:"media_type"`
	ReleaseYear       int       `json:"release_year"`
	SourceSeason      int64     `json:"source_season"`
	SourceEpisode     int64     `json:"source_episode"`
	SourceKind        string    `json:"source_kind"`
	ResourceUpdatedAt time.Time `json:"resource_updated_at"`
}

func calculateAcquisitionListProjections(ctx context.Context, queries *db.Queries, locked []db.AcquisitionListProjection) error {
	if len(locked) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, len(locked))
	revisions := make(map[uuid.UUID]int64, len(locked))
	for index, projection := range locked {
		if projection.ModelVersion > listProjectionVersion {
			return newerListProjectionError()
		}
		ids[index] = projection.AcquisitionID
		revisions[repository.UUIDFromPG(projection.AcquisitionID)] = projection.SourceRevision
	}
	sources, err := queries.ListAcquisitionProjectionSources(ctx, ids)
	if err != nil {
		return fmt.Errorf("load acquisition projection sources: %w", err)
	}
	if len(sources) != len(locked) {
		return fmt.Errorf("acquisition projection source count changed: %d of %d", len(sources), len(locked))
	}
	rows := make([]db.Acquisition, len(sources))
	for index, source := range sources {
		rows[index] = source.Acquisition
	}
	views, err := acquisitionViewsWithQueries(ctx, queries, rows, false)
	if err != nil {
		return err
	}
	calculations := make([]acquisitionProjectionCalculation, len(views))
	for index, view := range views {
		phases := make([]string, 0, 2)
		for _, phase := range [...]string{"downloading", "processing", "awaiting_review", "importing", "completed", "mapping_pending", "attention"} {
			if phase == "mapping_pending" && !sources[index].MappingFilterMatch {
				continue
			}
			if acquisitionStageMatches(phase, view) {
				phases = append(phases, phase)
			}
		}
		calculation := acquisitionProjectionCalculation{
			ID: view.ID, SourceRevision: revisions[view.ID], ModelVersion: listProjectionVersion,
			ListVisible: sources[index].ListVisible, Phases: phases,
			OverallProgress: view.OverallProgress,
			ContentTitle:    strings.ToLower(acquisitionSortTitle(view)), MediaType: string(view.MediaType),
			ReleaseYear: view.ReleaseYear, SourceSeason: math.MaxInt64, SourceEpisode: math.MaxInt64,
			SourceKind: view.SourceKind, ResourceUpdatedAt: view.UpdatedAt,
		}
		if view.SourceSeason != nil {
			calculation.SourceSeason = int64(*view.SourceSeason)
		}
		if view.SourceEpisode != nil {
			calculation.SourceEpisode = int64(*view.SourceEpisode)
		}
		if view.MediaType == domain.TaskMediaMovie {
			calculation.SourceSeason, calculation.SourceEpisode = 0, 0
		}
		calculations[index] = calculation
	}
	payload, err := json.Marshal(calculations)
	if err != nil {
		return fmt.Errorf("encode acquisition projections: %w", err)
	}
	updated, err := queries.UpdateAcquisitionListProjections(ctx, payload)
	if err != nil {
		return fmt.Errorf("update acquisition projections: %w", err)
	}
	if updated != int64(len(locked)) {
		return fmt.Errorf("acquisition projection revision changed: updated %d of %d", updated, len(locked))
	}
	return nil
}

type rssEntryProjectionCalculation struct {
	ID              uuid.UUID `json:"entry_id"`
	SubscriptionID  uuid.UUID `json:"subscription_id"`
	SourceRevision  int64     `json:"source_revision"`
	ModelVersion    int32     `json:"model_version"`
	TitleKey        string    `json:"title_key"`
	EntryGroup      string    `json:"entry_group"`
	RejectReasons   []string  `json:"reject_reasons"`
	OverallProgress float64   `json:"overall_progress"`
	SourceSeason    int64     `json:"source_season"`
	SourceEpisode   int64     `json:"source_episode"`
	DiscoveredAt    time.Time `json:"discovered_at"`
}

func calculateRSSEntryListProjections(ctx context.Context, queries *db.Queries, locked []db.RssEntryListProjection) error {
	if len(locked) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, len(locked))
	revisions := make(map[uuid.UUID]int64, len(locked))
	for index, projection := range locked {
		if projection.ModelVersion > listProjectionVersion {
			return newerListProjectionError()
		}
		ids[index] = projection.EntryID
		revisions[repository.UUIDFromPG(projection.EntryID)] = projection.SourceRevision
	}
	rows, err := queries.ListRSSEntryReadModelsByIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("load RSS entry projection sources: %w", err)
	}
	if len(rows) != len(locked) {
		return fmt.Errorf("RSS entry projection source count changed: %d of %d", len(rows), len(locked))
	}
	parents, err := queries.ListRSSEntryParentListProjections(ctx, ids)
	if err != nil {
		return fmt.Errorf("load RSS entry parent projections: %w", err)
	}
	parentByEntry := make(map[uuid.UUID]db.ListRSSEntryParentListProjectionsRow, len(parents))
	for _, parent := range parents {
		parentByEntry[repository.UUIDFromPG(parent.EntryID)] = parent
	}
	calculations := make([]rssEntryProjectionCalculation, 0, len(rows))
	for _, source := range rows {
		row := source
		id := repository.UUIDFromPG(row.ID)
		classification := classifyRSSEntry(row)
		view := domain.RSSEntryView{Classification: classification, AdjudicationState: row.AdjudicationState}
		group := "pending"
		if rssEntryMatchesGroup(view, "confirmed") {
			group = "confirmed"
		} else if rssEntryMatchesGroup(view, "skipped") {
			group = "skipped"
		}
		calculation := rssEntryProjectionCalculation{
			ID: id, SubscriptionID: repository.UUIDFromPG(row.SubscriptionID),
			SourceRevision: revisions[id], ModelVersion: listProjectionVersion,
			TitleKey: strings.ToLower(row.Title), EntryGroup: group,
			RejectReasons: make([]string, 0, len(row.RejectionReasons)),
			SourceSeason:  math.MaxInt64, SourceEpisode: math.MaxInt64, DiscoveredAt: row.DiscoveredAt.Time,
		}
		for _, reason := range row.RejectionReasons {
			calculation.RejectReasons = append(calculation.RejectReasons, strings.TrimSpace(reason))
		}
		if row.SourceSeason != nil {
			calculation.SourceSeason = int64(*row.SourceSeason)
		}
		if row.SourceEpisode != nil {
			calculation.SourceEpisode = int64(*row.SourceEpisode)
		}
		parent := parentByEntry[id]
		if parent.AcquisitionID.Valid {
			if parent.ModelVersion > listProjectionVersion {
				return newerListProjectionError()
			}
			if parent.Dirty || parent.ModelVersion != listProjectionVersion {
				return fmt.Errorf("RSS entry parent projection is not reconciled for %s", id)
			}
			calculation.OverallProgress = parent.OverallProgress
		} else if row.ImportedAt.Valid && row.SuccessfulImportPresent {
			calculation.OverallProgress = 1
		}
		calculations = append(calculations, calculation)
	}
	payload, err := json.Marshal(calculations)
	if err != nil {
		return fmt.Errorf("encode RSS entry projections: %w", err)
	}
	updated, err := queries.UpdateRSSEntryListProjections(ctx, payload)
	if err != nil {
		return fmt.Errorf("update RSS entry projections: %w", err)
	}
	if updated != int64(len(locked)) {
		return fmt.Errorf("RSS entry projection revision changed: updated %d of %d", updated, len(locked))
	}
	return nil
}
