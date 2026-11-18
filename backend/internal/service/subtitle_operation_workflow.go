package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/subtitlebinding"
)

func (workflow *MediaWorkflow) BeginSubtitleForOperation(
	ctx context.Context,
	handle subtitlebinding.AttemptHandle,
	taskID uuid.UUID,
) (domain.TaskMediaCommand, error) {
	if !handle.Valid() || handle.ResourceID != taskID {
		return domain.TaskMediaCommand{}, mediaWorkflowError("subtitle_attempt_invalid", "the subtitle attempt handle does not match its task", false)
	}
	command := domain.TaskMediaCommand{}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		operation, binding, err := workflow.lockSubtitleOperation(ctx, scope.Queries, handle, subtitlebinding.PhaseStarted)
		if err != nil {
			return err
		}
		if _, err := scope.Queries.GetOperationAttemptByID(ctx, db.GetOperationAttemptByIDParams{
			ID: repository.UUIDToPG(handle.AttemptID), OperationID: repository.UUIDToPG(handle.OperationID), Attempt: int32(handle.JobAttempt),
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("subtitle attempt audit is not current")
			}
			return fmt.Errorf("read subtitle attempt audit: %w", err)
		}
		snapshot, err := repository.LoadSubtitleSnapshotLocked(ctx, scope.Queries, taskID)
		if err != nil {
			return err
		}
		if err := subtitleStartObservationAllowsBegin(binding.Start, snapshot); err != nil {
			return err
		}
		task, err := scope.Queries.LockEpisodeTask(ctx, repository.UUIDToPG(taskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock episode task: %w", err)
		}
		if task.State != string(domain.TaskMediaQueued) && task.State != string(domain.TaskProcessing) && task.State != string(domain.TaskFailed) && task.State != string(domain.TaskFinalizing) && task.State != string(domain.TaskAwaitingReview) {
			return mediaWorkflowError("task_media_state_conflict", fmt.Sprintf("task media cannot start from state %q", task.State), false)
		}
		if task.SubtitleState == string(domain.SubtitleFailed) || task.SubtitleState == string(domain.SubtitleCancelled) {
			return mediaWorkflowError("subtitle_state_conflict", fmt.Sprintf("subtitle branch cannot start from state %q", task.SubtitleState), false)
		}
		if task.SubtitleState != string(domain.SubtitleASSReady) {
			startedTask, startErr := scope.Queries.StartTaskSubtitle(ctx, repository.UUIDToPG(taskID))
			if errors.Is(startErr, pgx.ErrNoRows) {
				return mediaWorkflowError("subtitle_state_conflict", fmt.Sprintf("subtitle branch cannot start from task state %q", task.State), false)
			} else if startErr != nil {
				return fmt.Errorf("start task subtitle branch: %w", startErr)
			}
			task = startedTask
		}
		receiptRow := task
		receipt := subtitlebinding.TaskReceipt{
			Attempt:       handle.JobAttempt,
			AttemptID:     handle.AttemptID,
			TaskVersion:   int(receiptRow.Version),
			TaskState:     receiptRow.State,
			SubtitleState: receiptRow.SubtitleState,
			VideoState:    receiptRow.VideoState,
			FailureStage:  stringOrEmpty(receiptRow.FailureStage),
		}
		binding.Phase = subtitlebinding.PhaseBegun
		binding.Begin = &receipt
		if err := advanceSubtitleBinding(ctx, scope.Queries, binding, subtitlebinding.PhaseStarted); err != nil {
			return err
		}
		row, err := scope.Queries.GetTaskMediaCommand(ctx, repository.UUIDToPG(taskID))
		if errors.Is(err, pgx.ErrNoRows) {
			return mediaWorkflowError("task_mapping_unavailable", "the task mapping or transcode profile is unavailable", false)
		}
		if err != nil {
			return fmt.Errorf("load task media command: %w", err)
		}
		externalRows, err := scope.Queries.ListTaskExternalSubtitles(ctx, repository.UUIDToPG(taskID))
		if err != nil {
			return fmt.Errorf("list task external subtitles: %w", err)
		}
		command, err = taskMediaCommandFromDB(row, externalRows)
		_ = operation
		return err
	})
	if err != nil {
		return domain.TaskMediaCommand{}, err
	}
	return command, nil
}

func (workflow *MediaWorkflow) CreateSubtitleVideoMatchScopeForOperation(
	ctx context.Context,
	handle subtitlebinding.AttemptHandle,
	taskID uuid.UUID,
	candidates []domain.SubtitleMatchCandidate,
) (uuid.UUID, error) {
	if !handle.Valid() || handle.ResourceID != taskID {
		return uuid.Nil, mediaWorkflowError("subtitle_attempt_invalid", "the subtitle attempt handle does not match its task", false)
	}
	if taskID == uuid.Nil || len(candidates) < 2 {
		return uuid.Nil, mediaWorkflowError("subtitle_scope_invalid", "subtitle video match requires a task and at least two candidates", false)
	}
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidateID := strings.TrimSpace(candidate.CandidateID)
		if candidateID == "" {
			return uuid.Nil, mediaWorkflowError("subtitle_scope_invalid", "subtitle candidates require stable identities", false)
		}
		if _, duplicate := seen[candidateID]; duplicate {
			return uuid.Nil, mediaWorkflowError("subtitle_scope_invalid", "subtitle candidate identities must be unique", false)
		}
		seen[candidateID] = struct{}{}
	}
	scopeID := uuid.New()
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		_, binding, err := workflow.lockSubtitleOperation(ctx, scope.Queries, handle, subtitlebinding.PhaseBegun)
		if err != nil {
			return err
		}
		currentScope, scopeErr := scope.Queries.GetSubtitleVideoMatchScopeID(ctx, repository.UUIDToPG(taskID))
		if scopeErr == nil {
			snapshot, err := repository.LoadSubtitleSnapshotLocked(ctx, scope.Queries, taskID)
			if err != nil {
				return err
			}
			if !repository.SubtitleBindingMatchesSnapshot(binding, snapshot, snapshot.SubtitleState) {
				return mediaWorkflowError("subtitle_scope_external", "the current subtitle scope is not the scope observed by this attempt", false)
			}
		} else if !errors.Is(scopeErr, pgx.ErrNoRows) {
			return fmt.Errorf("read current subtitle scope: %w", scopeErr)
		} else if binding.Start.ScopePresent {
			return mediaWorkflowError("subtitle_scope_external", "the scope observed by this attempt no longer exists", false)
		}
		_ = currentScope
		created, err := scope.Queries.CreateSubtitleVideoMatchScope(ctx, db.CreateSubtitleVideoMatchScopeParams{
			ID: repository.UUIDToPG(scopeID), TaskID: repository.UUIDToPG(taskID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			currentScopeID, idErr := scope.Queries.GetSubtitleVideoMatchScopeID(ctx, repository.UUIDToPG(taskID))
			if idErr != nil {
				return fmt.Errorf("read conflicted subtitle scope: %w", idErr)
			}
			if !binding.Start.ScopePresent || repository.UUIDFromPG(currentScopeID) != binding.Start.ScopeID {
				return mediaWorkflowError("subtitle_scope_external", "a different subtitle scope appeared after this attempt started", false)
			}
			return mediaWorkflowError("subtitle_scope_conflict", "a subtitle video match scope already exists for this task", false)
		}
		if err != nil {
			return mediaWorkflowError("subtitle_scope_unavailable", "the subtitle video match scope could not be created", true)
		}
		createdScopeID := repository.UUIDFromPG(created.ID)
		if binding.Start.ScopePresent && createdScopeID != binding.Start.ScopeID {
			return mediaWorkflowError("subtitle_scope_external", "a different subtitle scope replaced the observed scope", false)
		}
		if createdScopeID != scopeID {
			if err := scope.Queries.DeleteSubtitleVideoMatchCandidates(ctx, created.ID); err != nil {
				return mediaWorkflowError("subtitle_scope_unavailable", "stale subtitle candidates could not be cleared", true)
			}
			if _, err := scope.Queries.SupersedeSubtitleVideoMatchResolutions(ctx, db.SupersedeSubtitleVideoMatchResolutionsParams{ScopeID: created.ID}); err != nil {
				return mediaWorkflowError("subtitle_scope_unavailable", "stale subtitle resolutions could not be superseded", true)
			}
		}
		for _, candidate := range candidates {
			var streamIndex *int32
			if candidate.Source == domain.SubtitleSourceEmbedded {
				value := int32(candidate.StreamIndex)
				streamIndex = &value
			}
			if err := scope.Queries.InsertSubtitleVideoMatchCandidate(ctx, db.InsertSubtitleVideoMatchCandidateParams{
				ScopeID: repository.UUIDToPG(createdScopeID), CandidateID: candidate.CandidateID, Source: string(candidate.Source),
				StreamIndex: streamIndex, Format: optionalString(string(candidate.Format)), Language: optionalString(candidate.Language),
				Title: optionalString(candidate.Title), Path: optionalString(candidate.Path),
			}); err != nil {
				return mediaWorkflowError("subtitle_scope_unavailable", "a subtitle candidate could not be persisted", true)
			}
		}
		snapshot, err := repository.LoadSubtitleSnapshotLocked(ctx, scope.Queries, taskID)
		if err != nil {
			return err
		}
		beginReceipt := binding.TaskObservation()
		if snapshot.ScopeStatus != "pending" || snapshot.OwnerCount != 0 ||
			snapshot.SubtitleState != beginReceipt.SubtitleState || snapshot.TaskState == "cancelled" {
			return mediaWorkflowError("subtitle_scope_task_terminal", "the task subtitle branch changed before the scope was committed", false)
		}
		observation, err := subtitlebinding.ObservationFromSnapshot(snapshot)
		if err != nil {
			return mediaWorkflowError("subtitle_scope_binding_invalid", "the subtitle scope observation is invalid", false)
		}
		binding.Phase = subtitlebinding.PhaseScope
		binding.Scope = &subtitlebinding.ScopeReceipt{
			Attempt:            handle.JobAttempt,
			AttemptID:          handle.AttemptID,
			ScopeID:            createdScopeID,
			ScopeStatus:        "pending",
			ContentHash:        observation.ContentHash,
			CreatedByOperation: true,
			TaskVersion:        snapshot.TaskVersion,
			TaskState:          snapshot.TaskState,
			SubtitleState:      snapshot.SubtitleState,
			VideoState:         snapshot.VideoState,
			FailureStage:       snapshot.FailureStage,
		}
		if err := advanceSubtitleBinding(ctx, scope.Queries, binding, subtitlebinding.PhaseBegun); err != nil {
			return err
		}
		scopeID = createdScopeID
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return scopeID, nil
}

func (workflow *MediaWorkflow) lockSubtitleOperation(
	ctx context.Context,
	queries *db.Queries,
	handle subtitlebinding.AttemptHandle,
	expectedPhase string,
) (db.Operation, subtitlebinding.Binding, error) {
	operation, err := queries.LockOperation(ctx, repository.UUIDToPG(handle.OperationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Operation{}, subtitlebinding.Binding{}, fmt.Errorf("subtitle operation is missing")
	}
	if err != nil {
		return db.Operation{}, subtitlebinding.Binding{}, fmt.Errorf("lock subtitle operation: %w", err)
	}
	if operation.Kind != "subtitle.prepare" || valueOrEmpty(operation.ResourceType) != handle.ResourceType ||
		repository.UUIDFromPG(operation.ResourceID) != handle.ResourceID ||
		operation.Status != "running" || operation.CancelRequestedAt.Valid ||
		int(operation.AttemptCount) != handle.OperationAttemptCount {
		return db.Operation{}, subtitlebinding.Binding{}, fmt.Errorf("subtitle operation is not current")
	}
	binding, row, err := repository.ReadSubtitleBinding(ctx, queries, handle.OperationID)
	if err != nil {
		return db.Operation{}, subtitlebinding.Binding{}, fmt.Errorf("read subtitle binding: %w", err)
	}
	if row.AttemptID != repository.UUIDToPG(handle.AttemptID) || row.Attempt != int32(handle.JobAttempt) ||
		row.OperationAttemptCount != int32(handle.OperationAttemptCount) || (expectedPhase != "" && binding.Phase != expectedPhase) {
		return db.Operation{}, subtitlebinding.Binding{}, fmt.Errorf("subtitle binding is not current")
	}
	audit, err := queries.GetOperationAttemptByID(ctx, db.GetOperationAttemptByIDParams{
		ID: repository.UUIDToPG(handle.AttemptID), OperationID: repository.UUIDToPG(handle.OperationID), Attempt: int32(handle.JobAttempt),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Operation{}, subtitlebinding.Binding{}, fmt.Errorf("subtitle attempt audit is not current")
	}
	if err != nil {
		return db.Operation{}, subtitlebinding.Binding{}, fmt.Errorf("read subtitle attempt audit: %w", err)
	}
	if audit.Status != "running" {
		return db.Operation{}, subtitlebinding.Binding{}, fmt.Errorf("subtitle attempt audit is not current")
	}
	return operation, binding, nil
}

func (workflow *MediaWorkflow) ExpireOwnerlessSubtitleVideoMatchScopeForOperation(
	ctx context.Context,
	handle subtitlebinding.AttemptHandle,
	scopeID uuid.UUID,
	observed domain.SubtitleScopeObservation,
) (domain.SubtitleScopeExpiry, error) {
	if !handle.Valid() || scopeID == uuid.Nil {
		return domain.SubtitleScopeExpiry{}, mediaWorkflowError("subtitle_scope_invalid", "the subtitle scope cleanup requires a captured attempt and scope", false)
	}
	var result domain.SubtitleScopeExpiry
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		_, binding, err := workflow.lockSubtitleOperation(ctx, scope.Queries, handle, "")
		if err != nil {
			return err
		}
		snapshot, err := repository.LoadSubtitleSnapshotLocked(ctx, scope.Queries, handle.ResourceID)
		if err != nil {
			return err
		}
		// 所有实际 expiry/guard 路径先做 scope 归属检查。当前 task 没有
		// scope 时返回既有 Absent 幂等结果；不得再用请求 scopeID 去读/锁
		// 另一个 task 的 scope，也不引入跨任务 T→S 锁顺序。
		if !snapshot.ScopePresent {
			result = domain.SubtitleScopeExpiry{ScopeID: scopeID, Outcome: domain.SubtitleScopeAbsent}
			return nil
		}
		if snapshot.ScopeID != scopeID {
			result = domain.SubtitleScopeExpiry{
				ScopeID: scopeID, Outcome: domain.SubtitleScopeChanged, ScopeStatus: snapshot.ScopeStatus,
				TaskVersion: snapshot.TaskVersion, TaskState: snapshot.TaskState, SubtitleState: snapshot.SubtitleState,
			}
			return nil
		}
		if !repository.SubtitleBindingMatchesSnapshot(binding, snapshot, snapshot.SubtitleState) {
			// 当前有活动 owner 或已 applied 时复用既有 guard core 的完整分类：
			// MultipleOwners/Changed/OwnerPresent/AlreadyApplied 全部保留；
			// guard 在 owner/applied 分支不会 expire。owner0 的陌生
			// pending/expired/content 仍不借用旧 receipt 改写。
			if snapshot.ScopeStatus == "applied" || snapshot.OwnerCount > 0 {
				var innerErr error
				result, innerErr = expireOwnerlessSubtitleScopeInTx(ctx, scope.Queries, scopeID, observed)
				return innerErr
			}
			result = domain.SubtitleScopeExpiry{
				ScopeID: scopeID, Outcome: domain.SubtitleScopeChanged, ScopeStatus: snapshot.ScopeStatus,
				TaskVersion: snapshot.TaskVersion, TaskState: snapshot.TaskState, SubtitleState: snapshot.SubtitleState,
			}
			return nil
		}
		var innerErr error
		result, innerErr = expireOwnerlessSubtitleScopeInTx(ctx, scope.Queries, scopeID, observed)
		return innerErr
	})
	if err != nil {
		return domain.SubtitleScopeExpiry{}, mediaWorkflowError("subtitle_scope_unavailable", "the subtitle video match scope could not be expired", true)
	}
	return result, nil
}

func advanceSubtitleBinding(ctx context.Context, queries *db.Queries, binding subtitlebinding.Binding, expectedPhase string) error {
	raw, err := subtitlebinding.Encode(binding)
	if err != nil {
		return err
	}
	if _, err := queries.AdvanceOperationSubtitleBinding(ctx, db.AdvanceOperationSubtitleBindingParams{
		NextPhase: binding.Phase, Binding: raw, OperationID: repository.UUIDToPG(binding.OperationID),
		BindingVersion: int32(binding.Version), ExpectedPhase: expectedPhase, Attempt: int32(binding.Attempt),
		AttemptID: repository.UUIDToPG(binding.AttemptID), OperationAttemptCount: int32(binding.OperationAttemptCount),
	}); err != nil {
		return fmt.Errorf("advance subtitle binding: %w", err)
	}
	return nil
}

func subtitleStartObservationAllowsBegin(start subtitlebinding.Observation, snapshot subtitlebinding.Snapshot) error {
	if start.ScopePresent != snapshot.ScopePresent || snapshot.OwnerCount > 0 {
		return mediaWorkflowError("subtitle_scope_changed", "the subtitle scope changed after the attempt started", false)
	}
	if !start.ScopePresent {
		return nil
	}
	snapshotObservation, err := subtitlebinding.ObservationFromSnapshot(snapshot)
	if err != nil {
		return mediaWorkflowError("subtitle_scope_binding_invalid", "the subtitle scope observation is invalid", false)
	}
	if start.ScopeID != snapshot.ScopeID || start.ContentHash != snapshotObservation.ContentHash {
		return mediaWorkflowError("subtitle_scope_changed", "the subtitle scope content changed after the attempt started", false)
	}
	switch start.ScopeStatus {
	case "applied":
		if snapshot.ScopeStatus != "applied" || start.SelectedCandidateID != snapshot.SelectedCandidateID || start.AppliedResolutionID != snapshot.AppliedResolutionID {
			return mediaWorkflowError("subtitle_scope_changed", "the applied subtitle selection changed after the attempt started", false)
		}
	case "pending", "expired":
		if snapshot.ScopeStatus != "pending" && snapshot.ScopeStatus != "expired" {
			return mediaWorkflowError("subtitle_scope_changed", "the subtitle scope status changed after the attempt started", false)
		}
	default:
		return mediaWorkflowError("subtitle_scope_changed", "the subtitle scope status is unsupported", false)
	}
	return nil
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
