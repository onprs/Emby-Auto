package service

import "github.com/onprs/emby-auto/backend/internal/domain"

// isTaskRetryable 供任务详情与获取记录摘要复用；取消后的恢复还需事务守卫确认旧操作已退出。
func isTaskRetryable(state, videoState, subtitleState, failureStage, cleanupStatus string) bool {
	if state == string(domain.TaskImported) && cleanupStatus == string(domain.CleanupFailed) {
		return true
	}
	hasMediaFailed := videoState == string(domain.VideoFailed) || subtitleState == string(domain.SubtitleFailed)
	if state == string(domain.TaskFailed) && (failureStage != "" || hasMediaFailed) {
		return true
	}
	if state == string(domain.TaskProcessing) && hasMediaFailed {
		return true
	}
	return isCancelledMediaRecoverable(state, videoState, subtitleState)
}

// 只恢复所有媒体分支均已终止的任务，保留已完成产物。
func isCancelledMediaRecoverable(state, videoState, subtitleState string) bool {
	if state != string(domain.TaskCancelled) {
		return false
	}
	videoStopped := videoState == string(domain.VideoFailed) || videoState == string(domain.VideoCancelled) || videoState == string(domain.VideoReady)
	subtitleStopped := subtitleState == string(domain.SubtitleFailed) || subtitleState == string(domain.SubtitleCancelled) || subtitleState == string(domain.SubtitleASSReady)
	return videoStopped && subtitleStopped
}
