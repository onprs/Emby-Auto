package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
)

// ReconcileSubtitleVideoMatchTask 收敛 Agent 字幕匹配决议的终态。
//
// 只有当前租约 owner 在本事务中成功从 active 转为终态（调用方已用
// operation_id/version/status CAS 更新，并把返回行传进来）才允许过期 scope
// 和标记任务终态。这里再在 S/T→R 锁序下重读 R，核对 operation/version/status；
// 任一不匹配或存在其他活动 owner 时无副作用退出，因此旧 terminal、CAS 0 行
// 或缓存对象都不能回收新 scope/新任务。
func ReconcileSubtitleVideoMatchTask(
	ctx context.Context,
	queries *db.Queries,
	scopeID pgtype.UUID,
	resolutionID pgtype.UUID,
	expectedOperationID pgtype.UUID,
	expectedVersion int32,
	status string,
	code string,
	message string,
	operationID pgtype.UUID,
) error {
	if !scopeID.Valid || !resolutionID.Valid || !expectedOperationID.Valid {
		return nil
	}
	scope, err := queries.LockSubtitleVideoMatchScopeTask(ctx, scopeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock subtitle video match task: %w", err)
	}
	if scope.Status != "pending" {
		return nil
	}
	locked, err := queries.LockAgentResolution(ctx, resolutionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock subtitle video match resolution: %w", err)
	}
	// 核对本事务中确实完成的 active→terminal 转换；旧行/0 行/缓存 terminal
	// 不能凭“暂时没有其它 active”回收 scope。
	if UUIDFromPG(locked.OperationID) != UUIDFromPG(expectedOperationID) ||
		locked.Version != expectedVersion || locked.Status != status {
		return nil
	}
	switch status {
	case "failed", "cancelled", "expired", "rejected", "applied", "review_required":
	default:
		return nil
	}
	owners, err := queries.ListSubtitleVideoMatchActiveOwners(ctx, scopeID)
	if err != nil {
		return fmt.Errorf("list subtitle video match active owners: %w", err)
	}
	if len(owners) != 0 {
		// 还有其它活动 owner：当前终态不能回收 scope，让新 owner 继续完成。
		return nil
	}
	taskID, err := queries.ExpireSubtitleVideoMatchScope(ctx, scopeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("expire subtitle video match scope: %w", err)
	}
	topic := "task.media_failed"
	var task db.EpisodeTask
	if status == "cancelled" {
		task, err = queries.MarkTaskSubtitleCancelled(ctx, taskID)
		topic = "task.media_cancelled"
	} else {
		task, err = queries.MarkTaskSubtitleTerminalFailure(ctx, db.MarkTaskSubtitleTerminalFailureParams{
			ErrorCode: &code, ErrorMessage: &message, ID: taskID,
		})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mark subtitle video match task terminal: %w", err)
	}
	return appendTaskLifecycleEvent(ctx, queries, task.ID, operationID, topic, map[string]any{
		"kind": "agent.resolve", "status": status, "errorCode": code,
	})
}
