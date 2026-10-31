package service

import (
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

// acquisitionStageMatches groups the user-facing aggregate statuses into the
// task stages presented by the interface.
func acquisitionStageMatches(phase string, view domain.AcquisitionView) bool {
	switch phase {
	case "downloading":
		return view.AggregateStatus == "pending" || view.AggregateStatus == "downloading"
	case "processing":
		return view.AggregateStatus == "materializing" || view.AggregateStatus == "processing"
	case "awaiting_review":
		return view.AggregateStatus == "awaiting_review"
	case "importing":
		return view.AggregateStatus == "importing"
	case "completed":
		return view.AggregateStatus == "completed"
	case "mapping_pending":
		return view.AggregateStatus == "mapping_pending"
	case "attention":
		_, needsAttention := dashboardAttentionItem(view)
		return needsAttention
	default:
		return false
	}
}

func dashboardAttentionItem(view domain.AcquisitionView) (domain.DashboardAttentionItem, bool) {
	item := domain.DashboardAttentionItem{Acquisition: view}
	switch view.AggregateStatus {
	case "failed":
		item.Reason = "workflow_failed"
		if view.Download != nil && view.Download.Status == "failed" {
			item.ErrorCode = view.Download.ErrorCode
			item.ErrorMessage = view.Download.ErrorMessage
			return item, true
		}
		for _, task := range view.Tasks {
			if task.State == "failed" || task.ImportStatus == "failed" {
				item.ErrorCode = task.ErrorCode
				item.ErrorMessage = task.ErrorMessage
				break
			}
		}
		return item, true
	case "mapping_pending":
		item.Reason = "mapping_required"
		return item, true
	}
	for _, task := range view.Tasks {
		if task.CleanupStatus == "failed" {
			item.Reason = "cleanup_failed"
			return item, true
		}
	}
	for _, task := range view.Tasks {
		if task.EmbyRefreshStatus == "failed" {
			item.Reason = "emby_refresh_failed"
			return item, true
		}
	}
	if view.AggregateStatus == "rejected" {
		item.Reason = "review_rejected"
		return item, true
	}
	return domain.DashboardAttentionItem{}, false
}

const (
	stagePending   = "pending"
	stageBlocked   = "blocked"
	stageRunning   = "running"
	stageWaiting   = "waiting"
	stageCompleted = "completed"
	stageFailed    = "failed"
	stageRejected  = "rejected"
	stageCancelled = "cancelled"
	stageSkipped   = "skipped"

	acquisitionSourceWeight    = 0.02
	acquisitionDownloadWeight  = 0.28
	acquisitionMappingWeight   = 0.05
	acquisitionTranscodeWeight = 0.35
	acquisitionSubtitleWeight  = 0.10
	acquisitionRenameWeight    = 0.03
	acquisitionOrganizeWeight  = 0.03
	acquisitionReviewWeight    = 0.05
	acquisitionImportWeight    = 0.09
)

func deriveAcquisitionLifecycle(view *domain.AcquisitionView, downloadStatus string) {
	mappingDownloadStatus := downloadStatus
	if acquisitionWaitsForMapping(*view) {
		mappingDownloadStatus = "completed"
	}
	stages := []domain.AcquisitionStageView{
		{Key: "source", Status: stageCompleted, Progress: 1, CompletedItems: 1, TotalItems: 1, UpdatedAt: timeValue(view.CreatedAt)},
		downloadAcquisitionStage(view.Download),
		mappingAcquisitionStage(*view, mappingDownloadStatus),
		mediaBranchAcquisitionStage("transcode", view.Tasks, func(task domain.AcquisitionTaskSummary) string { return task.VideoState }),
		mediaBranchAcquisitionStage("subtitle", view.Tasks, func(task domain.AcquisitionTaskSummary) string { return task.SubtitleState }),
		artifactAcquisitionStage("rename", view.Tasks),
		artifactAcquisitionStage("organize", view.Tasks),
		reviewAcquisitionStage(view.Tasks),
		importAcquisitionStage(view.Tasks),
	}
	view.Stages = stages
	view.CurrentStage = currentAcquisitionStage(stages)
	view.OverallProgress = acquisitionProgress(stages)
	view.AggregateStatus = acquisitionAggregateStatus(*view, downloadStatus)
}

func downloadAcquisitionStage(download *domain.AcquisitionDownloadSummary) domain.AcquisitionStageView {
	stage := domain.AcquisitionStageView{Key: "download", Status: stagePending, TotalItems: 1}
	if download == nil {
		return stage
	}
	stage.UpdatedAt = timeValue(download.UpdatedAt)
	stage.Progress = max(0, min(1, download.Progress))
	switch download.Status {
	case "completed", "selecting_files", "materialized":
		stage.Status, stage.Progress, stage.CompletedItems = stageCompleted, 1, 1
	case "failed":
		if download.FailureStage == "materialize" && download.Progress >= 1 {
			stage.Status, stage.Progress, stage.CompletedItems = stageCompleted, 1, 1
		} else {
			stage.Status = stageFailed
		}
	case "cancelled":
		stage.Status = stageCancelled
	case "enqueue_pending", "file_resolution_pending", "downloading":
		stage.Status = stageRunning
	}
	return stage
}

func mappingAcquisitionStage(view domain.AcquisitionView, downloadStatus string) domain.AcquisitionStageView {
	if view.MediaType == domain.TaskMediaMovie {
		return domain.AcquisitionStageView{Key: "mapping", Status: stageSkipped, Progress: 1}
	}
	stage := domain.AcquisitionStageView{
		Key: "mapping", Status: stagePending, CompletedItems: view.Mapping.MappedVideoCount,
		TotalItems: view.Mapping.SelectedVideoCount, UpdatedAt: latestTaskTime(view.Tasks),
	}
	if stage.TotalItems > 0 {
		stage.Progress = float64(stage.CompletedItems) / float64(stage.TotalItems)
	}
	switch {
	case view.Mapping.Complete:
		stage.Status, stage.Progress = stageCompleted, 1
	case downloadStatus == "failed":
		stage.Status = stageBlocked
	case downloadStatus == "cancelled":
		stage.Status = stageCancelled
	case downloadStatus == "completed" || downloadStatus == "selecting_files" || downloadStatus == "materialized":
		stage.Status = stageWaiting
	}
	return stage
}

func mediaBranchAcquisitionStage(
	key string,
	tasks []domain.AcquisitionTaskSummary,
	stateOf func(domain.AcquisitionTaskSummary) string,
) domain.AcquisitionStageView {
	stage := domain.AcquisitionStageView{Key: key, Status: stagePending, TotalItems: len(tasks), UpdatedAt: latestTaskTime(tasks)}
	if len(tasks) == 0 {
		return stage
	}
	failed, cancelled, running := 0, 0, 0
	progressUnits := float64(0)
	for _, task := range tasks {
		state := stateOf(task)
		ready := (key == "transcode" && state == "video_ready") || (key == "subtitle" && state == "ass_ready")
		switch {
		case ready:
			stage.CompletedItems++
			progressUnits++
		case state == "failed" || (task.State == "failed" && task.FailureStage == branchFailureStage(key)):
			failed++
		case state == "cancelled":
			cancelled++
		case state == "transcoding" || state == "extracting_or_converting":
			running++
			progressUnits += 0.5
		}
	}
	stage.Progress = progressUnits / float64(stage.TotalItems)
	switch {
	case failed > 0:
		stage.Status = stageFailed
	case stage.CompletedItems == stage.TotalItems:
		stage.Status, stage.Progress = stageCompleted, 1
	case cancelled > 0 && stage.CompletedItems+cancelled == stage.TotalItems:
		stage.Status = stageCancelled
	case running > 0 || stage.CompletedItems > 0:
		stage.Status = stageRunning
	}
	return stage
}

func branchFailureStage(key string) string {
	if key == "transcode" {
		return "video"
	}
	return "subtitle"
}

func artifactAcquisitionStage(key string, tasks []domain.AcquisitionTaskSummary) domain.AcquisitionStageView {
	stage := domain.AcquisitionStageView{Key: key, Status: stagePending, TotalItems: len(tasks), UpdatedAt: latestTaskTime(tasks)}
	if len(tasks) == 0 {
		return stage
	}
	failed, cancelled, running := 0, 0, 0
	for _, task := range tasks {
		switch {
		case task.ArtifactBasename != "":
			stage.CompletedItems++
		case task.State == "failed" && task.FailureStage == "finalize":
			failed++
		case task.State == "cancelled":
			cancelled++
		case task.State == "finalizing":
			running++
		}
	}
	stage.Progress = float64(stage.CompletedItems) / float64(stage.TotalItems)
	switch {
	case failed > 0:
		stage.Status = stageFailed
	case stage.CompletedItems == stage.TotalItems:
		stage.Status, stage.Progress = stageCompleted, 1
	case cancelled > 0 && stage.CompletedItems+cancelled == stage.TotalItems:
		stage.Status = stageCancelled
	case running > 0 || stage.CompletedItems > 0:
		stage.Status = stageRunning
	}
	return stage
}

func reviewAcquisitionStage(tasks []domain.AcquisitionTaskSummary) domain.AcquisitionStageView {
	stage := domain.AcquisitionStageView{Key: "review", Status: stagePending, TotalItems: len(tasks), UpdatedAt: latestTaskTime(tasks)}
	if len(tasks) == 0 {
		return stage
	}
	rejected, cancelled, waiting := 0, 0, 0
	for _, task := range tasks {
		switch task.ReviewDecision {
		case "approved":
			stage.CompletedItems++
			if task.ReviewedAt != nil {
				stage.UpdatedAt = laterTimePointer(stage.UpdatedAt, *task.ReviewedAt)
			}
		case "rejected":
			stage.CompletedItems++
			rejected++
			if task.ReviewedAt != nil {
				stage.UpdatedAt = laterTimePointer(stage.UpdatedAt, *task.ReviewedAt)
			}
		default:
			if task.State == "awaiting_review" {
				waiting++
			}
			if task.State == "cancelled" {
				cancelled++
			}
		}
	}
	stage.Progress = float64(stage.CompletedItems) / float64(stage.TotalItems)
	switch {
	case rejected > 0:
		stage.Status = stageRejected
	case stage.CompletedItems == stage.TotalItems:
		stage.Status, stage.Progress = stageCompleted, 1
	case cancelled > 0 && stage.CompletedItems+cancelled == stage.TotalItems:
		stage.Status = stageCancelled
	case waiting > 0 || stage.CompletedItems > 0:
		stage.Status = stageWaiting
	}
	return stage
}

func importAcquisitionStage(tasks []domain.AcquisitionTaskSummary) domain.AcquisitionStageView {
	stage := domain.AcquisitionStageView{Key: "import", Status: stagePending, TotalItems: len(tasks), UpdatedAt: latestTaskTime(tasks)}
	if len(tasks) == 0 {
		return stage
	}
	failed, rejected, cancelled, active, waiting := 0, 0, 0, 0, 0
	progressUnits := float64(0)
	for _, task := range tasks {
		switch {
		case task.ReviewDecision == "rejected" || task.State == "rejected":
			rejected++
		case task.State == "imported" && task.ImportStatus == "succeeded":
			stage.CompletedItems++
			progressUnits++
		case task.State == "failed" && task.FailureStage == "import" || task.ImportStatus == "failed":
			failed++
		case task.State == "cancelled" || task.ImportStatus == "cancelled":
			cancelled++
		case task.State == "importing" || task.ImportStatus == "running":
			active++
			progressUnits += 0.5
		case task.State == "import_queued" || task.ImportStatus == "queued":
			active++
			progressUnits += 0.25
		case task.State == "approved":
			waiting++
		}
	}
	stage.Progress = progressUnits / float64(stage.TotalItems)
	switch {
	case failed > 0:
		stage.Status = stageFailed
	case rejected == stage.TotalItems:
		stage.Status, stage.Progress = stageSkipped, 1
	case rejected > 0:
		stage.Status = stageRejected
	case stage.CompletedItems == stage.TotalItems:
		stage.Status, stage.Progress = stageCompleted, 1
	case cancelled > 0 && stage.CompletedItems+cancelled == stage.TotalItems:
		stage.Status = stageCancelled
	case active > 0 || stage.CompletedItems > 0:
		stage.Status = stageRunning
	case waiting > 0:
		stage.Status = stageWaiting
	}
	return stage
}

func currentAcquisitionStage(stages []domain.AcquisitionStageView) string {
	for _, status := range []string{stageFailed, stageRejected, stageCancelled} {
		for _, stage := range stages {
			if stage.Status == status {
				return stage.Key
			}
		}
	}
	for _, stage := range stages {
		switch stage.Status {
		case stageRunning, stageWaiting, stageBlocked, stagePending:
			return stage.Key
		}
	}
	return "import"
}

func acquisitionProgress(stages []domain.AcquisitionStageView) float64 {
	totalWeight, weightedProgress := float64(0), float64(0)
	for _, stage := range stages {
		if stage.Status == stageSkipped {
			continue
		}
		weight := acquisitionStageWeight(stage.Key)
		totalWeight += weight
		weightedProgress += weight * max(0, min(1, stage.Progress))
	}
	if totalWeight == 0 {
		return 0
	}
	return max(0, min(1, weightedProgress/totalWeight))
}

func acquisitionStageWeight(key string) float64 {
	switch key {
	case "source":
		return acquisitionSourceWeight
	case "download":
		return acquisitionDownloadWeight
	case "mapping":
		return acquisitionMappingWeight
	case "transcode":
		return acquisitionTranscodeWeight
	case "subtitle":
		return acquisitionSubtitleWeight
	case "rename":
		return acquisitionRenameWeight
	case "organize":
		return acquisitionOrganizeWeight
	case "review":
		return acquisitionReviewWeight
	case "import":
		return acquisitionImportWeight
	default:
		return 0
	}
}

func acquisitionAggregateStatus(view domain.AcquisitionView, downloadStatus string) string {
	if len(view.Tasks) > 0 {
		allStopped := true
		for _, task := range view.Tasks {
			allStopped = allStopped && task.State == "cancelled"
		}
		if allStopped {
			return "cancelled"
		}
	}
	if (downloadStatus == "failed" && !acquisitionWaitsForMapping(view)) || acquisitionStageStatus(view.Stages, stageFailed) {
		return "failed"
	}
	if acquisitionStageKeyStatus(view.Stages, "review") == stageRejected || acquisitionStageKeyStatus(view.Stages, "import") == stageRejected {
		return "rejected"
	}
	if downloadStatus == "cancelled" || acquisitionStageStatus(view.Stages, stageCancelled) {
		return "cancelled"
	}
	if acquisitionStageKeyStatus(view.Stages, "import") == stageCompleted {
		return "completed"
	}
	switch view.CurrentStage {
	case "source":
		return "pending"
	case "download":
		if downloadStatus == "" {
			return "pending"
		}
		return "downloading"
	case "mapping":
		return "mapping_pending"
	case "transcode", "subtitle", "rename", "organize":
		if downloadStatus == "completed" || downloadStatus == "selecting_files" {
			return "materializing"
		}
		return "processing"
	case "review":
		return "awaiting_review"
	case "import":
		return "importing"
	default:
		return "processing"
	}
}

var mappingMaterializationErrorCodes = map[string]struct{}{
	"mapping_profile_required":    {},
	"episode_mapping_required":    {},
	"mapping_source_invalid":      {},
	"mapping_source_out_of_range": {},
	"mapping_context_incomplete":  {},
	"mapping_target_out_of_range": {},
	"mapping_title_missing":       {},
}

func isMappingMaterializationFailure(status, failureStage, errorCode string) bool {
	if status != "failed" || failureStage != "materialize" {
		return false
	}
	_, ok := mappingMaterializationErrorCodes[errorCode]
	return ok
}

func acquisitionWaitsForMapping(view domain.AcquisitionView) bool {
	return view.MediaType == domain.TaskMediaEpisode && !view.Mapping.Complete && view.Download != nil &&
		isMappingMaterializationFailure(view.Download.Status, view.Download.FailureStage, view.Download.ErrorCode)
}

func acquisitionStageStatus(stages []domain.AcquisitionStageView, status string) bool {
	for _, stage := range stages {
		if stage.Status == status {
			return true
		}
	}
	return false
}

func acquisitionStageKeyStatus(stages []domain.AcquisitionStageView, key string) string {
	for _, stage := range stages {
		if stage.Key == key {
			return stage.Status
		}
	}
	return ""
}

func latestTaskTime(tasks []domain.AcquisitionTaskSummary) *time.Time {
	var latest *time.Time
	for _, task := range tasks {
		latest = laterTimePointer(latest, task.UpdatedAt)
	}
	return latest
}
