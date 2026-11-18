package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/subtitlebinding"
)

var (
	errSubtitleBindingObservationRace = errors.New("subtitle binding observation changed during start")
	errSubtitleBindingRecoveryRetry   = errors.New("subtitle binding missing; retry the attempt within the existing budget")
	errSubtitleAttemptStale           = errors.New("subtitle attempt is no longer current")
)

func writeSubtitleStartBinding(
	ctx context.Context,
	queries *db.Queries,
	operation db.Operation,
	handle subtitlebinding.AttemptHandle,
) error {
	snapshot, err := LoadSubtitleSnapshotLocked(ctx, queries, UUIDFromPG(operation.ResourceID))
	if err != nil {
		return err
	}
	observation, err := subtitlebinding.ObservationFromSnapshot(snapshot)
	if err != nil {
		return fmt.Errorf("build subtitle start observation: %w", err)
	}
	binding := subtitlebinding.NewStartBinding(handle, observation)
	raw, err := subtitlebinding.Encode(binding)
	if err != nil {
		return fmt.Errorf("encode subtitle start binding: %w", err)
	}
	if _, err := queries.UpsertOperationSubtitleBinding(ctx, db.UpsertOperationSubtitleBindingParams{
		OperationID:           UUIDToPG(handle.OperationID),
		BindingVersion:        subtitlebinding.Version,
		Phase:                 subtitlebinding.PhaseStarted,
		Attempt:               int32(handle.JobAttempt),
		AttemptID:             UUIDToPG(handle.AttemptID),
		OperationAttemptCount: int32(handle.OperationAttemptCount),
		Binding:               raw,
	}); err != nil {
		return fmt.Errorf("write subtitle start binding: %w", err)
	}
	return nil
}

func LoadSubtitleSnapshotLocked(ctx context.Context, queries *db.Queries, taskID uuid.UUID) (subtitlebinding.Snapshot, error) {
	scopeID, err := queries.GetSubtitleVideoMatchScopeID(ctx, UUIDToPG(taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		task, err := queries.LockEpisodeTask(ctx, UUIDToPG(taskID))
		if err != nil {
			return subtitlebinding.Snapshot{}, fmt.Errorf("lock task for subtitle binding: %w", err)
		}
		if _, err := queries.GetSubtitleVideoMatchScopeID(ctx, UUIDToPG(taskID)); !errors.Is(err, pgx.ErrNoRows) {
			if err != nil {
				return subtitlebinding.Snapshot{}, fmt.Errorf("re-read subtitle scope: %w", err)
			}
			return subtitlebinding.Snapshot{}, errSubtitleBindingObservationRace
		}
		return subtitlebinding.Snapshot{
			TaskVersion:   int(task.Version),
			TaskState:     task.State,
			SubtitleState: task.SubtitleState,
			VideoState:    task.VideoState,
			FailureStage:  stringValue(task.FailureStage),
		}, nil
	}
	if err != nil {
		return subtitlebinding.Snapshot{}, fmt.Errorf("read subtitle scope: %w", err)
	}
	contextRow, err := queries.GetSubtitleVideoMatchContext(ctx, scopeID)
	if err != nil {
		return subtitlebinding.Snapshot{}, fmt.Errorf("lock subtitle scope context: %w", err)
	}
	candidates, err := queries.ListSubtitleVideoMatchCandidates(ctx, scopeID)
	if err != nil {
		return subtitlebinding.Snapshot{}, fmt.Errorf("list subtitle candidates: %w", err)
	}
	owners, err := queries.ListSubtitleVideoMatchActiveOwners(ctx, scopeID)
	if err != nil {
		return subtitlebinding.Snapshot{}, fmt.Errorf("list subtitle owners: %w", err)
	}
	return snapshotFromContext(contextRow, candidates, len(owners)), nil
}

func snapshotFromContext(
	row db.GetSubtitleVideoMatchContextRow,
	candidates []db.ListSubtitleVideoMatchCandidatesRow,
	ownerCount int,
) subtitlebinding.Snapshot {
	return subtitlebinding.Snapshot{
		ScopePresent:        true,
		ScopeID:             UUIDFromPG(row.ScopeID),
		ScopeStatus:         row.ScopeStatus,
		OwnerCount:          ownerCount,
		SelectedCandidateID: stringValue(row.SelectedCandidateID),
		AppliedResolutionID: UUIDFromPG(row.AgentResolutionID),
		Content: subtitlebinding.Content{
			TaskID:                  UUIDFromPG(row.TaskID),
			SeriesTitle:             row.SeriesTitle,
			TargetSeason:            intFromInt32(row.TargetSeasonNumber),
			TargetEpisode:           intFromInt32(row.TargetEpisodeNumber),
			TargetTitle:             stringValue(row.TargetEpisodeTitle),
			SourceVideoRelativePath: row.SourceVideoRelativePath,
			Candidates:              subtitleBindingCandidates(candidates),
		},
		TaskVersion:   int(row.TaskVersion),
		TaskState:     row.TaskState,
		SubtitleState: row.SubtitleState,
		VideoState:    row.VideoState,
		FailureStage:  stringValue(row.FailureStage),
	}
}

func subtitleBindingCandidates(rows []db.ListSubtitleVideoMatchCandidatesRow) []subtitlebinding.Candidate {
	result := make([]subtitlebinding.Candidate, 0, len(rows))
	for _, row := range rows {
		result = append(result, subtitlebinding.Candidate{
			CandidateID: row.CandidateID,
			Source:      row.Source,
			StreamIndex: intFromInt32(row.StreamIndex),
			Format:      stringValue(row.Format),
			Language:    stringValue(row.Language),
			Title:       stringValue(row.Title),
			Path:        stringValue(row.Path),
		})
	}
	return result
}

func ReadSubtitleBinding(ctx context.Context, queries *db.Queries, operationID uuid.UUID) (subtitlebinding.Binding, db.OperationSubtitleBinding, error) {
	row, err := queries.GetOperationSubtitleBinding(ctx, UUIDToPG(operationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return subtitlebinding.Binding{}, db.OperationSubtitleBinding{}, err
	}
	if err != nil {
		return subtitlebinding.Binding{}, db.OperationSubtitleBinding{}, err
	}
	binding, err := subtitlebinding.Decode(row.Binding, row.BindingVersion, row.Phase, row.Attempt, UUIDFromPG(row.AttemptID), row.OperationAttemptCount)
	if err != nil {
		return subtitlebinding.Binding{}, row, err
	}
	if binding.OperationID != operationID || UUIDFromPG(row.OperationID) != operationID {
		return subtitlebinding.Binding{}, row, subtitlebinding.ErrUnsupported
	}
	return binding, row, nil
}

func SubtitleBindingMatchesSnapshot(binding subtitlebinding.Binding, snapshot subtitlebinding.Snapshot, currentTask string) bool {
	latest := binding.LatestObservation()
	if !snapshot.ScopePresent {
		return !latest.ScopePresent
	}
	contentHash, hashErr := subtitlebinding.HashContent(snapshot.Content)
	if hashErr != nil {
		return false
	}
	if !latest.ScopePresent || latest.ScopeID != snapshot.ScopeID || latest.ContentHash != contentHash {
		return false
	}
	if latest.OwnerCount != snapshot.OwnerCount {
		return false
	}
	switch snapshot.ScopeStatus {
	case "pending", "expired":
		if latest.ScopeStatus != "pending" && latest.ScopeStatus != "expired" {
			return false
		}
		if latest.SelectedCandidateID != "" || latest.AppliedResolutionID != uuid.Nil {
			return false
		}
		if snapshot.SelectedCandidateID != "" || snapshot.AppliedResolutionID != uuid.Nil {
			return false
		}
	case "applied":
		if latest.ScopeStatus != "applied" {
			return false
		}
		if latest.SelectedCandidateID != snapshot.SelectedCandidateID || latest.AppliedResolutionID != snapshot.AppliedResolutionID {
			return false
		}
	default:
		return false
	}
	task := binding.TaskObservation()
	return task.SubtitleState == currentTask
}

type subtitleTerminalDecision int

const (
	subtitleTerminalAllow subtitleTerminalDecision = iota
	subtitleTerminalProtect
	subtitleTerminalMissingRetry
)

func decideSubtitleTerminal(
	ctx context.Context,
	queries *db.Queries,
	operation db.Operation,
	handle *subtitlebinding.AttemptHandle,
) (subtitleTerminalDecision, error) {
	snapshot, err := LoadSubtitleSnapshotLocked(ctx, queries, UUIDFromPG(operation.ResourceID))
	if err != nil {
		return subtitleTerminalProtect, err
	}
	if snapshot.TaskState == "cancelled" || snapshot.SubtitleState == "cancelled" {
		return subtitleTerminalProtect, nil
	}
	var binding subtitlebinding.Binding
	var bindingErr error
	if handle != nil {
		if handle.OperationID != UUIDFromPG(operation.ID) || handle.ResourceID != UUIDFromPG(operation.ResourceID) || handle.ResourceType != valueOrEmpty(operation.ResourceType) {
			return subtitleTerminalProtect, errSubtitleAttemptStale
		}
		binding, _, bindingErr = ReadSubtitleBinding(ctx, queries, UUIDFromPG(operation.ID))
	} else {
		binding, _, bindingErr = ReadSubtitleBinding(ctx, queries, UUIDFromPG(operation.ID))
	}
	missing := bindingErr != nil
	ownMatches := false
	if !missing {
		ownMatches = SubtitleBindingMatchesSnapshot(binding, snapshot, snapshot.SubtitleState)
	}
	if snapshot.OwnerCount > 0 {
		return subtitleTerminalProtect, nil
	}
	rows, err := queries.ListOtherActiveSubtitlePrepareOperations(ctx, db.ListOtherActiveSubtitlePrepareOperationsParams{
		ResourceID: operation.ResourceID, OperationID: operation.ID,
	})
	if err != nil {
		return subtitleTerminalProtect, fmt.Errorf("list other subtitle operations: %w", err)
	}
	queuedOther := false
	matchingOther := false
	for _, row := range rows {
		actionable := !row.CancelRequestedAt.Valid &&
			(row.Status == "running" || (row.Status == "queued" && row.AttemptCount < row.MaxAttempts))
		if !actionable {
			continue
		}
		if row.Status == "queued" {
			queuedOther = true
			continue
		}
		if row.BindingVersion == nil || row.Phase == nil || row.Attempt == nil || row.OperationAttemptCount == nil || len(row.Binding) == 0 {
			continue
		}
		other, err := subtitlebinding.Decode(row.Binding, *row.BindingVersion, *row.Phase, *row.Attempt, UUIDFromPG(row.AttemptID), *row.OperationAttemptCount)
		if err != nil {
			continue
		}
		if SubtitleBindingMatchesSnapshot(other, snapshot, snapshot.SubtitleState) {
			matchingOther = true
		}
	}
	if ownMatches {
		return subtitleTerminalAllow, nil
	}
	// binding 仍对应同一 pending 内容时是语义等价孤儿，可安全终态；
	// binding 之后 scope 变为 applied，或本人已有 Scope receipt 后 pending
	// 的内容/身份发生变化，说明存在新代次/正式后续，旧 operation 不能越过
	// 它终态。本人尚未提交 scope、仅外部出现 pending 时保持原安全终态，
	// missing binding 也不走此分支。
	if !missing && snapshot.ScopePresent {
		if snapshot.ScopeStatus == "applied" {
			return subtitleTerminalProtect, nil
		}
		if snapshot.ScopeStatus == "pending" && binding.Scope != nil {
			latest := binding.LatestObservation()
			currentHash, hashErr := subtitlebinding.HashContent(snapshot.Content)
			if hashErr != nil {
				return subtitleTerminalProtect, hashErr
			}
			if !latest.ScopePresent || latest.ScopeID != snapshot.ScopeID || latest.ContentHash != currentHash {
				return subtitleTerminalProtect, nil
			}
		}
	}
	if matchingOther || queuedOther {
		return subtitleTerminalProtect, nil
	}
	if missing && handle == nil {
		return subtitleTerminalProtect, nil
	}
	return subtitleTerminalAllow, nil
}

func decideSubtitleCancel(
	ctx context.Context,
	queries *db.Queries,
	operation db.Operation,
	handle *subtitlebinding.AttemptHandle,
) (subtitleTerminalDecision, error) {
	if handle == nil {
		return subtitleTerminalProtect, nil
	}
	snapshot, err := LoadSubtitleSnapshotLocked(ctx, queries, UUIDFromPG(operation.ResourceID))
	if err != nil {
		return subtitleTerminalProtect, err
	}
	if snapshot.TaskState == "cancelled" || snapshot.SubtitleState == "cancelled" {
		return subtitleTerminalProtect, nil
	}
	binding, _, bindingErr := ReadSubtitleBinding(ctx, queries, UUIDFromPG(operation.ID))
	if bindingErr != nil {
		return subtitleTerminalProtect, nil
	}
	if !SubtitleBindingMatchesSnapshot(binding, snapshot, snapshot.SubtitleState) {
		return subtitleTerminalProtect, nil
	}
	if snapshot.OwnerCount > 0 {
		return subtitleTerminalProtect, nil
	}
	rows, err := queries.ListOtherActiveSubtitlePrepareOperations(ctx, db.ListOtherActiveSubtitlePrepareOperationsParams{
		ResourceID: operation.ResourceID, OperationID: operation.ID,
	})
	if err != nil {
		return subtitleTerminalProtect, fmt.Errorf("list other subtitle operations: %w", err)
	}
	for _, row := range rows {
		actionable := !row.CancelRequestedAt.Valid &&
			(row.Status == "running" || (row.Status == "queued" && row.AttemptCount < row.MaxAttempts))
		if !actionable {
			continue
		}
		if row.Status == "queued" {
			return subtitleTerminalProtect, nil
		}
		if row.BindingVersion == nil || row.Phase == nil || row.Attempt == nil || row.OperationAttemptCount == nil || len(row.Binding) == 0 {
			continue
		}
		other, err := subtitlebinding.Decode(row.Binding, *row.BindingVersion, *row.Phase, *row.Attempt, UUIDFromPG(row.AttemptID), *row.OperationAttemptCount)
		if err != nil {
			continue
		}
		if SubtitleBindingMatchesSnapshot(other, snapshot, snapshot.SubtitleState) {
			return subtitleTerminalProtect, nil
		}
	}
	return subtitleTerminalAllow, nil
}

func (lifecycle *OperationLifecycle) FailSubtitleAttempt(
	ctx context.Context,
	handle subtitlebinding.AttemptHandle,
	code string,
	message string,
	retryable bool,
) error {
	return lifecycle.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockOperation(ctx, UUIDToPG(handle.OperationID))
		if err != nil {
			return fmt.Errorf("lock subtitle operation: %w", err)
		}
		if locked.Kind != "subtitle.prepare" || valueOrEmpty(locked.ResourceType) != handle.ResourceType || UUIDFromPG(locked.ResourceID) != handle.ResourceID {
			return errSubtitleAttemptStale
		}
		if int(locked.AttemptCount) != handle.OperationAttemptCount || locked.Status != "running" {
			return nil
		}
		finished, err := scope.Queries.FinishOperationAttemptByID(ctx, db.FinishOperationAttemptByIDParams{
			Status: "failed", ErrorCode: &code, ErrorMessage: &message,
			ID: UUIDToPG(handle.AttemptID), OperationID: UUIDToPG(handle.OperationID), Attempt: int32(handle.JobAttempt),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("finish subtitle attempt: %w", err)
		}
		if UUIDFromPG(finished.ID) != handle.AttemptID {
			return errSubtitleAttemptStale
		}
		if locked.CancelRequestedAt.Valid {
			operation, err := scope.Queries.CancelOperation(ctx, UUIDToPG(handle.OperationID))
			if err != nil {
				return fmt.Errorf("cancel subtitle operation after request: %w", err)
			}
			if err := lifecycle.cancelSubtitleResource(ctx, scope.Queries, operation, &handle); err != nil {
				return err
			}
			return appendOperationEvent(ctx, scope.Queries, operation, "operation.cancelled", map[string]any{"attempt": handle.JobAttempt})
		}
		if retryable && locked.AttemptCount < locked.MaxAttempts {
			operation, err := scope.Queries.RetryOperation(ctx, db.RetryOperationParams{
				ErrorCode: &code, ErrorMessage: &message, ID: UUIDToPG(handle.OperationID),
			})
			if err != nil {
				return fmt.Errorf("retry subtitle operation: %w", err)
			}
			return appendOperationEvent(ctx, scope.Queries, operation, "operation.retry_scheduled", map[string]any{
				"attempt": handle.JobAttempt, "errorCode": code,
			})
		}
		decision, err := func() (subtitleTerminalDecision, error) {
			binding, _, bindingErr := ReadSubtitleBinding(ctx, scope.Queries, handle.OperationID)
			if bindingErr != nil {
				if locked.AttemptCount < locked.MaxAttempts {
					return subtitleTerminalMissingRetry, nil
				}
				return decideSubtitleTerminal(ctx, scope.Queries, locked, &handle)
			}
			_ = binding
			return decideSubtitleTerminal(ctx, scope.Queries, locked, &handle)
		}()
		if err != nil {
			return err
		}
		if decision == subtitleTerminalMissingRetry {
			return errSubtitleBindingRecoveryRetry
		}
		if decision == subtitleTerminalAllow {
			if err := markSubtitleTaskFailed(ctx, scope.Queries, locked, handle.OperationAttemptCount, code, message); err != nil {
				return err
			}
		}
		operation, err := scope.Queries.FailOperation(ctx, db.FailOperationParams{
			ErrorCode: &code, ErrorMessage: &message, ID: UUIDToPG(handle.OperationID),
		})
		if err != nil {
			return fmt.Errorf("fail subtitle operation: %w", err)
		}
		return appendOperationEvent(ctx, scope.Queries, operation, "operation.failed", map[string]any{
			"attempt": handle.JobAttempt, "errorCode": code,
		})
	})
}

func markSubtitleTaskFailed(ctx context.Context, queries *db.Queries, operation db.Operation, attemptCount int, code, message string) error {
	task, err := queries.MarkTaskSubtitleTerminalFailure(ctx, db.MarkTaskSubtitleTerminalFailureParams{
		ErrorCode: &code, ErrorMessage: &message, ID: operation.ResourceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mark subtitle task failed: %w", err)
	}
	if err := appendTaskLifecycleEvent(ctx, queries, task.ID, operation.ID, "task.media_failed", map[string]any{
		"kind": operation.Kind, "status": "failed", "errorCode": code, "attempt": attemptCount,
	}); err != nil {
		return err
	}
	return nil
}

func (lifecycle *OperationLifecycle) CancelSubtitleAttempt(
	ctx context.Context,
	handle subtitlebinding.AttemptHandle,
) error {
	return lifecycle.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockOperation(ctx, UUIDToPG(handle.OperationID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock subtitle operation for cancel: %w", err)
		}
		if !subtitleOperationCurrentForCancel(locked, handle) {
			return nil
		}
		finished, err := scope.Queries.FinishOperationAttemptByID(ctx, db.FinishOperationAttemptByIDParams{
			Status: "cancelled", ID: UUIDToPG(handle.AttemptID),
			OperationID: UUIDToPG(handle.OperationID), Attempt: int32(handle.JobAttempt),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("finish cancelled subtitle attempt: %w", err)
		}
		if UUIDFromPG(finished.ID) != handle.AttemptID {
			return nil
		}
		operation, err := scope.Queries.CancelOperation(ctx, UUIDToPG(handle.OperationID))
		if err != nil {
			return fmt.Errorf("cancel subtitle operation: %w", err)
		}
		if err := lifecycle.cancelSubtitleResource(ctx, scope.Queries, operation, &handle); err != nil {
			return err
		}
		return appendOperationEvent(ctx, scope.Queries, operation, "operation.cancelled", map[string]any{"attempt": handle.JobAttempt})
	})
}

func subtitleOperationCurrent(operation db.Operation, handle subtitlebinding.AttemptHandle) bool {
	return operation.Kind == "subtitle.prepare" &&
		valueOrEmpty(operation.ResourceType) == handle.ResourceType &&
		UUIDFromPG(operation.ResourceID) == handle.ResourceID &&
		operation.Status == "running" &&
		!operation.CancelRequestedAt.Valid &&
		int(operation.AttemptCount) == handle.OperationAttemptCount
}

func subtitleOperationCurrentForCancel(operation db.Operation, handle subtitlebinding.AttemptHandle) bool {
	return operation.Kind == "subtitle.prepare" &&
		valueOrEmpty(operation.ResourceType) == handle.ResourceType &&
		UUIDFromPG(operation.ResourceID) == handle.ResourceID &&
		operation.Status == "running" &&
		int(operation.AttemptCount) == handle.OperationAttemptCount
}

func (lifecycle *OperationLifecycle) cancelSubtitleResource(ctx context.Context, queries *db.Queries, operation db.Operation, handle *subtitlebinding.AttemptHandle) error {
	decision, err := decideSubtitleCancel(ctx, queries, operation, handle)
	if err != nil {
		return err
	}
	if decision != subtitleTerminalAllow {
		return nil
	}
	task, err := queries.MarkTaskSubtitleCancelled(ctx, operation.ResourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cancel subtitle task: %w", err)
	}
	return appendTaskLifecycleEvent(ctx, queries, task.ID, operation.ID, "task.media_cancelled", map[string]any{
		"kind": operation.Kind, "status": "cancelled",
	})
}

func (lifecycle *OperationLifecycle) SucceedSubtitleAttempt(ctx context.Context, handle subtitlebinding.AttemptHandle) error {
	return lifecycle.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockOperation(ctx, UUIDToPG(handle.OperationID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock subtitle operation for success: %w", err)
		}
		if !subtitleOperationCurrent(locked, handle) {
			return nil
		}
		finished, err := scope.Queries.FinishOperationAttemptByID(ctx, db.FinishOperationAttemptByIDParams{
			Status: "succeeded", ID: UUIDToPG(handle.AttemptID),
			OperationID: UUIDToPG(handle.OperationID), Attempt: int32(handle.JobAttempt),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("finish successful subtitle attempt: %w", err)
		}
		if UUIDFromPG(finished.ID) != handle.AttemptID {
			return nil
		}
		operation, err := scope.Queries.CompleteOperation(ctx, UUIDToPG(handle.OperationID))
		if err != nil {
			return fmt.Errorf("complete subtitle operation: %w", err)
		}
		return appendOperationEvent(ctx, scope.Queries, operation, "operation.succeeded", map[string]any{"attempt": handle.JobAttempt})
	})
}

func (lifecycle *OperationLifecycle) SnoozeSubtitleAttempt(ctx context.Context, handle subtitlebinding.AttemptHandle) error {
	return lifecycle.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockOperation(ctx, UUIDToPG(handle.OperationID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock subtitle operation for snooze: %w", err)
		}
		if !subtitleOperationCurrent(locked, handle) {
			return nil
		}
		deleted, err := scope.Queries.DeleteOperationAttemptByID(ctx, db.DeleteOperationAttemptByIDParams{
			ID: UUIDToPG(handle.AttemptID), OperationID: UUIDToPG(handle.OperationID), Attempt: int32(handle.JobAttempt),
		})
		if err != nil {
			return fmt.Errorf("delete snoozed subtitle attempt: %w", err)
		}
		if deleted == 0 {
			return nil
		}
		if _, err := scope.Queries.SnoozeOperation(ctx, UUIDToPG(handle.OperationID)); err != nil {
			return fmt.Errorf("snooze subtitle operation: %w", err)
		}
		return nil
	})
}

func (lifecycle *OperationLifecycle) HeartbeatSubtitleAttempt(ctx context.Context, handle subtitlebinding.AttemptHandle) (bool, error) {
	alive := false
	err := lifecycle.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockOperation(ctx, UUIDToPG(handle.OperationID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock subtitle operation for heartbeat: %w", err)
		}
		if !subtitleOperationCurrent(locked, handle) {
			return nil
		}
		attemptRows, err := scope.Queries.HeartbeatOperationAttemptByID(ctx, db.HeartbeatOperationAttemptByIDParams{
			ID: UUIDToPG(handle.AttemptID), OperationID: UUIDToPG(handle.OperationID), Attempt: int32(handle.JobAttempt),
		})
		if err != nil {
			return fmt.Errorf("heartbeat subtitle attempt: %w", err)
		}
		if attemptRows != 1 {
			return nil
		}
		operationRows, err := scope.Queries.HeartbeatOperation(ctx, UUIDToPG(handle.OperationID))
		if err != nil {
			return fmt.Errorf("heartbeat subtitle operation: %w", err)
		}
		if operationRows != 1 {
			// audit heartbeat 已在本 Tx 更新；operation heartbeat 0 行属于
			// 故障/过期 transition，不是业务取消。返回 error 让整个 Tx 回滚，
			// 不能提交只更新 audit 的半状态。
			return fmt.Errorf("heartbeat subtitle operation update did not apply")
		}
		alive = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return alive, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func intFromInt32(value *int32) int {
	if value == nil {
		return 0
	}
	return int(*value)
}
