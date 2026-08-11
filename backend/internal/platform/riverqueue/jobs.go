package riverqueue

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

const (
	QueueGeneral   = "general"
	QueueTranscode = "transcode"
	QueueAgent     = "agent"

	KindSearchRun               = "search.run"
	KindRSSPoll                 = "rss.poll"
	KindDownloadEnqueue         = "download.enqueue"
	KindDownloadSync            = "download.sync"
	KindDownloadMaterialize     = "download.materialize"
	KindSubtitlePrepare         = "subtitle.prepare"
	KindTranscodeRun            = "transcode.run"
	KindMediaFinalize           = "media.finalize"
	KindEmbyImport              = "emby.import"
	KindCleanupRun              = "cleanup.run"
	KindEmbyRefresh             = "emby.refresh"
	KindEmbyScan                = "emby.scan"
	KindTMDbSync                = "tmdb.sync"
	KindTaskCancel              = "task.cancel"
	KindDownloadCancel          = "download.cancel"
	KindAcquisitionDelete       = "acquisition.delete"
	KindRSSSubscriptionComplete = "rss.subscription.complete"
	KindRSSSubscriptionDelete   = "rss.subscription.delete"
	KindAgentResolve            = "agent.resolve"
	KindDownloadSelectionApply  = "download.selection.apply"
)

type OperationArgs struct {
	OperationID   uuid.UUID `json:"operationId" river:"unique"`
	TimeoutSecond int       `json:"timeoutSeconds"`
}

func (args OperationArgs) Timeout() time.Duration {
	return time.Duration(args.TimeoutSecond) * time.Second
}

func (args OperationArgs) GetOperationArgs() OperationArgs {
	return args
}

type JobArguments interface {
	river.JobArgs
	GetOperationArgs() OperationArgs
}

type SearchRunArgs struct{ OperationArgs }

func (SearchRunArgs) Kind() string { return KindSearchRun }

type RSSPollArgs struct{ OperationArgs }

func (RSSPollArgs) Kind() string { return KindRSSPoll }

type RSSSubscriptionCompleteArgs struct{ OperationArgs }

func (RSSSubscriptionCompleteArgs) Kind() string { return KindRSSSubscriptionComplete }

type RSSSubscriptionDeleteArgs struct{ OperationArgs }

func (RSSSubscriptionDeleteArgs) Kind() string { return KindRSSSubscriptionDelete }

type AcquisitionDeleteArgs struct{ OperationArgs }

func (AcquisitionDeleteArgs) Kind() string { return KindAcquisitionDelete }

type DownloadEnqueueArgs struct{ OperationArgs }

func (DownloadEnqueueArgs) Kind() string { return KindDownloadEnqueue }

type DownloadSyncArgs struct{ OperationArgs }

func (DownloadSyncArgs) Kind() string { return KindDownloadSync }

type DownloadMaterializeArgs struct{ OperationArgs }

func (DownloadMaterializeArgs) Kind() string { return KindDownloadMaterialize }

type DownloadSelectionApplyArgs struct{ OperationArgs }

func (DownloadSelectionApplyArgs) Kind() string { return KindDownloadSelectionApply }

type AgentResolveArgs struct{ OperationArgs }

func (AgentResolveArgs) Kind() string { return KindAgentResolve }

type SubtitlePrepareArgs struct{ OperationArgs }

func (SubtitlePrepareArgs) Kind() string { return KindSubtitlePrepare }

type TranscodeRunArgs struct{ OperationArgs }

func (TranscodeRunArgs) Kind() string { return KindTranscodeRun }

type MediaFinalizeArgs struct{ OperationArgs }

func (MediaFinalizeArgs) Kind() string { return KindMediaFinalize }

type EmbyImportArgs struct{ OperationArgs }

func (EmbyImportArgs) Kind() string { return KindEmbyImport }

type CleanupRunArgs struct{ OperationArgs }

func (CleanupRunArgs) Kind() string { return KindCleanupRun }

type EmbyRefreshArgs struct{ OperationArgs }

func (EmbyRefreshArgs) Kind() string { return KindEmbyRefresh }

type EmbyScanArgs struct{ OperationArgs }

func (EmbyScanArgs) Kind() string { return KindEmbyScan }

type TMDbSyncArgs struct{ OperationArgs }

func (TMDbSyncArgs) Kind() string { return KindTMDbSync }

type TaskCancelArgs struct{ OperationArgs }

func (TaskCancelArgs) Kind() string { return KindTaskCancel }

type DownloadCancelArgs struct{ OperationArgs }

func (DownloadCancelArgs) Kind() string { return KindDownloadCancel }

func NewJobArgs(kind string, operationID uuid.UUID, timeout time.Duration) (river.JobArgs, error) {
	if operationID == uuid.Nil {
		return nil, fmt.Errorf("operation ID is required")
	}
	if timeout <= 0 || timeout%time.Second != 0 {
		return nil, fmt.Errorf("operation timeout must be a positive whole number of seconds")
	}
	operationArgs := OperationArgs{OperationID: operationID, TimeoutSecond: int(timeout / time.Second)}
	switch kind {
	case KindSearchRun:
		return SearchRunArgs{OperationArgs: operationArgs}, nil
	case KindRSSPoll:
		return RSSPollArgs{OperationArgs: operationArgs}, nil
	case KindRSSSubscriptionComplete:
		return RSSSubscriptionCompleteArgs{OperationArgs: operationArgs}, nil
	case KindRSSSubscriptionDelete:
		return RSSSubscriptionDeleteArgs{OperationArgs: operationArgs}, nil
	case KindAcquisitionDelete:
		return AcquisitionDeleteArgs{OperationArgs: operationArgs}, nil
	case KindDownloadEnqueue:
		return DownloadEnqueueArgs{OperationArgs: operationArgs}, nil
	case KindDownloadSync:
		return DownloadSyncArgs{OperationArgs: operationArgs}, nil
	case KindDownloadMaterialize:
		return DownloadMaterializeArgs{OperationArgs: operationArgs}, nil
	case KindDownloadSelectionApply:
		return DownloadSelectionApplyArgs{OperationArgs: operationArgs}, nil
	case KindAgentResolve:
		return AgentResolveArgs{OperationArgs: operationArgs}, nil
	case KindSubtitlePrepare:
		return SubtitlePrepareArgs{OperationArgs: operationArgs}, nil
	case KindTranscodeRun:
		return TranscodeRunArgs{OperationArgs: operationArgs}, nil
	case KindMediaFinalize:
		return MediaFinalizeArgs{OperationArgs: operationArgs}, nil
	case KindEmbyImport:
		return EmbyImportArgs{OperationArgs: operationArgs}, nil
	case KindCleanupRun:
		return CleanupRunArgs{OperationArgs: operationArgs}, nil
	case KindEmbyRefresh:
		return EmbyRefreshArgs{OperationArgs: operationArgs}, nil
	case KindEmbyScan:
		return EmbyScanArgs{OperationArgs: operationArgs}, nil
	case KindTMDbSync:
		return TMDbSyncArgs{OperationArgs: operationArgs}, nil
	case KindTaskCancel:
		return TaskCancelArgs{OperationArgs: operationArgs}, nil
	case KindDownloadCancel:
		return DownloadCancelArgs{OperationArgs: operationArgs}, nil
	default:
		return nil, fmt.Errorf("unsupported River job kind %q", kind)
	}
}

func InsertOptions(kind string, maxAttempts int) (*river.InsertOpts, error) {
	if maxAttempts <= 0 {
		return nil, fmt.Errorf("max attempts must be positive")
	}
	queue := QueueGeneral
	switch kind {
	case KindTranscodeRun:
		queue = QueueTranscode
	case KindAgentResolve:
		queue = QueueAgent
	case KindSearchRun,
		KindRSSPoll,
		KindRSSSubscriptionComplete,
		KindRSSSubscriptionDelete,
		KindAcquisitionDelete,
		KindDownloadEnqueue,
		KindDownloadSync,
		KindDownloadMaterialize,
		KindDownloadSelectionApply,
		KindSubtitlePrepare,
		KindMediaFinalize,
		KindEmbyImport,
		KindCleanupRun,
		KindEmbyRefresh,
		KindEmbyScan,
		KindTMDbSync,
		KindTaskCancel,
		KindDownloadCancel:
	default:
		return nil, fmt.Errorf("unsupported River job kind %q", kind)
	}
	return &river.InsertOpts{
		MaxAttempts: maxAttempts,
		Queue:       queue,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByQueue: true,
		},
	}, nil
}
