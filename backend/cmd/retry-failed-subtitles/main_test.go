package main

import (
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestIsLegacySubtitleFailureMatchesOnlyRetryableOldPipelineFailures(t *testing.T) {
	base := domain.EpisodeTask{
		ID:            uuid.New(),
		State:         domain.TaskFailed,
		SubtitleState: domain.SubtitleFailed,
		FailureStage:  "subtitle",
		Operations: []domain.OperationSummary{{
			Kind: "subtitle.prepare", Status: "failed", ErrorCode: "subtitle_output_invalid",
		}},
	}
	tests := []struct {
		name string
		task domain.EpisodeTask
		want bool
	}{
		{name: "old invalid output", task: base, want: true},
		{name: "old missing simplified subtitle", task: withOperationCode(base, "simplified_chinese_subtitle_not_found"), want: true},
		{name: "new candidate exhaustion", task: withOperationCode(base, "subtitle_candidates_exhausted")},
		{name: "video failure", task: withFailureStage(base, "video")},
		{name: "already imported", task: withTaskState(base, domain.TaskImported)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isLegacySubtitleFailure(test.task); got != test.want {
				t.Fatalf("isLegacySubtitleFailure() = %t, want %t", got, test.want)
			}
		})
	}
}

func withOperationCode(task domain.EpisodeTask, code string) domain.EpisodeTask {
	task.Operations = []domain.OperationSummary{{Kind: "subtitle.prepare", Status: "failed", ErrorCode: code}}
	return task
}

func withFailureStage(task domain.EpisodeTask, stage string) domain.EpisodeTask {
	task.FailureStage = stage
	return task
}

func withTaskState(task domain.EpisodeTask, state domain.TaskState) domain.EpisodeTask {
	task.State = state
	return task
}
