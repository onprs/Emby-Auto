package domain

import (
	"errors"
	"testing"
)

func TestStateMachinesAcceptDefinedTransitions(t *testing.T) {
	tests := []struct {
		name     string
		validate func() error
	}{
		{name: "rss discovered to enqueueing", validate: func() error { return ValidateRSSTransition(RSSDiscovered, RSSEnqueueing) }},
		{name: "rss failed retry", validate: func() error { return ValidateRSSTransition(RSSEnqueueFailed, RSSEnqueueing) }},
		{name: "download selecting to materialized", validate: func() error { return ValidateDownloadTransition(DownloadSelectingFiles, DownloadMaterialized) }},
		{name: "download active to cancelled", validate: func() error { return ValidateDownloadTransition(DownloadDownloading, DownloadCancelled) }},
		{name: "task processing to finalizing", validate: func() error { return ValidateTaskTransition(TaskProcessing, TaskFinalizing) }},
		{name: "task review approval", validate: func() error { return ValidateTaskTransition(TaskAwaitingReview, TaskApproved) }},
		{name: "task import completion", validate: func() error { return ValidateTaskTransition(TaskImporting, TaskImported) }},
		{name: "failed media retry", validate: func() error { return ValidateTaskTransition(TaskFailed, TaskProcessing) }},
		{name: "failed import retry", validate: func() error { return ValidateTaskTransition(TaskFailed, TaskImportQueued) }},
		{name: "video failed branch retry", validate: func() error { return ValidateVideoTransition(VideoFailed, VideoTranscodeQueued) }},
		{name: "subtitle failed branch retry", validate: func() error { return ValidateSubtitleTransition(SubtitleFailed, SubtitleQueued) }},
		{name: "cleanup completion", validate: func() error { return ValidateCleanupTransition(CleanupRunning, CleanupCompleted) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate(); err != nil {
				t.Fatalf("transition error = %v, want nil", err)
			}
		})
	}
}

func TestStateMachinesRejectSkippedAndTerminalTransitions(t *testing.T) {
	tests := []struct {
		name     string
		validate func() error
		machine  string
		from     string
		to       string
	}{
		{name: "rss skips enqueue", validate: func() error { return ValidateRSSTransition(RSSDiscovered, RSSEnqueued) }, machine: "rss", from: "discovered", to: "enqueued"},
		{name: "materialized download is terminal", validate: func() error { return ValidateDownloadTransition(DownloadMaterialized, DownloadDownloading) }, machine: "download", from: "materialized", to: "downloading"},
		{name: "task skips review", validate: func() error { return ValidateTaskTransition(TaskFinalizing, TaskApproved) }, machine: "task", from: "finalizing", to: "approved"},
		{name: "imported task cannot roll back", validate: func() error { return ValidateTaskTransition(TaskImported, TaskFailed) }, machine: "task", from: "imported", to: "failed"},
		{name: "rejected task is terminal", validate: func() error { return ValidateTaskTransition(TaskRejected, TaskProcessing) }, machine: "task", from: "rejected", to: "processing"},
		{name: "cancelled task cannot directly retry", validate: func() error { return ValidateTaskTransition(TaskCancelled, TaskProcessing) }, machine: "task", from: "cancelled", to: "processing"},
		{name: "ready video cannot restart", validate: func() error { return ValidateVideoTransition(VideoReady, VideoTranscoding) }, machine: "video", from: "video_ready", to: "transcoding"},
		{name: "ready subtitle cannot restart", validate: func() error { return ValidateSubtitleTransition(SubtitleASSReady, SubtitleExtractingConverting) }, machine: "subtitle", from: "ass_ready", to: "extracting_or_converting"},
		{name: "cleanup failure is terminal per attempt", validate: func() error { return ValidateCleanupTransition(CleanupFailed, CleanupRunning) }, machine: "cleanup", from: "failed", to: "running"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.validate()
			var transitionErr *TransitionError
			if !errors.As(err, &transitionErr) {
				t.Fatalf("error = %v, want TransitionError", err)
			}
			if transitionErr.Machine != test.machine || transitionErr.From != test.from || transitionErr.To != test.to {
				t.Fatalf("error = %#v, want machine=%q from=%q to=%q", transitionErr, test.machine, test.from, test.to)
			}
		})
	}
}

func TestReadyToFinalizeRequiresBothBranchesAndProcessingState(t *testing.T) {
	tests := []struct {
		name     string
		task     TaskState
		video    VideoState
		subtitle SubtitleState
		want     bool
	}{
		{name: "both branches ready", task: TaskProcessing, video: VideoReady, subtitle: SubtitleASSReady, want: true},
		{name: "video still running", task: TaskProcessing, video: VideoTranscoding, subtitle: SubtitleASSReady, want: false},
		{name: "subtitle still running", task: TaskProcessing, video: VideoReady, subtitle: SubtitleExtractingConverting, want: false},
		{name: "video failed", task: TaskProcessing, video: VideoFailed, subtitle: SubtitleASSReady, want: false},
		{name: "already finalizing", task: TaskFinalizing, video: VideoReady, subtitle: SubtitleASSReady, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ReadyToFinalize(test.task, test.video, test.subtitle); got != test.want {
				t.Fatalf("ReadyToFinalize() = %t, want %t", got, test.want)
			}
		})
	}
}
