package worker

import (
	"context"
	"errors"
	"time"

	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/riverqueue/river"
)

type RSSSubscriptionProgressReconciler interface {
	ReconcileSubscriptionProgress(context.Context) (int, error)
}

type RSSSubscriptionProgressReconcileWorker struct {
	river.WorkerDefaults[appqueue.RSSSubscriptionProgressReconcileArgs]
	reconciler RSSSubscriptionProgressReconciler
}

func (worker *RSSSubscriptionProgressReconcileWorker) Timeout(
	*river.Job[appqueue.RSSSubscriptionProgressReconcileArgs],
) time.Duration {
	return 5 * time.Minute
}

func NewRSSSubscriptionProgressReconcileWorker(
	reconciler RSSSubscriptionProgressReconciler,
) *RSSSubscriptionProgressReconcileWorker {
	return &RSSSubscriptionProgressReconcileWorker{reconciler: reconciler}
}

func (worker *RSSSubscriptionProgressReconcileWorker) Work(
	ctx context.Context,
	_ *river.Job[appqueue.RSSSubscriptionProgressReconcileArgs],
) error {
	if worker.reconciler == nil {
		return errors.New("RSS subscription progress reconciler is unavailable")
	}
	_, err := worker.reconciler.ReconcileSubscriptionProgress(ctx)
	return err
}
