package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type RiverJobInserter interface {
	InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

var _ RiverJobInserter = (*river.Client[pgx.Tx])(nil)

type ScheduleOperationRequest struct {
	Kind           string
	ResourceType   string
	ResourceID     uuid.UUID
	IdempotencyKey string
	MaxAttempts    int
	Timeout        time.Duration
	Payload        map[string]any
	ActorUserID    uuid.UUID
}

type ScheduleOperationResult struct {
	Operation domain.Operation
	Created   bool
}

type OperationScheduler struct {
	transactor *database.Transactor
	jobs       RiverJobInserter
}

func NewOperationScheduler(transactor *database.Transactor, jobs RiverJobInserter) *OperationScheduler {
	return &OperationScheduler{transactor: transactor, jobs: jobs}
}

type preparedOperation struct {
	request       ScheduleOperationRequest
	operationID   uuid.UUID
	jobArgs       river.JobArgs
	insertOptions *river.InsertOpts
	payloadJSON   []byte
}

func (scheduler *OperationScheduler) Schedule(
	ctx context.Context,
	request ScheduleOperationRequest,
) (ScheduleOperationResult, error) {
	prepared, err := prepareOperation(request)
	if err != nil {
		return ScheduleOperationResult{}, err
	}
	result := ScheduleOperationResult{}
	if err := scheduler.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		var scheduleErr error
		result, scheduleErr = scheduler.scheduleInScope(ctx, scope, prepared)
		return scheduleErr
	}); err != nil {
		return ScheduleOperationResult{}, wrapScheduleError(err)
	}
	return result, nil
}

// ScheduleInTx inserts an operation, its unique River job, and queued event in
// a caller-owned transaction alongside the domain state change that created it.
func (scheduler *OperationScheduler) ScheduleInTx(
	ctx context.Context,
	scope database.TxScope,
	request ScheduleOperationRequest,
) (ScheduleOperationResult, error) {
	prepared, err := prepareOperation(request)
	if err != nil {
		return ScheduleOperationResult{}, err
	}
	result, err := scheduler.scheduleInScope(ctx, scope, prepared)
	if err != nil {
		return ScheduleOperationResult{}, wrapScheduleError(err)
	}
	return result, nil
}

func prepareOperation(request ScheduleOperationRequest) (preparedOperation, error) {
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return preparedOperation{}, invalidOperation("idempotencyKey", "must not be blank")
	}
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.MaxAttempts <= 0 || request.MaxAttempts > math.MaxInt32 {
		return preparedOperation{}, invalidOperation("maxAttempts", "must be between 1 and 2147483647")
	}
	if request.Timeout <= 0 || request.Timeout%time.Second != 0 || request.Timeout/time.Second > math.MaxInt32 {
		return preparedOperation{}, invalidOperation("timeout", "must be between 1 and 2147483647 whole seconds")
	}
	if (request.ResourceType == "") != (request.ResourceID == uuid.Nil) {
		return preparedOperation{}, invalidOperation("resource", "type and ID must be provided together")
	}
	operationID := uuid.New()
	jobArgs, err := appqueue.NewJobArgs(request.Kind, operationID, request.Timeout)
	if err != nil {
		return preparedOperation{}, invalidOperation("kind", err.Error())
	}
	insertOptions, err := appqueue.InsertOptions(request.Kind, request.MaxAttempts)
	if err != nil {
		return preparedOperation{}, invalidOperation("kind", err.Error())
	}
	payload := request.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return preparedOperation{}, invalidOperation("payload", "must be a JSON object")
	}
	return preparedOperation{
		request:       request,
		operationID:   operationID,
		jobArgs:       jobArgs,
		insertOptions: insertOptions,
		payloadJSON:   payloadJSON,
	}, nil
}

func (scheduler *OperationScheduler) scheduleInScope(
	ctx context.Context,
	scope database.TxScope,
	prepared preparedOperation,
) (ScheduleOperationResult, error) {
	request := prepared.request
	resourceType, resourceID := nullableResource(request.ResourceType, request.ResourceID)
	created, err := scope.Queries.CreateOperation(ctx, db.CreateOperationParams{
		ID:             repository.UUIDToPG(prepared.operationID),
		Kind:           request.Kind,
		ResourceType:   resourceType,
		ResourceID:     resourceID,
		IdempotencyKey: request.IdempotencyKey,
		MaxAttempts:    int32(request.MaxAttempts),
		TimeoutSeconds: int32(request.Timeout / time.Second),
		Payload:        prepared.payloadJSON,
	})
	if err != nil {
		return ScheduleOperationResult{}, fmt.Errorf("create operation: %w", err)
	}
	if created == 0 {
		existing, err := scope.Queries.GetOperationByIdempotencyKey(ctx, request.IdempotencyKey)
		if err != nil {
			return ScheduleOperationResult{}, fmt.Errorf("load idempotent operation: %w", err)
		}
		if !sameOperationCommand(existing, request, prepared.payloadJSON) {
			return ScheduleOperationResult{}, NewError(
				"idempotency_conflict",
				"the idempotency key was already used for a different command",
				ErrStateConflict,
				map[string]any{"idempotencyKey": request.IdempotencyKey},
			)
		}
		return ScheduleOperationResult{Operation: operationFromDB(existing), Created: false}, nil
	}

	inserted, err := scheduler.jobs.InsertTx(ctx, scope.Tx, prepared.jobArgs, prepared.insertOptions)
	if err != nil {
		return ScheduleOperationResult{}, fmt.Errorf("insert River job: %w", err)
	}
	if inserted.UniqueSkippedAsDuplicate || inserted.Job == nil {
		return ScheduleOperationResult{}, fmt.Errorf("new operation River job was unexpectedly deduplicated")
	}
	operation, err := scope.Queries.AttachRiverJob(ctx, db.AttachRiverJobParams{
		RiverJobID: &inserted.Job.ID,
		ID:         repository.UUIDToPG(prepared.operationID),
	})
	if err != nil {
		return ScheduleOperationResult{}, fmt.Errorf("attach River job to operation: %w", err)
	}

	eventData, err := json.Marshal(map[string]any{"kind": request.Kind, "status": "queued"})
	if err != nil {
		return ScheduleOperationResult{}, fmt.Errorf("encode operation event: %w", err)
	}
	if _, err := scope.Queries.AppendEvent(ctx, db.AppendEventParams{
		ID:           repository.UUIDToPG(uuid.New()),
		Topic:        "operation.queued",
		ResourceType: resourceType,
		ResourceID:   resourceID,
		OperationID:  repository.UUIDToPG(prepared.operationID),
		ActorUserID:  nullableUUID(request.ActorUserID),
		Data:         eventData,
	}); err != nil {
		return ScheduleOperationResult{}, fmt.Errorf("append operation event: %w", err)
	}
	return ScheduleOperationResult{Operation: operationFromDB(operation), Created: true}, nil
}

func wrapScheduleError(err error) error {
	var serviceErr *Error
	if errors.As(err, &serviceErr) {
		return serviceErr
	}
	return fmt.Errorf("schedule operation: %w", err)
}

func nullableResource(resourceType string, resourceID uuid.UUID) (*string, pgtype.UUID) {
	if resourceType == "" {
		return nil, pgtype.UUID{}
	}
	return &resourceType, repository.UUIDToPG(resourceID)
}

func nullableUUID(value uuid.UUID) pgtype.UUID {
	if value == uuid.Nil {
		return pgtype.UUID{}
	}
	return repository.UUIDToPG(value)
}

func sameOperationCommand(operation db.Operation, request ScheduleOperationRequest, payload []byte) bool {
	return operation.Kind == request.Kind &&
		valueOrEmpty(operation.ResourceType) == request.ResourceType &&
		repository.UUIDFromPG(operation.ResourceID) == request.ResourceID &&
		operation.MaxAttempts == int32(request.MaxAttempts) &&
		operation.TimeoutSeconds == int32(request.Timeout/time.Second) &&
		jsonValuesEqual(operation.Payload, payload)
}

func jsonValuesEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func operationFromDB(operation db.Operation) domain.Operation {
	return domain.Operation{
		ID:             repository.UUIDFromPG(operation.ID),
		Kind:           operation.Kind,
		ResourceType:   valueOrEmpty(operation.ResourceType),
		ResourceID:     repository.UUIDFromPG(operation.ResourceID),
		IdempotencyKey: operation.IdempotencyKey,
		Status:         operation.Status,
		RiverJobID:     valueOrZero(operation.RiverJobID),
		MaxAttempts:    int(operation.MaxAttempts),
		AttemptCount:   int(operation.AttemptCount),
		Timeout:        time.Duration(operation.TimeoutSeconds) * time.Second,
		Payload:        operation.Payload,
	}
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func invalidOperation(field, reason string) *Error {
	return NewError(
		"invalid_operation",
		"the operation is invalid",
		ErrInvalidInput,
		map[string]any{"field": field, "reason": reason},
	)
}

func findIdempotentResourceCommand(
	ctx context.Context,
	scope database.TxScope,
	idempotencyKey string,
	resourceType string,
	resourceID uuid.UUID,
	command string,
	allowedKinds ...string,
) (domain.Operation, bool, error) {
	existing, err := scope.Queries.GetOperationByIdempotencyKey(ctx, strings.TrimSpace(idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Operation{}, false, nil
	}
	if err != nil {
		return domain.Operation{}, false, fmt.Errorf("load idempotent command: %w", err)
	}
	kindAllowed := false
	for _, kind := range allowedKinds {
		if existing.Kind == kind {
			kindAllowed = true
			break
		}
	}
	var payload map[string]any
	payloadMatches := json.Unmarshal(existing.Payload, &payload) == nil && payload["command"] == command
	if !kindAllowed || valueOrEmpty(existing.ResourceType) != resourceType || repository.UUIDFromPG(existing.ResourceID) != resourceID || !payloadMatches {
		return domain.Operation{}, false, NewError(
			"idempotency_conflict",
			"the idempotency key was already used for a different command",
			ErrStateConflict,
			map[string]any{"idempotencyKey": idempotencyKey},
		)
	}
	return operationFromDB(existing), true, nil
}

func requestResourceOperationCancellations(
	ctx context.Context,
	scope database.TxScope,
	resourceType string,
	resourceID uuid.UUID,
	actorUserID uuid.UUID,
) error {
	operations, err := scope.Queries.ListCancellableResourceOperations(ctx, db.ListCancellableResourceOperationsParams{
		ResourceType: &resourceType,
		ResourceID:   repository.UUIDToPG(resourceID),
	})
	if err != nil {
		return fmt.Errorf("list cancellable %s operations: %w", resourceType, err)
	}
	for _, operation := range operations {
		requested, err := scope.Queries.RequestOperationCancellation(ctx, operation.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("request operation %s cancellation: %w", repository.UUIDFromPG(operation.ID), err)
		}
		if err := appendResourceEvent(
			ctx,
			scope.Queries,
			resourceType,
			resourceID,
			repository.UUIDFromPG(requested.ID),
			actorUserID,
			"operation.cancel_requested",
			map[string]any{"kind": requested.Kind},
		); err != nil {
			return err
		}
	}
	return nil
}
