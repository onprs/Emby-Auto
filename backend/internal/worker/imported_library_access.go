package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// 入库后的媒体权限模型：应用账户（所有者）保持读写；独立运行的 Emby 服务账户
// 只需要读取与遍历。目录 0755 提供 other 的读与执行（遍历），文件 0644 提供
// other 的读；不向任何账户授予写入或文件执行位，避免入库媒体变为全局可写。
const (
	importedLibraryDirMode  os.FileMode = 0o755
	importedLibraryFileMode os.FileMode = 0o644
)

type ImportedLibraryAccess interface {
	Apply(string) error
	ApplyTree(context.Context, string) error
}

type osImportedLibraryAccess struct {
	ownerUID int
}

func NewImportedLibraryAccess(ownerUID int) (ImportedLibraryAccess, error) {
	if ownerUID <= 0 {
		return nil, fmt.Errorf("imported library owner UID must identify a non-root user")
	}
	return osImportedLibraryAccess{ownerUID: ownerUID}, nil
}

func (access osImportedLibraryAccess) Apply(path string) error {
	if runtime.GOOS != "windows" {
		if err := os.Chown(path, access.ownerUID, -1); err != nil {
			return fmt.Errorf("set imported library owner: %w", err)
		}
	}
	mode := importedLibraryFileMode
	if info, err := os.Lstat(path); err != nil {
		return fmt.Errorf("inspect imported library path: %w", err)
	} else if info.IsDir() {
		mode = importedLibraryDirMode
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("set imported library permissions: %w", err)
	}
	return nil
}

func (access osImportedLibraryAccess) ApplyTree(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("walk imported library directory: %w", walkErr)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("imported library directory contains a symlink")
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("imported library directory contains an unsupported filesystem object")
		}
		return access.Apply(path)
	})
}

type HostControlExecutor interface {
	Output(context.Context, string, string) ([]byte, error)
}

type OSHostControlExecutor struct{}

func (OSHostControlExecutor) Output(ctx context.Context, executable, command string) ([]byte, error) {
	return exec.CommandContext(ctx, executable, command).Output()
}

func ResolveConfiguredImportedLibraryAccess(
	ctx context.Context,
	ownerUID int,
	executable string,
	executor HostControlExecutor,
) (ImportedLibraryAccess, error) {
	if ownerUID != 0 {
		return NewImportedLibraryAccess(ownerUID)
	}
	return ResolveImportedLibraryAccess(ctx, executable, executor)
}

func ResolveImportedLibraryAccess(
	ctx context.Context,
	executable string,
	executor HostControlExecutor,
) (ImportedLibraryAccess, error) {
	if strings.TrimSpace(executable) == "" || executor == nil {
		return nil, errors.New("host control executable is required to resolve the Emby media owner")
	}
	output, err := executor.Output(ctx, executable, "media-owner")
	if err != nil {
		return nil, fmt.Errorf("query host Emby media owner: %w", err)
	}
	if len(output) > 32 {
		return nil, errors.New("host Emby media owner response is too large")
	}
	parsed, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 32)
	if err != nil || parsed == 0 {
		return nil, errors.New("host control returned an invalid Emby media owner UID")
	}
	return NewImportedLibraryAccess(int(parsed))
}
