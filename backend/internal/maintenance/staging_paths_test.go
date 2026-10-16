package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestStagingRootRejectsLinksAtRootAndParent(t *testing.T) {
	directory := t.TempDir()
	real := filepath.Join(directory, "real")
	if err := os.MkdirAll(filepath.Join(real, "staging"), 0o750); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(directory, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{alias, filepath.Join(alias, "staging")} {
		if _, _, err := PlanStagingOwnership(context.Background(), path, nil, 10001); err == nil {
			t.Fatal("规划跟随了暂存根目录或父目录链接")
		}
		if _, err := ApplyStagingOwnership(context.Background(), StagingBackup{Format: 1, Root: path, TargetUID: 10001}, nil, true); err == nil {
			t.Fatal("恢复跟随了暂存根目录或父目录链接")
		}
	}
}

func TestStagingPathsRejectsPhysicalOverlapWithMediaLibrary(t *testing.T) {
	directory := t.TempDir()
	paths := domain.PathSettings{StagingRoot: filepath.Join(directory, "staging"), DownloadRoot: filepath.Join(directory, "downloads"), WorkRoot: filepath.Join(directory, "work"), AnimeLibraryRoot: filepath.Join(directory, "anime"), MovieLibraryRoot: filepath.Join(directory, "movies")}
	for _, path := range []string{paths.StagingRoot, paths.DownloadRoot, paths.WorkRoot, paths.AnimeLibraryRoot, paths.MovieLibraryRoot} {
		if err := os.Mkdir(path, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := validateStagingPaths(paths); err != nil {
		t.Fatalf("独立目录未通过核查：%v", err)
	}
	alias := filepath.Join(directory, "library-alias")
	if err := os.Symlink(paths.StagingRoot, alias); err != nil {
		t.Fatal(err)
	}
	paths.AnimeLibraryRoot = alias
	if _, err := validateStagingPaths(paths); err == nil {
		t.Fatal("媒体库通过符号链接与暂存目录重叠时仍允许迁移")
	}
}
