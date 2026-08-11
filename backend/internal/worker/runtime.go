package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/riverqueue/river"
)

var (
	ErrCancellationRequested = errors.New("operation cancellation was requested")
	errWorkerFinished        = errors.New("operation handler finished")
)

type OperationLifecycle interface {
	StartAttempt(context.Context, uuid.UUID, int, string) (domain.Operation, error)
	Heartbeat(context.Context, uuid.UUID, int) (bool, error)
	SucceedAttempt(context.Context, uuid.UUID, int) error
	FailAttempt(context.Context, uuid.UUID, int, string, string, bool) error
	CancelAttempt(context.Context, uuid.UUID, int) error
	SnoozeAttempt(context.Context, uuid.UUID, int) error
}

type Handler interface {
	Handle(context.Context, domain.Operation) error
}

type HandlerFunc func(context.Context, domain.Operation) error

func (handler HandlerFunc) Handle(ctx context.Context, operation domain.Operation) error {
	return handler(ctx, operation)
}

type Failure struct {
	Code      string
	Message   string
	Retryable bool
	Cause     error
}

func (failure *Failure) Error() string {
	if failure.Cause != nil {
		return fmt.Sprintf("%s: %v", failure.Message, failure.Cause)
	}
	return failure.Message
}

func (failure *Failure) Unwrap() error {
	return failure.Cause
}

type operationWorker[T appqueue.JobArguments] struct {
	river.WorkerDefaults[T]
	lifecycle         OperationLifecycle
	handler           Handler
	heartbeatInterval time.Duration
	workerID          string
}

func (worker *operationWorker[T]) Timeout(job *river.Job[T]) time.Duration {
	return job.Args.GetOperationArgs().Timeout()
}

func (worker *operationWorker[T]) Work(ctx context.Context, job *river.Job[T]) error {
	args := job.Args.GetOperationArgs()
	operation, err := worker.lifecycle.StartAttempt(ctx, args.OperationID, job.Attempt, worker.workerID)
	if errors.Is(err, domain.ErrOperationNotRunnable) {
		return river.JobCancel(err)
	}
	if err != nil {
		return fmt.Errorf("start operation audit: %w", err)
	}
	if operation.Status == "cancelled" {
		return river.JobCancel(ErrCancellationRequested)
	}

	workCtx, cancel := context.WithCancelCause(ctx)
	heartbeatDone := make(chan struct{})
	go worker.heartbeat(workCtx, cancel, args.OperationID, job.Attempt, heartbeatDone)

	handlerErr := worker.handler.Handle(workCtx, operation)
	cancel(errWorkerFinished)
	<-heartbeatDone
	cause := context.Cause(workCtx)
	if errors.Is(cause, errWorkerFinished) {
		cause = nil
	}

	finalizeCtx, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer finalizeCancel()

	if errors.Is(cause, ErrCancellationRequested) {
		if err := worker.lifecycle.CancelAttempt(finalizeCtx, args.OperationID, job.Attempt); err != nil {
			return fmt.Errorf("record operation cancellation: %w", err)
		}
		return river.JobCancel(ErrCancellationRequested)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		if err := worker.lifecycle.SnoozeAttempt(finalizeCtx, args.OperationID, job.Attempt); err != nil {
			return fmt.Errorf("record interrupted operation snooze: %w", err)
		}
		return river.JobSnooze(time.Second)
	}
	var snoozeErr *river.JobSnoozeError
	if cause == nil && errors.As(handlerErr, &snoozeErr) {
		if err := worker.lifecycle.SnoozeAttempt(finalizeCtx, args.OperationID, job.Attempt); err != nil {
			return fmt.Errorf("record operation snooze: %w", err)
		}
		return handlerErr
	}
	if handlerErr == nil && cause == nil {
		if err := worker.lifecycle.SucceedAttempt(finalizeCtx, args.OperationID, job.Attempt); err != nil {
			return fmt.Errorf("record successful operation: %w", err)
		}
		return nil
	}

	failure := classifyFailure(handlerErr, cause)
	if err := worker.lifecycle.FailAttempt(
		finalizeCtx,
		args.OperationID,
		job.Attempt,
		failure.Code,
		failure.Message,
		failure.Retryable,
	); err != nil {
		return fmt.Errorf("record failed operation: %w", errors.Join(failure, err))
	}
	if !failure.Retryable {
		return river.JobCancel(failure)
	}
	return failure
}

func (worker *operationWorker[T]) heartbeat(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	operationID uuid.UUID,
	attempt int,
	done chan<- struct{},
) {
	defer close(done)
	interval := worker.heartbeatInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			alive, err := worker.lifecycle.Heartbeat(ctx, operationID, attempt)
			if err != nil {
				cancel(fmt.Errorf("operation heartbeat: %w", err))
				return
			}
			if !alive {
				cancel(ErrCancellationRequested)
				return
			}
		}
	}
}

func classifyFailure(handlerErr, contextCause error) *Failure {
	var failure *Failure
	if errors.As(handlerErr, &failure) {
		return failure
	}
	if contextCause != nil {
		return &Failure{
			Code:      "operation_interrupted",
			Message:   contextCause.Error(),
			Retryable: true,
			Cause:     contextCause,
		}
	}
	if handlerErr == nil {
		handlerErr = errors.New("operation failed without an error")
	}
	return &Failure{
		Code:      "operation_failed",
		Message:   handlerErr.Error(),
		Retryable: true,
		Cause:     handlerErr,
	}
}

func NewWorkers(
	lifecycle OperationLifecycle,
	handlers map[string]Handler,
	heartbeatInterval time.Duration,
	workerID string,
) *river.Workers {
	workers := river.NewWorkers()
	register := func(kind string, registerWorker func(Handler)) {
		handler := handlers[kind]
		if handler == nil {
			handler = HandlerFunc(func(context.Context, domain.Operation) error {
				return &Failure{
					Code:      "handler_not_registered",
					Message:   fmt.Sprintf("no operation handler is registered for %s", kind),
					Retryable: false,
				}
			})
		}
		registerWorker(handler)
	}
	register(appqueue.KindSearchRun, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.SearchRunArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindRSSPoll, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.RSSPollArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindRSSSubscriptionComplete, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.RSSSubscriptionCompleteArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindRSSSubscriptionDelete, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.RSSSubscriptionDeleteArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindAcquisitionDelete, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.AcquisitionDeleteArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindDownloadEnqueue, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.DownloadEnqueueArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindDownloadSync, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.DownloadSyncArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindDownloadMaterialize, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.DownloadMaterializeArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindDownloadSelectionApply, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.DownloadSelectionApplyArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindAgentResolve, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.AgentResolveArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindSubtitlePrepare, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.SubtitlePrepareArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindTranscodeRun, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.TranscodeRunArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindMediaFinalize, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.MediaFinalizeArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindEmbyImport, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.EmbyImportArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindCleanupRun, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.CleanupRunArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindEmbyRefresh, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.EmbyRefreshArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindEmbyScan, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.EmbyScanArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindTMDbSync, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.TMDbSyncArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindTaskCancel, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.TaskCancelArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	register(appqueue.KindDownloadCancel, func(handler Handler) {
		river.AddWorker(workers, newOperationWorker[appqueue.DownloadCancelArgs](lifecycle, handler, heartbeatInterval, workerID))
	})
	return workers
}

func newOperationWorker[T appqueue.JobArguments](
	lifecycle OperationLifecycle,
	handler Handler,
	heartbeatInterval time.Duration,
	workerID string,
) *operationWorker[T] {
	return &operationWorker[T]{
		lifecycle:         lifecycle,
		handler:           handler,
		heartbeatInterval: heartbeatInterval,
		workerID:          workerID,
	}
}
