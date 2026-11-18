package maintenance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

const (
	// MinimumHistoryRetentionAge 阻止维护命令清理仍可能被排查的近期记录。
	MinimumHistoryRetentionAge = 24 * time.Hour
	// MaxHistoryRetentionBatchSize 限制单事务删除行数，避免长事务与大批量锁。
	MaxHistoryRetentionBatchSize int32 = 5000
	// MaxHistoryRetentionBatches 限制单次命令的总工作量，积压由后续执行继续清理。
	MaxHistoryRetentionBatches = 100
)

// HistoryRetentionRequest 描述一次有界的历史清理。Apply 为 false 时只统计候选。
type HistoryRetentionRequest struct {
	Before     time.Time
	BatchSize  int32
	MaxBatches int
	Apply      bool
}

// HistoryRetentionResult 汇总候选与已删除行数，便于维护记录和审计。
type HistoryRetentionResult struct {
	Before               time.Time `json:"before"`
	Apply                bool      `json:"apply"`
	OperationCandidates  int64     `json:"operationCandidates"`
	ResolutionCandidates int64     `json:"resolutionCandidates"`
	OperationsDeleted    int64     `json:"operationsDeleted"`
	ResolutionsDeleted   int64     `json:"resolutionsDeleted"`
	Batches              int       `json:"batches"`
}

// HistoryRetention 分批清理终态历史操作与 Agent 决议。候选规则由数据库视图
// 统一维护：活跃状态、扫描历史和所有 RESTRICT 引用都会排除对应行。
type HistoryRetention struct {
	queries    *db.Queries
	transactor *database.Transactor
	now        func() time.Time
}

func NewHistoryRetention(queries *db.Queries, transactor *database.Transactor) *HistoryRetention {
	return &HistoryRetention{queries: queries, transactor: transactor, now: time.Now}
}

func ValidateHistoryRetentionRequest(request HistoryRetentionRequest, now time.Time) error {
	if request.Before.IsZero() || !request.Before.Before(now.Add(-MinimumHistoryRetentionAge)) {
		return fmt.Errorf("before must be at least %s in the past", MinimumHistoryRetentionAge)
	}
	if request.BatchSize <= 0 || request.BatchSize > MaxHistoryRetentionBatchSize {
		return fmt.Errorf("batch size must be between 1 and %d", MaxHistoryRetentionBatchSize)
	}
	if request.MaxBatches <= 0 || request.MaxBatches > MaxHistoryRetentionBatches {
		return fmt.Errorf("max batches must be between 1 and %d", MaxHistoryRetentionBatches)
	}
	return nil
}

func (retention *HistoryRetention) Run(ctx context.Context, request HistoryRetentionRequest) (HistoryRetentionResult, error) {
	result := HistoryRetentionResult{Before: request.Before.UTC(), Apply: request.Apply}
	if retention == nil || retention.queries == nil || retention.transactor == nil {
		return result, errors.New("history retention is not configured")
	}
	if err := ValidateHistoryRetentionRequest(request, retention.now()); err != nil {
		return result, err
	}
	before := pgtype.Timestamptz{Time: request.Before.UTC(), Valid: true}
	operationCandidates, err := retention.queries.CountExpiredOperations(ctx, before)
	if err != nil {
		return result, fmt.Errorf("count expired operations: %w", err)
	}
	resolutionCandidates, err := retention.queries.CountExpiredAgentResolutions(ctx, before)
	if err != nil {
		return result, fmt.Errorf("count expired agent resolutions: %w", err)
	}
	result.OperationCandidates = operationCandidates
	result.ResolutionCandidates = resolutionCandidates
	if !request.Apply {
		return result, nil
	}
	for range request.MaxBatches {
		var deletedOperations, deletedResolutions int64
		err := retention.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
			var err error
			// 先删除决议，使仅被已过期决议引用的操作在下一批成为候选。
			deletedResolutions, err = scope.Queries.DeleteExpiredAgentResolutions(ctx, db.DeleteExpiredAgentResolutionsParams{
				Before:  before,
				MaxRows: request.BatchSize,
			})
			if err != nil {
				return fmt.Errorf("delete expired agent resolutions: %w", err)
			}
			deletedOperations, err = scope.Queries.DeleteExpiredOperations(ctx, db.DeleteExpiredOperationsParams{
				Before:  before,
				MaxRows: request.BatchSize,
			})
			if err != nil {
				return fmt.Errorf("delete expired operations: %w", err)
			}
			return nil
		})
		if err != nil {
			return result, err
		}
		result.ResolutionsDeleted += deletedResolutions
		result.OperationsDeleted += deletedOperations
		result.Batches++
		if deletedResolutions < int64(request.BatchSize) && deletedOperations < int64(request.BatchSize) {
			break
		}
	}
	return result, nil
}
