package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

// importRecoveryQueryTimeout 限制导入提交失败后的状态确认时间。
// 使用与任务上下文解绑的短超时，避免取消后无法判断导入记录是否已提交。
const importRecoveryQueryTimeout = 5 * time.Second

type TaskImportStore interface {
	BeginImport(context.Context, uuid.UUID, uuid.UUID) (domain.ImportCommand, error)
	CompleteImport(context.Context, domain.ImportCompletion) error
	ObserveImportRecovery(context.Context, uuid.UUID, uuid.UUID, string, string) (domain.ImportRecoveryState, error)
}

// importCompensationStore 扩展观察接口：允许实现以回调执行“持锁删除”。
// 观察事务在释放目标路径锁之前调用回滚闭包，使补偿删除与成功引用写入
// 在同一临界区内串行，观察后窗口内的采用无法与删除交错。
type importCompensationStore interface {
	TaskImportStore
	// RunImportCompensation 在单个持锁事务内执行观察与回滚决策：
	// rollback 返回 true 表示确认未成功且未被占用，闭包内完成文件删除。
	RunImportCompensation(
		ctx context.Context,
		completion domain.ImportCompletion,
		rollback func() bool,
	) (domain.ImportRecoveryState, error)
}

type EmbyImportHandler struct {
	configuration MediaConfiguration
	store         TaskImportStore
	libraryAccess ImportedLibraryAccess
}

type embyImportPayload struct {
	ImportID uuid.UUID `json:"importId"`
}

func NewEmbyImportHandler(
	configuration MediaConfiguration,
	store TaskImportStore,
	libraryAccess ImportedLibraryAccess,
) *EmbyImportHandler {
	return &EmbyImportHandler{configuration: configuration, store: store, libraryAccess: libraryAccess}
}

func (handler *EmbyImportHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "episode_task" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_import_operation", "emby.import requires an episode task resource", nil)
	}
	if handler.configuration == nil || handler.store == nil || handler.libraryAccess == nil {
		return permanentFailure("import_handler_not_configured", "Emby import handler dependencies are unavailable", nil)
	}
	var payload embyImportPayload
	if json.Unmarshal(operation.Payload, &payload) != nil || payload.ImportID == uuid.Nil {
		return permanentFailure("invalid_import_operation", "emby.import payload requires an import ID", nil)
	}
	command, err := handler.store.BeginImport(ctx, operation.ResourceID, payload.ImportID)
	if err != nil {
		return mediaStoreFailure("import", err)
	}
	if command.TaskID != operation.ResourceID || command.ImportID != payload.ImportID {
		return permanentFailure("import_resource_mismatch", "the operation does not match its task import", nil)
	}
	if command.TaskState == domain.TaskImported && command.ImportState == "succeeded" {
		return nil
	}
	configuration, err := handler.configuration.Load(ctx)
	if err != nil {
		return retryableFailure("configuration_unavailable", "runtime configuration is unavailable", err)
	}
	videoName := filepath.Base(command.Video.FilePath)
	subtitleName := filepath.Base(command.Subtitle.FilePath)
	var libraryRoot string
	var relative domain.LibraryRelativePaths
	switch command.MediaType {
	case domain.TaskMediaMovie:
		libraryRoot = strings.TrimSpace(configuration.Settings.Paths.MovieLibraryRoot)
		if libraryRoot == "" {
			return permanentFailure("movie_library_root_not_configured", "the movie library root is not configured", nil)
		}
		relative, err = domain.BuildMovieLibraryRelativePaths(command.MovieTitle, command.ReleaseYear, videoName, subtitleName)
	case domain.TaskMediaEpisode, "":
		libraryRoot = strings.TrimSpace(configuration.Settings.Paths.EffectiveAnimeLibraryRoot())
		if libraryRoot == "" {
			return permanentFailure("anime_library_root_not_configured", "the anime library root is not configured", nil)
		}
		relative, err = domain.BuildLibraryRelativePaths(command.SeriesTitle, command.Season, videoName, subtitleName)
	default:
		return permanentFailure("media_type_invalid", "the import task has an unsupported media type", nil)
	}
	if err != nil {
		return permanentFailure("library_path_invalid", "the Emby library destination is invalid", err)
	}
	destinationVideo, err := secureJoin(libraryRoot, relative.Video)
	if err != nil {
		return permanentFailure("library_path_invalid", "the video library destination is unsafe", err)
	}
	destinationSubtitle, err := secureJoin(libraryRoot, relative.Subtitle)
	if err != nil {
		return permanentFailure("library_path_invalid", "the subtitle library destination is unsafe", err)
	}
	created, err := importArtifactPair(
		ctx,
		command.Video,
		command.Subtitle,
		libraryRoot,
		destinationVideo,
		destinationSubtitle,
		operation.ID,
		handler.libraryAccess,
	)
	if err != nil {
		var treeErr *unsupportedLibraryTreeError
		if errors.As(err, &treeErr) {
			return permanentFailure("library_tree_unsupported", "the Emby library directory layout is not supported", err)
		}
		var conflict *destinationConflictError
		if errors.As(err, &conflict) {
			return permanentFailure("library_destination_conflict", "an Emby library destination contains different content", err)
		}
		return retryableFailure("library_import_failed", "the paired media files could not be imported", err)
	}
	completion := domain.ImportCompletion{
		TaskID:                  command.TaskID,
		ImportID:                command.ImportID,
		OperationID:             operation.ID,
		DestinationVideoPath:    destinationVideo,
		DestinationSubtitlePath: destinationSubtitle,
	}
	if completeErr := handler.store.CompleteImport(ctx, completion); completeErr != nil {
		failure := mediaStoreFailure("import", completeErr)
		var classified *Failure
		if !errors.As(failure, &classified) {
			return failure
		}
		return handler.compensateFailedImport(ctx, completion, created, classified)
	}
	return nil
}

// compensateFailedImport 在数据库确认失败后判断是否安全回滚。
// 观察与删除通过 RunImportCompensation 放入与 CompleteImport 相同的目标路径
// advisory 锁域：成功引用的写入与补偿的观察/删除串行，观察后窗口内的采用
// 要么被阻塞到删除完成（新引用随后按文件不存在重新创建），要么已经提交
// 成功引用并使观察读到 DestinationClaimed。只有导入确知未成功、目标未被
// 占用且本次失败不会重试时才回滚；不支持临界区回调的存储实现退回
// ObserveImportRecovery 快照判定并保留文件（不冒删除风险）。
func (handler *EmbyImportHandler) compensateFailedImport(
	ctx context.Context,
	completion domain.ImportCompletion,
	created []importedCommit,
	failure *Failure,
) error {
	compensateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), importRecoveryQueryTimeout)
	defer cancel()
	if compensationStore, ok := handler.store.(importCompensationStore); ok && len(created) > 0 {
		_, err := compensationStore.RunImportCompensation(compensateCtx, completion, func() bool {
			rollbackImportedCommits(created)
			return true
		})
		if err != nil {
			// 数据库状态未知：保留已提交文件，避免因取消或连接中断盲删。
			return failure
		}
		return failure
	}
	state, err := handler.store.ObserveImportRecovery(
		compensateCtx,
		completion.TaskID,
		completion.ImportID,
		completion.DestinationVideoPath,
		completion.DestinationSubtitlePath,
	)
	if err != nil {
		// 数据库状态未知：保留已提交文件，避免因取消或连接中断盲删。
		return failure
	}
	if state.Succeeded {
		// 提交实际已生效（例如提交后连接中断），按成功处理。
		return nil
	}
	if len(created) == 0 || state.DestinationClaimed || state.ImportState == "running" {
		// 导入行仍在运行说明重试可能采用同一内容，保留文件。
		return failure
	}
	if failure.Retryable && state.TaskState != domain.TaskCancelled {
		// 可重试失败保留文件，下一次尝试会验证并采用同一内容。
		return failure
	}
	rollbackImportedCommits(created)
	return failure
}

type destinationConflictError struct {
	path string
}

func (err *destinationConflictError) Error() string {
	return fmt.Sprintf("destination %q does not match the artifact checksum", err.path)
}

type importDestination struct {
	artifact  domain.MediaArtifact
	final     string
	temporary string
	prepared  bool
}

func importArtifactPair(
	ctx context.Context,
	video, subtitle domain.MediaArtifact,
	libraryRoot, destinationVideo, destinationSubtitle string,
	operationID uuid.UUID,
	libraryAccess ImportedLibraryAccess,
) (committed []importedCommit, importErr error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if operationID == uuid.Nil {
		return nil, fmt.Errorf("import operation ID is required")
	}
	if libraryAccess == nil {
		return nil, fmt.Errorf("imported library access is required")
	}
	if video.BaseName == "" || video.BaseName != subtitle.BaseName {
		return nil, fmt.Errorf("paired import artifacts must share a basename")
	}
	if filepath.Dir(destinationVideo) != filepath.Dir(destinationSubtitle) {
		return nil, fmt.Errorf("paired import destinations must share a directory")
	}
	libraryTreeRoot, err := prepareImportedLibraryDirectories(
		libraryRoot,
		filepath.Dir(destinationVideo),
		libraryAccess,
	)
	if err != nil {
		return nil, err
	}
	// 目录树的符号链接、特殊对象与权限问题必须在提交前暴露，
	// 否则失败后已经提交的媒体文件会变成应用不追踪的孤儿文件。
	if err := libraryAccess.ValidateTree(ctx, libraryTreeRoot); err != nil {
		return nil, err
	}
	destinations := []*importDestination{
		{artifact: video, final: destinationVideo, temporary: importTemporaryPath(destinationVideo, operationID)},
		{artifact: subtitle, final: destinationSubtitle, temporary: importTemporaryPath(destinationSubtitle, operationID)},
	}
	defer func() {
		for _, destination := range destinations {
			_ = os.Remove(destination.temporary)
		}
	}()
	// created 记录本次调用确定创建的文件；本函数内失败时回滚，
	// 成功时返回给调用方，用于数据库确认阶段失败后的补偿判断。
	created := make([]importedCommit, 0, len(destinations))
	defer func() {
		if importErr != nil {
			rollbackImportedCommits(created)
		}
	}()
	for _, destination := range destinations {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		prepared, err := prepareImportDestination(ctx, *destination, libraryAccess)
		if err != nil {
			return nil, err
		}
		destination.prepared = prepared
	}
	for _, destination := range destinations {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		if !destination.prepared {
			continue
		}
		if err := commitTemporaryFile(destination.temporary, destination.final); err != nil {
			// 硬链接已建立但临时文件移除失败时，目标与临时文件指向同一 inode，
			// 可以确认目标由本次操作创建并纳入回滚。
			if committed, adopted := adoptCommittedTemporary(destination.temporary, destination.final, destination.artifact); adopted {
				created = append(created, committed)
				if accessErr := libraryAccess.Apply(destination.final); accessErr != nil {
					return nil, accessErr
				}
				continue
			}
			if existingErr := verifyFileAgainstArtifact(ctx, destination.final, destination.artifact); existingErr == nil {
				// 目标是并发写入者创建的相同内容文件，不属于本次操作，回滚时不能删除。
				if accessErr := libraryAccess.Apply(destination.final); accessErr != nil {
					return nil, accessErr
				}
				continue
			}
			if cause := context.Cause(ctx); cause != nil {
				return nil, cause
			}
			if errors.Is(err, os.ErrExist) {
				return nil, &destinationConflictError{path: destination.final}
			}
			return nil, fmt.Errorf("atomically commit library file: %w", err)
		}
		committed, err := inspectCommittedImport(destination.final, destination.artifact)
		if err != nil {
			return nil, err
		}
		created = append(created, committed)
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	for _, destination := range destinations {
		if err := verifyFileAgainstArtifact(ctx, destination.final, destination.artifact); err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return nil, cause
			}
			return nil, &destinationConflictError{path: destination.final}
		}
		if err := libraryAccess.Apply(destination.final); err != nil {
			return nil, err
		}
	}
	// 提交后再次校验目录树，捕获本操作期间被替换为符号链接或特殊对象的目录；
	// 失败时由上面的 defer 回滚本次创建的文件。
	if err := libraryAccess.ApplyTree(ctx, libraryTreeRoot); err != nil {
		return nil, err
	}
	return created, nil
}

type importedCommit struct {
	path     string
	info     os.FileInfo
	checksum []byte
}

// inspectCommittedImport 记录本次操作提交后的文件身份，用于回滚时确认目标没有被替换。
func inspectCommittedImport(final string, artifact domain.MediaArtifact) (importedCommit, error) {
	info, err := os.Lstat(final)
	if err != nil {
		return importedCommit{}, fmt.Errorf("inspect committed library file: %w", err)
	}
	return importedCommit{path: final, info: info, checksum: artifact.ChecksumSHA256}, nil
}

// adoptCommittedTemporary 判断提交失败时目标是否已由本次操作建立。
// 硬链接回退在 link 成功、临时文件移除失败时会留下同一 inode 的两个路径。
func adoptCommittedTemporary(temporary, final string, artifact domain.MediaArtifact) (importedCommit, bool) {
	temporaryInfo, temporaryErr := os.Lstat(temporary)
	finalInfo, finalErr := os.Lstat(final)
	if temporaryErr != nil || finalErr != nil {
		return importedCommit{}, false
	}
	if temporaryInfo.Mode()&os.ModeSymlink != 0 || finalInfo.Mode()&os.ModeSymlink != 0 {
		return importedCommit{}, false
	}
	if !os.SameFile(temporaryInfo, finalInfo) {
		return importedCommit{}, false
	}
	return importedCommit{path: final, info: finalInfo, checksum: artifact.ChecksumSHA256}, true
}

// rollbackImportedCommits 只删除本次操作提交且身份仍一致的对象。
// inode、大小与修改时间任一变化都说明目标已被替换或修改，保留现文件；
// 三者均匹配时再核对内容校验和，避免文件系统重用 inode 或时间精度不足时误删。
func rollbackImportedCommits(commits []importedCommit) {
	for index := len(commits) - 1; index >= 0; index-- {
		commit := commits[index]
		if commit.info == nil {
			continue
		}
		current, err := os.Lstat(commit.path)
		if err != nil || !current.Mode().IsRegular() || !os.SameFile(commit.info, current) {
			continue
		}
		if current.Size() != commit.info.Size() || !current.ModTime().Equal(commit.info.ModTime()) {
			continue
		}
		if err := verifyFileAgainstArtifact(context.Background(), commit.path, domain.MediaArtifact{
			FilePath: commit.path, SizeBytes: current.Size(), ChecksumSHA256: commit.checksum,
		}); err != nil {
			continue
		}
		_ = os.Remove(commit.path)
	}
}

func prepareImportDestination(ctx context.Context, destination importDestination, libraryAccess ImportedLibraryAccess) (bool, error) {
	if _, err := os.Lstat(destination.final); err == nil {
		if err := verifyFileAgainstArtifact(ctx, destination.final, destination.artifact); err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return false, cause
			}
			return false, &destinationConflictError{path: destination.final}
		}
		if err := libraryAccess.Apply(destination.final); err != nil {
			return false, err
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	info, err := os.Lstat(destination.artifact.FilePath)
	if err != nil {
		return false, fmt.Errorf("inspect staged artifact: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() != destination.artifact.SizeBytes {
		return false, fmt.Errorf("staged artifact must be a regular file matching its recorded size")
	}
	// 复制后验证临时文件的完整校验和，避免对源文件重复进行一次全量读取。
	if err := os.Remove(destination.temporary); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := copyFile(ctx, destination.artifact.FilePath, destination.temporary); err != nil {
		return false, fmt.Errorf("copy artifact to library temporary file: %w", err)
	}
	if err := verifyFileAgainstArtifact(ctx, destination.temporary, destination.artifact); err != nil {
		return false, fmt.Errorf("verify library temporary file: %w", err)
	}
	if err := libraryAccess.Apply(destination.temporary); err != nil {
		return false, err
	}
	return true, nil
}

func prepareImportedLibraryDirectories(
	libraryRoot, destinationDirectory string,
	libraryAccess ImportedLibraryAccess,
) (string, error) {
	root, err := filepath.Abs(filepath.Clean(libraryRoot))
	if err != nil {
		return "", fmt.Errorf("resolve library root: %w", err)
	}
	destination, err := filepath.Abs(filepath.Clean(destinationDirectory))
	if err != nil {
		return "", fmt.Errorf("resolve library directory: %w", err)
	}
	relative, err := filepath.Rel(root, destination)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("library directory must be a descendant of the configured root")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect library root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", unsupportedLibraryTree("configured library root must be a directory, not a symlink")
	}

	current := root
	libraryTreeRoot := ""
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		if libraryTreeRoot == "" {
			libraryTreeRoot = current
		}
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			if err := os.Mkdir(current, importedLibraryPathMode); err != nil {
				return "", fmt.Errorf("create imported library directory: %w", err)
			}
		} else if statErr != nil {
			return "", fmt.Errorf("inspect imported library directory: %w", statErr)
		} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", unsupportedLibraryTree("library path components must be real directories")
		}
		if err := libraryAccess.Apply(current); err != nil {
			return "", err
		}
	}
	return libraryTreeRoot, nil
}

func importTemporaryPath(final string, operationID uuid.UUID) string {
	extension := filepath.Ext(final)
	stem := strings.TrimSuffix(filepath.Base(final), extension)
	return filepath.Join(filepath.Dir(final), "."+stem+"."+operationID.String()+".import.part"+extension)
}

func verifyFileAgainstArtifact(ctx context.Context, path string, artifact domain.MediaArtifact) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("library artifact must be a regular file, not a symlink")
	}
	size, checksum, err := fileIdentity(ctx, path)
	if err != nil {
		return err
	}
	if size != artifact.SizeBytes || string(checksum) != string(artifact.ChecksumSHA256) {
		return fmt.Errorf("file identity does not match artifact")
	}
	return nil
}
