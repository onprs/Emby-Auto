package worker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type cancellingMediaReader struct {
	reader io.Reader
	cancel context.CancelCauseFunc
}

func (reader *cancellingMediaReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.cancel(ErrCancellationRequested)
	return count, err
}

func TestMediaCopyStopsWithinOneChunkAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	reader := &cancellingMediaReader{reader: bytes.NewReader(make([]byte, 8<<20)), cancel: cancel}
	copied, err := copyMediaContent(ctx, io.Discard, reader)
	if !errors.Is(err, ErrCancellationRequested) || copied > 4<<20 {
		t.Fatalf("取消后复制了 %d 字节，错误为 %v", copied, err)
	}
}

func TestMediaIdentityStopsReadingAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	source := &cancellingMediaReader{reader: bytes.NewReader(make([]byte, 1<<20)), cancel: cancel}
	reader := contextMediaReader{ctx: ctx, reader: source}
	buffer := make([]byte, 32<<10)
	if _, err := reader.Read(buffer); err != nil {
		t.Fatal(err)
	}
	if count, err := reader.Read(buffer); count != 0 || !errors.Is(err, ErrCancellationRequested) {
		t.Fatalf("取消后仍继续校验，读取 %d 字节，错误为 %v", count, err)
	}
}

func TestCancelledMediaIOLeavesNoDestination(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source.mkv"), filepath.Join(root, "destination.mkv")
	if err := os.WriteFile(source, []byte("媒体内容"), 0o640); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrCancellationRequested)
	if err := copyFile(ctx, source, destination); !errors.Is(err, ErrCancellationRequested) {
		t.Fatalf("已取消复制的错误为 %v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("已取消复制创建了目标：%v", err)
	}
	if _, _, err := fileIdentity(ctx, source); !errors.Is(err, ErrCancellationRequested) {
		t.Fatalf("已取消校验的错误为 %v", err)
	}
}

func TestMediaCommitNeverReplacesExistingDestination(t *testing.T) {
	for _, danglingSymlink := range []bool{false, true} {
		name := "普通文件"
		if danglingSymlink {
			name = "悬空符号链接"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			temporary, final := filepath.Join(root, "output.part.mkv"), filepath.Join(root, "output.mkv")
			if err := os.WriteFile(temporary, []byte("新输出"), 0o640); err != nil {
				t.Fatal(err)
			}
			if danglingSymlink {
				if err := os.Symlink(filepath.Join(root, "missing.mkv"), final); err != nil {
					t.Skipf("当前环境无法创建符号链接：%v", err)
				}
			} else if err := os.WriteFile(final, []byte("已有输出"), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := commitTemporaryFile(temporary, final); !errors.Is(err, os.ErrExist) {
				t.Fatalf("提交应报告目标已存在，实际为 %v", err)
			}
			assertFileContent(t, temporary, []byte("新输出"))
			if danglingSymlink {
				info, err := os.Lstat(final)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("已有符号链接被替换：%v", err)
				}
			} else {
				assertFileContent(t, final, []byte("已有输出"))
			}
		})
	}
}

func TestMediaCommitMovesValidatedTemporaryFile(t *testing.T) {
	root := t.TempDir()
	temporary, final := filepath.Join(root, "output.part.mkv"), filepath.Join(root, "output.mkv")
	if err := os.WriteFile(temporary, []byte("已验证输出"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := commitTemporaryFile(temporary, final); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, final, []byte("已验证输出"))
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("提交成功后临时文件仍存在：%v", err)
	}
}

func TestMediaTemporaryNamesStayWithinNameMaxAfterNamingCap(t *testing.T) {
	names, err := domain.BuildEpisodeFileNames(domain.EpisodeNamingRequest{
		SeriesTitle: strings.Repeat("很长的剧集名称", 20), Season: 1, Episode: 1,
		EpisodeTitle: strings.Repeat("很长的单集标题", 20), VideoExtension: "mkv",
	})
	if err != nil {
		t.Fatal(err)
	}
	stagingRoot := t.TempDir()
	paths, err := buildMediaOutputPaths(stagingRoot, uuid.New(), uuid.New(), names.VideoName)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Directory, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Base(paths.Final), filepath.Base(paths.Temporary)} {
		if len(name) > 255 {
			t.Fatalf("临时或正式文件名 %q = %d 字节，超过 NAME_MAX", name, len(name))
		}
	}
	file, err := os.OpenFile(paths.Temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		t.Fatalf("临时文件名无法落盘：%v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareOutputDirectoryReclaimsOnlyStaleSameStemTemporaries(t *testing.T) {
	stagingRoot := t.TempDir()
	taskID, operationID := uuid.New(), uuid.New()
	paths, err := buildMediaOutputPaths(stagingRoot, taskID, operationID, "Show - S01E01 - Panic.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Directory, 0o750); err != nil {
		t.Fatal(err)
	}
	staleSameStem := filepath.Join(paths.Directory, ".Show - S01E01 - Panic."+uuid.New().String()+".part.mkv")
	freshSameStem := filepath.Join(paths.Directory, ".Show - S01E01 - Panic."+uuid.New().String()+".part.mkv")
	staleOtherStem := filepath.Join(paths.Directory, ".Show - S01E02 - Panic."+uuid.New().String()+".part.mkv")
	unrelated := filepath.Join(paths.Directory, "Show - S01E01 - Panic.nfo")
	for path, content := range map[string]string{
		paths.Temporary: "current", staleSameStem: "stale", freshSameStem: "fresh", staleOtherStem: "other", unrelated: "metadata",
	} {
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	staleTime := time.Now().Add(-staleMediaTemporaryAge - time.Hour)
	for _, path := range []string{staleSameStem, staleOtherStem} {
		if err := os.Chtimes(path, staleTime, staleTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareOutputDirectory(paths); err != nil {
		t.Fatalf("prepareOutputDirectory() error = %v", err)
	}
	for _, removed := range []string{paths.Temporary, staleSameStem} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("预期清理的临时文件 %q 仍存在：%v", removed, err)
		}
	}
	for _, kept := range []string{freshSameStem, staleOtherStem, unrelated} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("不应清理的文件 %q 被删除：%v", kept, err)
		}
	}
}

func TestIsMediaTemporaryNameMatchesOnlyServiceTemporaryOutputs(t *testing.T) {
	operationID := uuid.New().String()
	tests := map[string]bool{
		".Show - S01E01." + operationID + ".part.mkv":        true,
		".Show - S01E01." + operationID + ".part":            true,
		".Show - S01E01." + operationID + ".import.part.mkv": false,
		".Show - S01E01.8f9b0c1d-part.part.mkv":              false,
		"Show - S01E01.mkv":                                  false,
		".Show - S01E01." + operationID + ".part.mkv.tmp":    false,
	}
	for name, want := range tests {
		if got := isMediaTemporaryName(name); got != want {
			t.Fatalf("isMediaTemporaryName(%q) = %v, want %v", name, got, want)
		}
	}
}
