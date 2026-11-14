package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
)

// TestSettleTerminalDeletionFailureInTxRejectsIneligibleOperations 验证维护结算
// 的事务内防御：调用方即使错误地传入不匹配的记录，也不会走到资源恢复或写库。
func TestSettleTerminalDeletionFailureInTxRejectsIneligibleOperations(t *testing.T) {
	running := db.Operation{
		ID:           UUIDToPG(uuid.New()),
		Kind:         "acquisition.delete",
		ResourceType: stringPointer("acquisition"),
		ResourceID:   UUIDToPG(uuid.New()),
		Status:       "running",
		MaxAttempts:  5,
		AttemptCount: 5,
	}
	withCancel := running
	withCancel.CancelRequestedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	notRunning := running
	notRunning.Status = "failed"
	invalidResource := running
	invalidResource.ResourceID = pgtype.UUID{}
	otherKind := running
	otherKind.Kind = "search.run"
	otherResource := running
	otherResource.ResourceType = stringPointer("episode_task")

	cases := []struct {
		name      string
		operation db.Operation
		attempt   int32
	}{
		{name: "operation not running", operation: notRunning, attempt: 5},
		{name: "cancel requested", operation: withCancel, attempt: 5},
		{name: "attempt below current count", operation: running, attempt: 4},
		{name: "attempt above current count", operation: running, attempt: 6},
		{name: "attempt below budget", operation: running, attempt: 0},
		{name: "attempt above budget", operation: func() db.Operation {
			operation := running
			operation.MaxAttempts = 3
			return operation
		}(), attempt: 5},
		{name: "resource id missing", operation: invalidResource, attempt: 5},
		{name: "kind not allowed", operation: otherKind, attempt: 5},
		{name: "resource type not allowed", operation: otherResource, attempt: 5},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			settled, err := SettleTerminalDeletionFailureInTx(
				context.Background(), nil, testCase.operation, testCase.attempt, "code", "message",
			)
			if err != nil {
				t.Fatalf("SettleTerminalDeletionFailureInTx() error = %v", err)
			}
			if settled {
				t.Fatalf("SettleTerminalDeletionFailureInTx() settled ineligible operation %#v", testCase.operation)
			}
		})
	}
}

func stringPointer(value string) *string {
	return &value
}
