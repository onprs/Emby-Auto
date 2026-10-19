package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

type importAccessHook struct {
	*testImportedLibraryAccess
	beforeApply func(string) error
}

func (access *importAccessHook) Apply(path string) error {
	if access.beforeApply != nil {
		if err := access.beforeApply(path); err != nil {
			return err
		}
	}
	return access.testImportedLibraryAccess.Apply(path)
}

func TestImportPreservesDestinationCreatedWhilePreparingPair(t *testing.T) {
	for _, conflictKind := range []string{"video", "subtitle"} {
		t.Run(conflictKind, func(t *testing.T) {
			root := t.TempDir()
			libraryRoot := filepath.Join(root, "library")
			if err := os.Mkdir(libraryRoot, 0o750); err != nil {
				t.Fatal(err)
			}
			videoContent := []byte("validated import video")
			videoPath, subtitlePath := filepath.Join(root, "source.mkv"), filepath.Join(root, "source.ass")
			if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(subtitlePath, validASSFixture, 0o640); err != nil {
				t.Fatal(err)
			}
			baseName := "并发入库 - S01E01"
			command := importCommandFixture(uuid.New(), uuid.New(), baseName, videoPath, subtitlePath, videoContent, validASSFixture)
			operationID := uuid.New()
			destinationVideo := filepath.Join(libraryRoot, "并发入库", "Season1", baseName+".mkv")
			destinationSubtitle := filepath.Join(libraryRoot, "并发入库", "Season1", baseName+".ass")
			conflictingPath := destinationVideo
			if conflictKind == "subtitle" {
				conflictingPath = destinationSubtitle
			}
			otherContent := []byte("另一个任务提交的内容")
			access := &importAccessHook{testImportedLibraryAccess: newTestImportedLibraryAccess()}
			access.beforeApply = func(path string) error {
				if path == importTemporaryPath(destinationSubtitle, operationID) {
					return os.WriteFile(conflictingPath, otherContent, 0o640)
				}
				return nil
			}
			err := importArtifactPair(context.Background(), command.Video, command.Subtitle, libraryRoot, destinationVideo, destinationSubtitle, operationID, access)
			var conflict *destinationConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("并发目标冲突应返回冲突错误，实际为 %v", err)
			}
			assertFileContent(t, conflictingPath, otherContent)
			for _, final := range []string{destinationVideo, destinationSubtitle} {
				if _, err := os.Stat(importTemporaryPath(final, operationID)); !os.IsNotExist(err) {
					t.Fatalf("入库失败后残留临时文件：%v", err)
				}
			}
		})
	}
}

func TestImportRejectsCorruptedStagedContentBeforeCommit(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	if err := os.Mkdir(libraryRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	videoContent := []byte("原始视频内容")
	videoPath, subtitlePath := filepath.Join(root, "source.mkv"), filepath.Join(root, "source.ass")
	if err := os.WriteFile(videoPath, []byte("篡改视频内容"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, validASSFixture, 0o640); err != nil {
		t.Fatal(err)
	}
	baseName := "完整性检查 - S01E01"
	command := importCommandFixture(uuid.New(), uuid.New(), baseName, videoPath, subtitlePath, videoContent, validASSFixture)
	destinationVideo := filepath.Join(libraryRoot, "完整性检查", "Season1", baseName+".mkv")
	destinationSubtitle := filepath.Join(libraryRoot, "完整性检查", "Season1", baseName+".ass")
	operationID := uuid.New()
	if err := importArtifactPair(context.Background(), command.Video, command.Subtitle, libraryRoot, destinationVideo, destinationSubtitle, operationID, newTestImportedLibraryAccess()); err == nil {
		t.Fatal("损坏的暂存文件被成功入库")
	}
	for _, final := range []string{destinationVideo, destinationSubtitle} {
		for _, path := range []string{final, importTemporaryPath(final, operationID)} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("完整性检查失败后仍有输出 %q：%v", path, err)
			}
		}
	}
}

func BenchmarkImportArtifactPair(b *testing.B) {
	benchmarkImportArtifactPair(b, 32<<20)
}

func BenchmarkImportArtifactPairLarge(b *testing.B) {
	benchmarkImportArtifactPair(b, 256<<20)
}

func benchmarkImportArtifactPair(b *testing.B, videoSize int) {
	b.Helper()
	root := b.TempDir()
	libraryRoot := filepath.Join(root, "library")
	if err := os.Mkdir(libraryRoot, 0o750); err != nil {
		b.Fatal(err)
	}
	videoContent := make([]byte, videoSize)
	for index := range videoContent {
		videoContent[index] = byte(index % 251)
	}
	videoPath, subtitlePath := filepath.Join(root, "source.mkv"), filepath.Join(root, "source.ass")
	if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, validASSFixture, 0o640); err != nil {
		b.Fatal(err)
	}
	baseName := "入库性能 - S01E01"
	command := importCommandFixture(uuid.New(), uuid.New(), baseName, videoPath, subtitlePath, videoContent, validASSFixture)
	destinationDirectory := filepath.Join(libraryRoot, "入库性能", "Season1")
	destinationVideo := filepath.Join(destinationDirectory, baseName+".mkv")
	destinationSubtitle := filepath.Join(destinationDirectory, baseName+".ass")
	b.SetBytes(int64(len(videoContent) + len(validASSFixture)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := importArtifactPair(context.Background(), command.Video, command.Subtitle, libraryRoot, destinationVideo, destinationSubtitle, uuid.New(), newTestImportedLibraryAccess()); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if err := os.RemoveAll(filepath.Join(libraryRoot, "入库性能")); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}
