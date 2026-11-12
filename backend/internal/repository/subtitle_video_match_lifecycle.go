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
// 字幕匹配等待期间任务字幕分支停留在 extracting_or_converting，而
// subtitle.prepare 操作已经成功结束；若 Agent 决议失败或取消而不回写任务，
// 任务会永久停留在 processing。这里过期仍处于 pending 的匹配范围，并把
// 任务字幕分支标记为失败或取消。任务重试时
// MediaWorkflow.CreateSubtitleVideoMatchScope 会复用已过期范围并换用新的候选集，
// 因此不会重新命中同一个等待中的范围。expectedTaskVersion 非空时，只有任务
// 版本仍与失败决议一致才收敛；旧 generation 的失败不得回收新范围或新任务。
func ReconcileSubtitleVideoMatchTask(
	ctx context.Context,
	queries *db.Queries,
	scopeID pgtype.UUID,
	status string,
	code string,
	message string,
	expectedTaskVersion *int32,
	operationID pgtype.UUID,
) error {
	if !scopeID.Valid {
		return nil
	}
	if expectedTaskVersion != nil {
		currentVersion, err := queries.GetSubtitleVideoMatchTaskVersion(ctx, scopeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load subtitle video match task version: %w", err)
		}
		if currentVersion != *expectedTaskVersion {
			// 任务版本已经推进，失败的是旧 generation；不能回收新范围或失败新任务。
			return nil
		}
	}
	taskID, err := queries.ExpireSubtitleVideoMatchScope(ctx, scopeID)
	if errors.Is(err, pgx.ErrNoRows) {
		// 匹配范围已应用或已过期时没有等待中的任务需要收敛。
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
