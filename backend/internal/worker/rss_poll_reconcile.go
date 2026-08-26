package worker

import (
	"context"
	"errors"
	"time"

	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/riverqueue/river"
)

type RSSDuePollReconciler interface {
	ReconcileDuePolls(context.Context) (int, error)
}

type RSSPollReconcileWorker struct {
	river.WorkerDefaults[appqueue.RSSPollReconcileArgs]
	reconciler RSSDuePollReconciler
}

func NewRSSPollReconcileWorker(reconciler RSSDuePollReconciler) *RSSPollReconcileWorker {
	return &RSSPollReconcileWorker{reconciler: reconciler}
}

func (worker *RSSPollReconcileWorker) Timeout(*river.Job[appqueue.RSSPollReconcileArgs]) time.Duration {
	return time.Minute
}

func (worker *RSSPollReconcileWorker) Work(
	ctx context.Context,
	_ *river.Job[appqueue.RSSPollReconcileArgs],
) error {
	if worker.reconciler == nil {
		return errors.New("RSS due poll reconciler is unavailable")
	}
	_, err := worker.reconciler.ReconcileDuePolls(ctx)
	return err
}
