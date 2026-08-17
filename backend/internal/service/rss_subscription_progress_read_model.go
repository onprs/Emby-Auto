package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

const (
	// 修改 deriveAcquisitionLifecycle、dashboardAttentionItem 或订阅汇总语义时
	// 必须递增此版本，使现存 clean 投影重新进入 reconciliation。
	rssSubscriptionProgressModelVersion int32 = 1
	rssSubscriptionProgressBatchSize          = 100
	rssSubscriptionProgressHookName           = "rss_subscription_progress"
)

type rssSubscriptionProgressCandidate struct {
	subscriptionID uuid.UUID
	sourceRevision int64
	modelVersion   int32
	completed      bool
}

type rssSubscriptionProgressReadModel struct {
	queries    *db.Queries
	transactor *database.Transactor
}

func newRSSSubscriptionProgressReadModel(
	queries *db.Queries,
	transactor *database.Transactor,
) *rssSubscriptionProgressReadModel {
	model := &rssSubscriptionProgressReadModel{queries: queries, transactor: transactor}
	model.registerCommitHook()
	return model
}

// RegisterRSSSubscriptionProgressCommitHook 让独立维护进程复用与 API/Worker
// 相同的提交前重算边界，而不要求构造完整 RSS workflow。
func RegisterRSSSubscriptionProgressCommitHook(transactor *database.Transactor) {
	(&rssSubscriptionProgressReadModel{transactor: transactor}).registerCommitHook()
}

func (model *rssSubscriptionProgressReadModel) registerCommitHook() {
	if model != nil && model.transactor != nil {
		model.transactor.RegisterBeforeCommitHook(rssSubscriptionProgressHookName, model.recalculateCurrentTransaction)
	}
}

// recalculateCurrentTransaction 把触发器在当前事务中标脏的订阅投影
// 一并写回，使业务状态、事件、River 任务与 read model 原子提交。
func (model *rssSubscriptionProgressReadModel) recalculateCurrentTransaction(
	ctx context.Context,
	scope database.TxScope,
) error {
	rows, err := scope.Queries.LockCurrentTransactionRSSSubscriptionProgress(ctx)
	if err != nil {
		return fmt.Errorf("lock current transaction RSS subscription progress: %w", err)
	}
	candidates := make([]rssSubscriptionProgressCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, rssSubscriptionProgressCandidate{
			subscriptionID: repository.UUIDFromPG(row.SubscriptionID),
			sourceRevision: row.SourceRevision,
			modelVersion:   row.ModelVersion,
			completed:      row.CompletedAt.Valid,
		})
	}
	if err := recalculateRSSSubscriptionProgress(ctx, scope.Queries, candidates); err != nil {
		return fmt.Errorf("recalculate current transaction RSS subscription progress: %w", err)
	}
	return nil
}

// ReconcileSubscriptionProgress 恢复 migration、维护 SQL 或旧进程留下的
// dirty 投影。所有调用方复用这一入口，重复执行不会改写已收敛的行。
func (workflow *RSSWorkflow) ReconcileSubscriptionProgress(ctx context.Context) (int, error) {
	if workflow == nil || workflow.progressReadModel == nil {
		return 0, errors.New("RSS subscription progress read model is unavailable")
	}
	return workflow.progressReadModel.reconcileAll(ctx)
}

func (model *rssSubscriptionProgressReadModel) reconcileAll(ctx context.Context) (int, error) {
	if model.queries == nil || model.transactor == nil {
		return 0, errors.New("RSS subscription progress reconciliation storage is unavailable")
	}
	readiness, err := model.queries.GetRSSSubscriptionProgressReadiness(ctx, rssSubscriptionProgressModelVersion)
	if err != nil {
		return 0, fmt.Errorf("inspect RSS subscription progress readiness: %w", err)
	}
	if readiness.HasNewerModel {
		return 0, fmt.Errorf("RSS subscription progress model is newer than supported version %d", rssSubscriptionProgressModelVersion)
	}
	if _, err := model.queries.MarkOutdatedRSSSubscriptionProgressDirty(ctx, rssSubscriptionProgressModelVersion); err != nil {
		return 0, fmt.Errorf("mark outdated RSS subscription progress: %w", err)
	}

	total := 0
	for {
		batchCount := 0
		err := model.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
			rows, err := scope.Queries.LockDirtyRSSSubscriptionProgress(ctx, rssSubscriptionProgressBatchSize)
			if err != nil {
				return fmt.Errorf("lock dirty RSS subscription progress: %w", err)
			}
			candidates := make([]rssSubscriptionProgressCandidate, 0, len(rows))
			for _, row := range rows {
				candidates = append(candidates, rssSubscriptionProgressCandidate{
					subscriptionID: repository.UUIDFromPG(row.SubscriptionID),
					sourceRevision: row.SourceRevision,
					modelVersion:   row.ModelVersion,
					completed:      row.CompletedAt.Valid,
				})
			}
			if err := recalculateRSSSubscriptionProgress(ctx, scope.Queries, candidates); err != nil {
				return err
			}
			batchCount = len(candidates)
			return nil
		})
		if err != nil {
			return total, fmt.Errorf("reconcile RSS subscription progress: %w", err)
		}
		total += batchCount
		if batchCount > 0 {
			continue
		}

		readiness, err = model.queries.GetRSSSubscriptionProgressReadiness(ctx, rssSubscriptionProgressModelVersion)
		if err != nil {
			return total, fmt.Errorf("inspect RSS subscription progress readiness: %w", err)
		}
		if readiness.HasNewerModel {
			return total, fmt.Errorf("RSS subscription progress model is newer than supported version %d", rssSubscriptionProgressModelVersion)
		}
		if readiness.HasOutdatedModel {
			if _, err := model.queries.MarkOutdatedRSSSubscriptionProgressDirty(ctx, rssSubscriptionProgressModelVersion); err != nil {
				return total, fmt.Errorf("mark outdated RSS subscription progress: %w", err)
			}
			continue
		}
		if !readiness.HasDirty {
			return total, nil
		}
		// 另一实例可能正持有 SKIP LOCKED 行；等待其提交后再次确认，
		// 避免读请求在可见 dirty 行尚未收敛时继续分页。
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (model *rssSubscriptionProgressReadModel) reconcileIDs(
	ctx context.Context,
	subscriptionIDs []uuid.UUID,
) (int, error) {
	if len(subscriptionIDs) == 0 {
		return 0, nil
	}
	if model.queries == nil || model.transactor == nil {
		return 0, errors.New("RSS subscription progress reconciliation storage is unavailable")
	}
	pgIDs := uniquePGUUIDs(subscriptionIDs)
	if _, err := model.queries.MarkOutdatedRSSSubscriptionProgressByIDsDirty(ctx, db.MarkOutdatedRSSSubscriptionProgressByIDsDirtyParams{
		SubscriptionIds: pgIDs,
		ModelVersion:    rssSubscriptionProgressModelVersion,
	}); err != nil {
		return 0, fmt.Errorf("mark outdated RSS subscription progress: %w", err)
	}

	recalculated := 0
	err := model.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		rows, err := scope.Queries.LockDirtyRSSSubscriptionProgressByIDs(ctx, pgIDs)
		if err != nil {
			return fmt.Errorf("lock RSS subscription progress: %w", err)
		}
		candidates := make([]rssSubscriptionProgressCandidate, 0, len(rows))
		for _, row := range rows {
			candidates = append(candidates, rssSubscriptionProgressCandidate{
				subscriptionID: repository.UUIDFromPG(row.SubscriptionID),
				sourceRevision: row.SourceRevision,
				modelVersion:   row.ModelVersion,
				completed:      row.CompletedAt.Valid,
			})
		}
		if err := recalculateRSSSubscriptionProgress(ctx, scope.Queries, candidates); err != nil {
			return err
		}
		recalculated = len(candidates)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("reconcile RSS subscription progress: %w", err)
	}
	return recalculated, nil
}

func recalculateRSSSubscriptionProgress(
	ctx context.Context,
	queries *db.Queries,
	candidates []rssSubscriptionProgressCandidate,
) error {
	if len(candidates) == 0 {
		return nil
	}
	subscriptionIDs := make([]uuid.UUID, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.modelVersion > rssSubscriptionProgressModelVersion {
			return fmt.Errorf(
				"RSS subscription progress %s uses newer model version %d",
				candidate.subscriptionID,
				candidate.modelVersion,
			)
		}
		subscriptionIDs = append(subscriptionIDs, candidate.subscriptionID)
	}
	progressBySubscription, err := subscriptionProgressBySubscriptionsWithQueries(ctx, queries, subscriptionIDs)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		progress := progressBySubscription[candidate.subscriptionID]
		if candidate.completed {
			progress.overallProgress = 1
		}
		taskCount, err := rssSubscriptionProgressCount(progress.taskCount)
		if err != nil {
			return fmt.Errorf("subscription %s task count: %w", candidate.subscriptionID, err)
		}
		completedTaskCount, err := rssSubscriptionProgressCount(progress.completedTaskCount)
		if err != nil {
			return fmt.Errorf("subscription %s completed task count: %w", candidate.subscriptionID, err)
		}
		attentionTaskCount, err := rssSubscriptionProgressCount(progress.attentionTaskCount)
		if err != nil {
			return fmt.Errorf("subscription %s attention task count: %w", candidate.subscriptionID, err)
		}
		_, err = queries.UpdateRSSSubscriptionProgress(ctx, db.UpdateRSSSubscriptionProgressParams{
			OverallProgress:        progress.overallProgress,
			TaskCount:              taskCount,
			CompletedTaskCount:     completedTaskCount,
			AttentionTaskCount:     attentionTaskCount,
			ModelVersion:           rssSubscriptionProgressModelVersion,
			SubscriptionID:         repository.UUIDToPG(candidate.subscriptionID),
			ExpectedSourceRevision: candidate.sourceRevision,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("RSS subscription progress revision changed for %s", candidate.subscriptionID)
		}
		if err != nil {
			return fmt.Errorf("update RSS subscription progress for %s: %w", candidate.subscriptionID, err)
		}
	}
	return nil
}

func (workflow *RSSWorkflow) persistedSubscriptionProgressByIDs(
	ctx context.Context,
	subscriptionIDs []uuid.UUID,
) (map[uuid.UUID]rssSubscriptionProgress, error) {
	result := make(map[uuid.UUID]rssSubscriptionProgress, len(subscriptionIDs))
	if len(subscriptionIDs) == 0 {
		return result, nil
	}
	if _, err := workflow.progressReadModel.reconcileIDs(ctx, subscriptionIDs); err != nil {
		return nil, err
	}
	rows, err := workflow.queries.ListRSSSubscriptionProgressByIDs(ctx, uniquePGUUIDs(subscriptionIDs))
	if err != nil {
		return nil, fmt.Errorf("list persisted RSS subscription progress: %w", err)
	}
	for _, row := range rows {
		progress, err := rssSubscriptionProgressFromRow(row)
		if err != nil {
			return nil, err
		}
		result[repository.UUIDFromPG(row.SubscriptionID)] = progress
	}
	for _, subscriptionID := range subscriptionIDs {
		if _, ok := result[subscriptionID]; !ok {
			return nil, fmt.Errorf("persisted RSS subscription progress is missing for %s", subscriptionID)
		}
	}
	return result, nil
}

func (workflow *RSSWorkflow) persistedSubscriptionProgress(
	ctx context.Context,
	subscriptionID uuid.UUID,
) (rssSubscriptionProgress, error) {
	progressByID, err := workflow.persistedSubscriptionProgressByIDs(ctx, []uuid.UUID{subscriptionID})
	if err != nil {
		return rssSubscriptionProgress{}, err
	}
	return progressByID[subscriptionID], nil
}

func rssSubscriptionProgressFromRow(row db.RssSubscriptionProgress) (rssSubscriptionProgress, error) {
	if row.Dirty || row.ModelVersion != rssSubscriptionProgressModelVersion ||
		row.CalculatedRevision != row.SourceRevision || !row.CalculatedAt.Valid {
		return rssSubscriptionProgress{}, fmt.Errorf("RSS subscription progress %s is not reconciled", repository.UUIDFromPG(row.SubscriptionID))
	}
	return rssSubscriptionProgress{
		overallProgress:    row.OverallProgress,
		taskCount:          int(row.TaskCount),
		completedTaskCount: int(row.CompletedTaskCount),
		attentionTaskCount: int(row.AttentionTaskCount),
	}, nil
}

func rssSubscriptionProgressCount(value int) (int32, error) {
	if value < 0 || value > math.MaxInt32 {
		return 0, fmt.Errorf("%d is outside the int32 response range", value)
	}
	return int32(value), nil
}

func uniquePGUUIDs(ids []uuid.UUID) []pgtype.UUID {
	result := make([]pgtype.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, repository.UUIDToPG(id))
	}
	return result
}
