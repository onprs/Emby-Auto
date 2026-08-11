package domain

import "fmt"

// TransitionError reports a state change rejected by a domain state machine.
type TransitionError struct {
	Machine string
	From    string
	To      string
}

func (err *TransitionError) Error() string {
	return fmt.Sprintf("%s state transition %q -> %q is not allowed", err.Machine, err.From, err.To)
}

type RSSState string

const (
	RSSDiscovered    RSSState = "discovered"
	RSSEnqueueing    RSSState = "enqueueing"
	RSSEnqueued      RSSState = "enqueued"
	RSSEnqueueFailed RSSState = "enqueue_failed"
)

func ValidateRSSTransition(from, to RSSState) error {
	allowed := map[RSSState]map[RSSState]struct{}{
		RSSDiscovered:    {RSSEnqueueing: {}},
		RSSEnqueueing:    {RSSEnqueued: {}, RSSEnqueueFailed: {}},
		RSSEnqueueFailed: {RSSEnqueueing: {}},
	}
	return validateTransition("rss", string(from), string(to), allowed[from], to)
}

type DownloadState string

const (
	DownloadEnqueuePending        DownloadState = "enqueue_pending"
	DownloadFileResolutionPending DownloadState = "file_resolution_pending"
	DownloadDownloading           DownloadState = "downloading"
	DownloadCompleted             DownloadState = "completed"
	DownloadSelectingFiles        DownloadState = "selecting_files"
	DownloadMaterialized          DownloadState = "materialized"
	DownloadFailed                DownloadState = "failed"
	DownloadCancelled             DownloadState = "cancelled"
)

func ValidateDownloadTransition(from, to DownloadState) error {
	allowed := map[DownloadState]map[DownloadState]struct{}{
		DownloadEnqueuePending:        {DownloadFileResolutionPending: {}, DownloadDownloading: {}, DownloadFailed: {}, DownloadCancelled: {}},
		DownloadFileResolutionPending: {DownloadDownloading: {}, DownloadFailed: {}, DownloadCancelled: {}},
		DownloadDownloading:           {DownloadCompleted: {}, DownloadFailed: {}, DownloadCancelled: {}},
		DownloadCompleted:             {DownloadSelectingFiles: {}, DownloadFailed: {}, DownloadCancelled: {}},
		DownloadSelectingFiles:        {DownloadMaterialized: {}, DownloadFailed: {}, DownloadCancelled: {}},
	}
	return validateTransition("download", string(from), string(to), allowed[from], to)
}

type TaskState string

const (
	TaskMediaQueued    TaskState = "media_queued"
	TaskProcessing     TaskState = "processing"
	TaskFinalizing     TaskState = "finalizing"
	TaskAwaitingReview TaskState = "awaiting_review"
	TaskApproved       TaskState = "approved"
	TaskRejected       TaskState = "rejected"
	TaskImportQueued   TaskState = "import_queued"
	TaskImporting      TaskState = "importing"
	TaskImported       TaskState = "imported"
	TaskFailed         TaskState = "failed"
	TaskCancelled      TaskState = "cancelled"
)

func ValidateTaskTransition(from, to TaskState) error {
	allowed := map[TaskState]map[TaskState]struct{}{
		TaskMediaQueued:    {TaskProcessing: {}, TaskFailed: {}, TaskCancelled: {}},
		TaskProcessing:     {TaskFinalizing: {}, TaskFailed: {}, TaskCancelled: {}},
		TaskFinalizing:     {TaskAwaitingReview: {}, TaskFailed: {}, TaskCancelled: {}},
		TaskAwaitingReview: {TaskApproved: {}, TaskRejected: {}, TaskFailed: {}, TaskCancelled: {}},
		TaskApproved:       {TaskImportQueued: {}, TaskFailed: {}, TaskCancelled: {}},
		TaskImportQueued:   {TaskImporting: {}, TaskFailed: {}, TaskCancelled: {}},
		TaskImporting:      {TaskImported: {}, TaskFailed: {}, TaskCancelled: {}},
		TaskFailed:         {TaskProcessing: {}, TaskImportQueued: {}},
	}
	return validateTransition("task", string(from), string(to), allowed[from], to)
}

type VideoState string

const (
	VideoTranscodeQueued VideoState = "transcode_queued"
	VideoTranscoding     VideoState = "transcoding"
	VideoReady           VideoState = "video_ready"
	VideoFailed          VideoState = "failed"
	VideoCancelled       VideoState = "cancelled"
)

func ValidateVideoTransition(from, to VideoState) error {
	allowed := map[VideoState]map[VideoState]struct{}{
		VideoTranscodeQueued: {VideoTranscoding: {}, VideoFailed: {}, VideoCancelled: {}},
		VideoTranscoding:     {VideoReady: {}, VideoFailed: {}, VideoCancelled: {}},
		VideoFailed:          {VideoTranscodeQueued: {}},
	}
	return validateTransition("video", string(from), string(to), allowed[from], to)
}

type SubtitleState string

const (
	SubtitleQueued               SubtitleState = "subtitle_queued"
	SubtitleExtractingConverting SubtitleState = "extracting_or_converting"
	SubtitleASSReady             SubtitleState = "ass_ready"
	SubtitleFailed               SubtitleState = "failed"
	SubtitleCancelled            SubtitleState = "cancelled"
)

func ValidateSubtitleTransition(from, to SubtitleState) error {
	allowed := map[SubtitleState]map[SubtitleState]struct{}{
		SubtitleQueued:               {SubtitleExtractingConverting: {}, SubtitleFailed: {}, SubtitleCancelled: {}},
		SubtitleExtractingConverting: {SubtitleASSReady: {}, SubtitleFailed: {}, SubtitleCancelled: {}},
		SubtitleFailed:               {SubtitleQueued: {}},
	}
	return validateTransition("subtitle", string(from), string(to), allowed[from], to)
}

type CleanupState string

const (
	CleanupQueued    CleanupState = "queued"
	CleanupRunning   CleanupState = "running"
	CleanupCompleted CleanupState = "completed"
	CleanupFailed    CleanupState = "failed"
	CleanupCancelled CleanupState = "cancelled"
)

func ValidateCleanupTransition(from, to CleanupState) error {
	allowed := map[CleanupState]map[CleanupState]struct{}{
		CleanupQueued:  {CleanupRunning: {}, CleanupFailed: {}, CleanupCancelled: {}},
		CleanupRunning: {CleanupCompleted: {}, CleanupFailed: {}, CleanupCancelled: {}},
	}
	return validateTransition("cleanup", string(from), string(to), allowed[from], to)
}

// ReadyToFinalize is true only while a task is processing and both independent
// media branches have completed. The task state guard makes finalization enqueue
// idempotent under concurrent branch completion.
func ReadyToFinalize(task TaskState, video VideoState, subtitle SubtitleState) bool {
	return task == TaskProcessing && video == VideoReady && subtitle == SubtitleASSReady
}

func validateTransition[T ~string](machine, from, to string, allowed map[T]struct{}, target T) error {
	if _, ok := allowed[target]; ok {
		return nil
	}
	return &TransitionError{Machine: machine, From: from, To: to}
}
