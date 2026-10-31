package maintenance

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type DirectoryOwnership struct {
	Relative string `json:"relative"`
	UID      uint32 `json:"uid"`
	GID      uint32 `json:"gid"`
	Mode     uint32 `json:"mode"`
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
}

type StagingBackup struct {
	Format      int                  `json:"format"`
	Root        string               `json:"root"`
	TargetUID   int                  `json:"targetUid"`
	Directories []DirectoryOwnership `json:"directories"`
}

type StagingResult struct {
	Tasks       int  `json:"tasks"`
	Directories int  `json:"directories"`
	Changed     int  `json:"changed"`
	Applied     bool `json:"applied"`
}

func openStagingRoot(directory string) (*os.Root, error) {
	directory = filepath.Clean(directory)
	if !filepath.IsAbs(directory) || directory == "/" {
		return nil, errors.New("暂存根目录无效")
	}
	current := string(filepath.Separator)
	var before os.FileInfo
	for _, part := range strings.Split(strings.TrimPrefix(directory, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("暂存根目录及其父目录必须是实际目录")
		}
		before = info
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = root.Close()
		return nil, errors.New("暂存根目录在核查期间发生变化")
	}
	return root, nil
}

func validateStagingPaths(paths domain.PathSettings) (string, error) {
	directory := filepath.Clean(paths.StagingRoot)
	root, err := openStagingRoot(directory)
	if err != nil {
		return "", err
	}
	_ = root.Close()
	for _, other := range []string{paths.DownloadRoot, paths.WorkRoot, paths.EffectiveAnimeLibraryRoot(), paths.MovieLibraryRoot} {
		if !filepath.IsAbs(other) {
			return "", errors.New("应用目录必须是绝对路径")
		}
		physical, err := filepath.EvalSymlinks(other)
		if err != nil {
			return "", err
		}
		physical = filepath.Clean(physical)
		if directory == physical || strings.HasPrefix(directory+"/", physical+"/") || strings.HasPrefix(physical+"/", directory+"/") {
			return "", errors.New("暂存目录必须与其它应用目录分离")
		}
	}
	return directory, nil
}

func openTaskRoot(root *os.Root, task string) (*os.Root, error) {
	parsed, err := uuid.Parse(task)
	if err != nil || parsed.String() != task {
		return nil, errors.New("暂存任务标识无效")
	}
	before, err := root.Lstat(task)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("暂存任务目录包含链接或特殊文件")
	}
	taskRoot, err := root.OpenRoot(task)
	if err != nil {
		return nil, err
	}
	after, err := taskRoot.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = taskRoot.Close()
		return nil, errors.New("暂存任务目录在核查期间发生变化")
	}
	return taskRoot, nil
}

func ownership(info os.FileInfo, relative string) (DirectoryOwnership, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() {
		return DirectoryOwnership{}, errors.New("暂存目录元数据不可用")
	}
	return DirectoryOwnership{Relative: relative, UID: stat.Uid, GID: stat.Gid,
		Mode:   uint32(info.Mode() & (os.ModePerm | os.ModeSetgid | os.ModeSetuid | os.ModeSticky)),
		Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func PlanStagingOwnership(ctx context.Context, directory string, tasks []string, targetUID int) (StagingBackup, StagingResult, error) {
	backup := StagingBackup{Format: 1, Root: filepath.Clean(directory), TargetUID: targetUID}
	var result StagingResult
	if !filepath.IsAbs(directory) || backup.Root == "/" || targetUID <= 0 || uint64(targetUID) >= 1<<32-1 {
		return backup, result, errors.New("暂存目录或目标 UID 无效")
	}
	root, err := openStagingRoot(backup.Root)
	if err != nil {
		return backup, result, err
	}
	defer func() { _ = root.Close() }()
	sort.Strings(tasks)
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			return backup, result, err
		}
		taskRoot, err := openTaskRoot(root, task)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return backup, result, err
		}
		before := len(backup.Directories)
		err = fs.WalkDir(taskRoot.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("暂存任务树包含符号链接")
			}
			if !entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			relative := task
			if name != "." {
				relative += "/" + name
			}
			item, err := ownership(info, relative)
			if err != nil {
				return err
			}
			if item.UID == uint32(targetUID) {
				return nil
			}
			if item.UID != 0 {
				return errors.New("暂存目录存在预期以外的所有者")
			}
			backup.Directories = append(backup.Directories, item)
			return nil
		})
		_ = taskRoot.Close()
		if err != nil {
			return backup, result, err
		}
		if len(backup.Directories) > before {
			result.Tasks++
		}
	}
	result.Directories = len(backup.Directories)
	return backup, result, nil
}

func ApplyStagingOwnership(ctx context.Context, backup StagingBackup, tasks map[string]bool, restore bool) (StagingResult, error) {
	result := StagingResult{Directories: len(backup.Directories), Applied: true}
	if backup.Format != 1 || !filepath.IsAbs(backup.Root) || filepath.Clean(backup.Root) == "/" || backup.TargetUID <= 0 || uint64(backup.TargetUID) >= 1<<32-1 {
		return result, errors.New("暂存权限快照无效")
	}
	root, err := openStagingRoot(backup.Root)
	if err != nil {
		return result, err
	}
	defer func() { _ = root.Close() }()
	seen := make(map[string]bool)
	for _, expected := range backup.Directories {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !fs.ValidPath(expected.Relative) || seen[expected.Relative] || expected.UID != 0 {
			return result, errors.New("暂存权限快照包含无效或重复路径")
		}
		seen[expected.Relative] = true
		task, local, nested := strings.Cut(expected.Relative, "/")
		if !tasks[task] {
			if restore {
				if _, err := root.Lstat(task); os.IsNotExist(err) {
					continue
				}
			}
			return result, errors.New("暂存任务已经不属于数据库中的目标范围")
		}
		if !nested {
			local = "."
		}
		taskRoot, err := openTaskRoot(root, task)
		if restore && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return result, err
		}
		file, err := taskRoot.OpenFile(local, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
		_ = taskRoot.Close()
		if restore && os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return result, err
		}
		info, statErr := file.Stat()
		var current DirectoryOwnership
		if statErr == nil {
			current, statErr = ownership(info, expected.Relative)
		}
		if statErr != nil || current.Device != expected.Device || current.Inode != expected.Inode ||
			current.GID != expected.GID || current.Mode != expected.Mode {
			_ = file.Close()
			return result, errors.New("暂存目录身份、组或权限已变化")
		}
		wanted := backup.TargetUID
		allowed := expected.UID
		if restore {
			wanted = int(expected.UID)
			allowed = uint32(backup.TargetUID)
		}
		if current.UID != uint32(wanted) && current.UID != allowed {
			_ = file.Close()
			return result, errors.New("暂存目录所有者不符合迁移快照")
		}
		if current.UID != uint32(wanted) {
			err = file.Chown(wanted, -1)
			if err == nil {
				err = file.Chmod(os.FileMode(expected.Mode))
			}
			if err == nil {
				result.Changed++
			}
		}
		_ = file.Close()
		if err != nil {
			return result, err
		}
	}
	uniqueTasks := make(map[string]bool)
	for relative := range seen {
		task, _, _ := strings.Cut(relative, "/")
		uniqueTasks[task] = true
	}
	result.Tasks = len(uniqueTasks)
	return result, nil
}

func (scope *Scope) MigrateStaging(ctx context.Context, targetUID int, backupPath, restorePath string, apply bool, expectedTasks int) (StagingResult, error) {
	var result StagingResult
	err := scope.WithStopped(ctx, func() error {
		configuration, err := scope.Configuration.Load(ctx)
		if err != nil {
			return err
		}
		root, err := validateStagingPaths(configuration.Settings.Paths)
		if err != nil {
			return err
		}
		ids, err := scope.Queries.ListTaskIDsForStagingMigration(ctx)
		if err != nil {
			return err
		}
		taskIDs := make([]string, 0, len(ids))
		tasks := make(map[string]bool)
		for _, id := range ids {
			value := repository.UUIDFromPG(id).String()
			taskIDs = append(taskIDs, value)
			tasks[value] = true
		}
		var backup StagingBackup
		if restorePath != "" {
			if err := ReadPrivateJSON(restorePath, &backup); err != nil {
				return err
			}
		} else if apply {
			if _, err := os.Lstat(backupPath); err == nil {
				if err := ReadPrivateJSON(backupPath, &backup); err != nil {
					return err
				}
			} else {
				backup, result, err = PlanStagingOwnership(ctx, root, taskIDs, targetUID)
				if err != nil {
					return err
				}
				if expectedTasks >= 0 && result.Tasks != expectedTasks {
					return errors.New("暂存任务数量不符合预期")
				}
				if err := WritePrivateJSON(backupPath, backup); err != nil {
					return err
				}
			}
		} else {
			_, result, err = PlanStagingOwnership(ctx, root, taskIDs, targetUID)
			if err != nil {
				return err
			}
			if expectedTasks >= 0 && result.Tasks != expectedTasks {
				return errors.New("暂存任务数量不符合预期")
			}
			return nil
		}
		if filepath.Clean(backup.Root) != root || (targetUID > 0 && backup.TargetUID != targetUID) {
			return errors.New("暂存快照与当前配置不一致")
		}
		selectedTasks := make(map[string]bool)
		for _, directory := range backup.Directories {
			task, _, _ := strings.Cut(directory.Relative, "/")
			selectedTasks[task] = true
		}
		if expectedTasks >= 0 && len(selectedTasks) != expectedTasks {
			return errors.New("快照任务数量不符合预期")
		}
		if !apply {
			result.Tasks = len(selectedTasks)
			result.Directories = len(backup.Directories)
			return nil
		}
		if os.Geteuid() != 0 {
			return errors.New("暂存所有权迁移需要 root")
		}
		result, err = ApplyStagingOwnership(ctx, backup, tasks, restorePath != "")
		return err
	})
	return result, err
}
