package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
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
			_, err := importArtifactPair(context.Background(), command.Video, command.Subtitle, libraryRoot, destinationVideo, destinationSubtitle, operationID, access)
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
	if _, err := importArtifactPair(context.Background(), command.Video, command.Subtitle, libraryRoot, destinationVideo, destinationSubtitle, operationID, newTestImportedLibraryAccess()); err == nil {
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

func TestImportRejectsUnsupportedLibraryTreeBeforeCommit(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	stagingRoot := filepath.Join(root, "staging")
	destination := filepath.Join(libraryRoot, "Show", "Season1")
	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stagingRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	// 已有媒体库中常见由其它工具维护的符号链接，属于不支持的目录布局。
	if err := os.Symlink(filepath.Join(root, "elsewhere.mkv"), filepath.Join(destination, "extra.mkv")); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	baseName := "不支持布局 - S01E01 - Pilot"
	videoContent := []byte("tree-video")
	videoPath := filepath.Join(stagingRoot, baseName+".mkv")
	subtitlePath := filepath.Join(stagingRoot, baseName+".ass")
	if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, validASSFixture, 0o640); err != nil {
		t.Fatal(err)
	}
	command := importCommandFixture(uuid.New(), uuid.New(), baseName, videoPath, subtitlePath, videoContent, validASSFixture)
	destinationVideo := filepath.Join(destination, baseName+".mkv")
	destinationSubtitle := filepath.Join(destination, baseName+".ass")
	operationID := uuid.New()
	_, err := importArtifactPair(context.Background(), command.Video, command.Subtitle, libraryRoot, destinationVideo, destinationSubtitle, operationID, newTestImportedLibraryAccess())
	var treeErr *unsupportedLibraryTreeError
	if !errors.As(err, &treeErr) {
		t.Fatalf("importArtifactPair() error = %v, want unsupported library tree", err)
	}
	for _, path := range []string{destinationVideo, destinationSubtitle, importTemporaryPath(destinationVideo, operationID), importTemporaryPath(destinationSubtitle, operationID)} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("unsupported tree created %q: %v", path, statErr)
		}
	}
	assertFileContent(t, videoPath, videoContent)
	assertFileContent(t, subtitlePath, validASSFixture)
}

func TestEmbyImportUnsupportedLibraryTreeIsPermanentFailure(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	stagingRoot := filepath.Join(root, "staging")
	destination := filepath.Join(libraryRoot, "Canonical Show", "Season2")
	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stagingRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside.mkv"), filepath.Join(destination, "extra.mkv")); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	taskID, importID := uuid.New(), uuid.New()
	baseName := "Canonical Show - S02E01 - Episode"
	videoContent := []byte("tree-video")
	videoPath := filepath.Join(stagingRoot, baseName+".mkv")
	subtitlePath := filepath.Join(stagingRoot, baseName+".ass")
	if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, validASSFixture, 0o640); err != nil {
		t.Fatal(err)
	}
	store := &taskImportStoreStub{command: importCommandFixture(taskID, importID, baseName, videoPath, subtitlePath, videoContent, validASSFixture)}
	handler := NewEmbyImportHandler(
		&downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{AnimeLibraryRoot: libraryRoot}}}},
		store,
		newTestImportedLibraryAccess(),
	)
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID, Payload: []byte(`{"importId":"` + importID.String() + `"}`),
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "library_tree_unsupported" || failure.Retryable {
		t.Fatalf("Handle() error = %#v, want permanent library_tree_unsupported", err)
	}
	if store.completion.ImportID != uuid.Nil {
		t.Fatalf("unsupported tree completed import: %#v", store.completion)
	}
	if matches, _ := filepath.Glob(filepath.Join(destination, "*")); len(matches) != 1 {
		t.Fatalf("import left extra files in library: %v", matches)
	}
}

func TestImportRollsBackCreatedFilesWhenPostCommitAccessFails(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	destination := filepath.Join(libraryRoot, "Show", "Season1")
	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatal(err)
	}
	baseName := "回滚验证 - S01E01 - Pilot"
	videoContent := []byte("rollback-video")
	videoPath := filepath.Join(root, baseName+".mkv")
	subtitlePath := filepath.Join(root, baseName+".ass")
	if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, validASSFixture, 0o640); err != nil {
		t.Fatal(err)
	}
	command := importCommandFixture(uuid.New(), uuid.New(), baseName, videoPath, subtitlePath, videoContent, validASSFixture)
	destinationVideo := filepath.Join(destination, baseName+".mkv")
	destinationSubtitle := filepath.Join(destination, baseName+".ass")
	operationID := uuid.New()
	access := &importAccessHook{testImportedLibraryAccess: newTestImportedLibraryAccess()}
	access.beforeApply = func(path string) error {
		if path == destinationVideo {
			return errors.New("chown denied after commit")
		}
		return nil
	}
	_, err := importArtifactPair(context.Background(), command.Video, command.Subtitle, libraryRoot, destinationVideo, destinationSubtitle, operationID, access)
	if err == nil {
		t.Fatal("importArtifactPair() error = nil, want post-commit failure")
	}
	for _, path := range []string{destinationVideo, destinationSubtitle, importTemporaryPath(destinationVideo, operationID), importTemporaryPath(destinationSubtitle, operationID)} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("failed import left %q: %v", path, statErr)
		}
	}
	assertFileContent(t, videoPath, videoContent)
	assertFileContent(t, subtitlePath, validASSFixture)

	// 回滚后同一对文件应可以重新入库。
	if _, err := importArtifactPair(context.Background(), command.Video, command.Subtitle, libraryRoot, destinationVideo, destinationSubtitle, uuid.New(), newTestImportedLibraryAccess()); err != nil {
		t.Fatalf("retry after rollback error = %v", err)
	}
	assertFileContent(t, destinationVideo, videoContent)
	assertFileContent(t, destinationSubtitle, validASSFixture)
}

func TestImportRollsBackOnlyFilesCreatedByThisOperation(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	destination := filepath.Join(libraryRoot, "Show", "Season1")
	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatal(err)
	}
	baseName := "保留既有 - S01E01 - Pilot"
	videoContent := []byte("preexisting-video")
	videoPath := filepath.Join(root, baseName+".mkv")
	subtitlePath := filepath.Join(root, baseName+".ass")
	if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, validASSFixture, 0o640); err != nil {
		t.Fatal(err)
	}
	destinationVideo := filepath.Join(destination, baseName+".mkv")
	destinationSubtitle := filepath.Join(destination, baseName+".ass")
	if err := os.WriteFile(destinationVideo, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	command := importCommandFixture(uuid.New(), uuid.New(), baseName, videoPath, subtitlePath, videoContent, validASSFixture)
	access := &importAccessHook{testImportedLibraryAccess: newTestImportedLibraryAccess()}
	access.beforeApply = func(path string) error {
		if path == destinationSubtitle {
			return errors.New("chown denied for new subtitle")
		}
		return nil
	}
	if _, err := importArtifactPair(context.Background(), command.Video, command.Subtitle, libraryRoot, destinationVideo, destinationSubtitle, uuid.New(), access); err == nil {
		t.Fatal("importArtifactPair() error = nil, want failure")
	}
	assertFileContent(t, destinationVideo, videoContent)
	if _, err := os.Stat(destinationSubtitle); !os.IsNotExist(err) {
		t.Fatalf("rollback removed or kept wrong subtitle: %v", err)
	}
}

func TestImportCancellationAfterCommitRollsBackCreatedFiles(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	destination := filepath.Join(libraryRoot, "Show", "Season1")
	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatal(err)
	}
	baseName := "取消回滚 - S01E01 - Pilot"
	videoContent := []byte("cancel-video")
	videoPath := filepath.Join(root, baseName+".mkv")
	subtitlePath := filepath.Join(root, baseName+".ass")
	if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, validASSFixture, 0o640); err != nil {
		t.Fatal(err)
	}
	command := importCommandFixture(uuid.New(), uuid.New(), baseName, videoPath, subtitlePath, videoContent, validASSFixture)
	destinationVideo := filepath.Join(destination, baseName+".mkv")
	destinationSubtitle := filepath.Join(destination, baseName+".ass")
	operationID := uuid.New()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	cancellation := errors.New("test cancellation")
	access := &importAccessHook{testImportedLibraryAccess: newTestImportedLibraryAccess()}
	access.beforeApply = func(path string) error {
		// 提交后的权限应用阶段收到取消，已提交的文件必须回滚。
		if path == destinationVideo {
			cancel(cancellation)
		}
		return nil
	}
	_, err := importArtifactPair(ctx, command.Video, command.Subtitle, libraryRoot, destinationVideo, destinationSubtitle, operationID, access)
	if !errors.Is(err, cancellation) {
		t.Fatalf("importArtifactPair() error = %v, want cancellation", err)
	}
	for _, path := range []string{destinationVideo, destinationSubtitle, importTemporaryPath(destinationVideo, operationID), importTemporaryPath(destinationSubtitle, operationID)} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("cancelled import left %q: %v", path, statErr)
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
		if _, err := importArtifactPair(context.Background(), command.Video, command.Subtitle, libraryRoot, destinationVideo, destinationSubtitle, uuid.New(), newTestImportedLibraryAccess()); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		if err := os.RemoveAll(filepath.Join(libraryRoot, "入库性能")); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}
