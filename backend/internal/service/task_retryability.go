package service

import "github.com/onprs/emby-auto/backend/internal/domain"

// isTaskRetryable 是唯一的任务可重试判定，供完整 Task 与 Acquisition 摘要复用。
// 保持：
//   - failed 且有 failureStage 或媒体分支 failed
//   - processing 且至少一个媒体分支 failed
//   - imported 且 cleanup failed
//   - cancelled 仅当至少一个分支 failed 且 video 仅为 failed|video_ready 且 subtitle 仅为 failed|ass_ready
func isTaskRetryable(state, videoState, subtitleState, failureStage, cleanupStatus string) bool {
	if state == string(domain.TaskImported) && cleanupStatus == string(domain.CleanupFailed) {
		return true
	}
	videoFailed := videoState == string(domain.VideoFailed)
	subtitleFailed := subtitleState == string(domain.SubtitleFailed)
	hasMediaFailed := videoFailed || subtitleFailed
	if state == string(domain.TaskFailed) && (failureStage != "" || hasMediaFailed) {
		return true
	}
	if state == string(domain.TaskProcessing) && hasMediaFailed {
		return true
	}
	if state == string(domain.TaskCancelled) && hasMediaFailed {
		// 严格限定：只接受 failed/ready 组合，任何 queued/running/cancelled 均拒绝
		if videoState != string(domain.VideoFailed) && videoState != string(domain.VideoReady) {
			return false
		}
		if subtitleState != string(domain.SubtitleFailed) && subtitleState != string(domain.SubtitleASSReady) {
			return false
		}
		return true
	}
	return false
}

// isCancelledMediaRecoverable 仅判断 cancelled 状态是否满足安全媒体恢复条件。
func isCancelledMediaRecoverable(state, videoState, subtitleState string) bool {
	if state != string(domain.TaskCancelled) {
		return false
	}
	videoFailed := videoState == string(domain.VideoFailed)
	subtitleFailed := subtitleState == string(domain.SubtitleFailed)
	hasMediaFailed := videoFailed || subtitleFailed
	if !hasMediaFailed {
		return false
	}
	if videoState != string(domain.VideoFailed) && videoState != string(domain.VideoReady) {
		return false
	}
	if subtitleState != string(domain.SubtitleFailed) && subtitleState != string(domain.SubtitleASSReady) {
		return false
	}
	return true
}
