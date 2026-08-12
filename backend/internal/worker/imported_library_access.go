package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const importedLibraryPathMode os.FileMode = 0o777

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
	if err := os.Chown(path, access.ownerUID, -1); err != nil {
		return fmt.Errorf("set imported library owner: %w", err)
	}
	if err := os.Chmod(path, importedLibraryPathMode); err != nil {
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
