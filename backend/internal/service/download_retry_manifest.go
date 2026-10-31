package service

import (
	"context"
	"fmt"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

// 保留已确认清单中的文件标识、选择及剧集坐标，避免恢复下载使显式映射失效。
func restoreResolvedDownloadManifest(ctx context.Context, queries *db.Queries, download db.Download, completion *domain.DownloadEnqueueCompletion) (bool, error) {
	existing, err := queries.ListDownloadFiles(ctx, download.ID)
	if err != nil {
		return false, fmt.Errorf("load previous download manifest: %w", err)
	}
	if len(existing) == 0 {
		return false, nil
	}
	if download.FileResolutionSource == nil {
		if err := queries.DeleteDownloadFilesByDownloadID(ctx, download.ID); err != nil {
			return false, fmt.Errorf("clear unresolved download manifest: %w", err)
		}
		return false, nil
	}
	changed := func() (bool, error) {
		return false, NewError("download_manifest_changed", "the source manifest no longer matches the verified file selection", ErrStateConflict, map[string]any{})
	}
	if len(existing) != len(completion.Files) || completion.Outcome == domain.DownloadManifestHardRejected {
		return changed()
	}
	byIndex := make(map[int32]db.DownloadFile, len(existing))
	for _, file := range existing {
		byIndex[file.FileIndex] = file
	}
	files := append([]domain.ClassifiedDownloadFile(nil), completion.Files...)
	for index, file := range files {
		previous, ok := byIndex[int32(file.Index)]
		if !ok || file.Index < 0 || int(previous.FileIndex) != file.Index || previous.RelativePath != file.RelativePath || previous.SizeBytes != file.SizeBytes {
			return changed()
		}
		delete(byIndex, previous.FileIndex)
		file.Kind, file.Selected = domain.MediaKind(previous.MediaKind), previous.Selected
		file.SourceSeason, file.SourceEpisode = 0, 0
		if previous.SourceSeason != nil {
			file.SourceSeason = int(*previous.SourceSeason)
		}
		if previous.SourceEpisode != nil {
			file.SourceEpisode = int(*previous.SourceEpisode)
		}
		file.SourceEpisodeFractionHundredths = int(previous.SourceEpisodeFractionHundredths)
		file.Language = valueOrEmpty(previous.Language)
		files[index] = file
	}
	completion.Files = files
	completion.Outcome = domain.DownloadManifestResolved
	return true, nil
}
