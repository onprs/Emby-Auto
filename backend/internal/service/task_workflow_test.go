package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestReviewTaskValidatesCommandBeforeDatabaseAccess(t *testing.T) {
	workflow := NewTaskWorkflow(nil, nil, nil)
	base := domain.ReviewTask{
		TaskID: uuid.MustParse("7a000000-0000-0000-0000-000000000001"), ExpectedVersion: 1,
		Decision: domain.TaskApproved, IdempotencyKey: "review-1", ActorUserID: uuid.MustParse("7a000000-0000-0000-0000-000000000002"),
	}
	tests := []struct {
		name  string
		edit  func(*domain.ReviewTask)
		field string
	}{
		{name: "missing task", edit: func(input *domain.ReviewTask) { input.TaskID = uuid.Nil }, field: "taskId"},
		{name: "missing version", edit: func(input *domain.ReviewTask) { input.ExpectedVersion = 0 }, field: "expectedVersion"},
		{name: "invalid decision", edit: func(input *domain.ReviewTask) { input.Decision = domain.TaskProcessing }, field: "decision"},
		{name: "missing key", edit: func(input *domain.ReviewTask) { input.IdempotencyKey = " " }, field: "idempotencyKey"},
		{name: "missing actor", edit: func(input *domain.ReviewTask) { input.ActorUserID = uuid.Nil }, field: "actorUserId"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			_, err := workflow.ReviewTask(context.Background(), input)
			var serviceErr *Error
			if !errors.As(err, &serviceErr) || !errors.Is(err, ErrInvalidInput) || serviceErr.Details["field"] != test.field {
				t.Fatalf("ReviewTask() error = %#v, want field %q", err, test.field)
			}
		})
	}
}

func TestQueueImportValidatesCommandBeforeDatabaseAccess(t *testing.T) {
	workflow := NewTaskWorkflow(nil, nil, nil)
	_, err := workflow.QueueImport(context.Background(), domain.QueueTaskImport{})
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Details["field"] != "taskId" {
		t.Fatalf("QueueImport() error = %#v", err)
	}
}
