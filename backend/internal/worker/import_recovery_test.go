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

type importRecoveryFixture struct {
	libraryRoot         string
	taskID              uuid.UUID
	importID            uuid.UUID
	command             domain.ImportCommand
	destinationVideo    string
	destinationSubtitle string
	stagedVideo         string
	stagedSubtitle      string
}

func newImportRecoveryFixture(t *testing.T) importRecoveryFixture {
	t.Helper()
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	stagingRoot := filepath.Join(root, "staging")
	if err := os.MkdirAll(libraryRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stagingRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	taskID, importID := uuid.New(), uuid.New()
	baseName := "恢复验证 - S02E01 - Pilot"
	videoContent := []byte("recovery-video")
	videoPath := filepath.Join(stagingRoot, baseName+".mkv")
	subtitlePath := filepath.Join(stagingRoot, baseName+".ass")
	if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, validASSFixture, 0o640); err != nil {
		t.Fatal(err)
	}
	command := importCommandFixture(taskID, importID, baseName, videoPath, subtitlePath, videoContent, validASSFixture)
	// importCommandFixture 的剧集元数据是 Canonical Show / Season 2。
	destinationDirectory := filepath.Join(libraryRoot, "Canonical Show", "Season2")
	return importRecoveryFixture{
		libraryRoot:         libraryRoot,
		taskID:              taskID,
		importID:            importID,
		command:             command,
		destinationVideo:    filepath.Join(destinationDirectory, baseName+".mkv"),
		destinationSubtitle: filepath.Join(destinationDirectory, baseName+".ass"),
		stagedVideo:         videoPath,
		stagedSubtitle:      subtitlePath,
	}
}

func (fixture importRecoveryFixture) handler(store *taskImportStoreStub) *EmbyImportHandler {
	return NewEmbyImportHandler(
		&downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{AnimeLibraryRoot: fixture.libraryRoot}}}},
		store,
		newTestImportedLibraryAccess(),
	)
}

func (fixture importRecoveryFixture) operation() domain.Operation {
	return domain.Operation{
		ID: uuid.New(), ResourceType: "episode_task", ResourceID: fixture.taskID,
		Payload: []byte(`{"importId":"` + fixture.importID.String() + `"}`),
	}
}

func (fixture importRecoveryFixture) assertLibraryAbsent(t *testing.T) {
	t.Helper()
	for _, path := range []string{fixture.destinationVideo, fixture.destinationSubtitle} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("库内残留 %q: %v", path, err)
		}
	}
}

func (fixture importRecoveryFixture) assertLibraryPresent(t *testing.T) {
	t.Helper()
	assertFileContent(t, fixture.destinationVideo, []byte("recovery-video"))
	assertFileContent(t, fixture.destinationSubtitle, validASSFixture)
}

func TestEmbyImportCompensatesAfterPermanentStoreFailure(t *testing.T) {
	fixture := newImportRecoveryFixture(t)
	store := &taskImportStoreStub{
		command: fixture.command,
		completeErr: &domain.MediaWorkflowError{
			Code: "task_import_state_conflict", Message: "the task import is not running", Retryable: false,
		},
		recovery: domain.ImportRecoveryState{TaskState: domain.TaskImporting, ImportState: "failed"},
	}
	err := fixture.handler(store).Handle(context.Background(), fixture.operation())
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "task_import_state_conflict" || failure.Retryable {
		t.Fatalf("Handle() error = %#v, want permanent task_import_state_conflict", err)
	}
	if store.recoveryCalls != 1 {
		t.Fatalf("recovery observations = %d, want 1", store.recoveryCalls)
	}
	fixture.assertLibraryAbsent(t)
	assertFileContent(t, fixture.stagedVideo, []byte("recovery-video"))
	assertFileContent(t, fixture.stagedSubtitle, validASSFixture)
}

func TestEmbyImportCompensatesAfterCancellation(t *testing.T) {
	fixture := newImportRecoveryFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	store := &taskImportStoreStub{
		command:     fixture.command,
		completeErr: context.Canceled,
		onComplete:  func() { cancel(errors.New("user cancelled the task")) },
		recovery:    domain.ImportRecoveryState{TaskState: domain.TaskCancelled, ImportState: "failed"},
	}
	err := fixture.handler(store).Handle(ctx, fixture.operation())
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "media_storage_unavailable" || !failure.Retryable {
		t.Fatalf("Handle() error = %#v, want retryable media_storage_unavailable", err)
	}
	if store.recoveryCalls != 1 {
		t.Fatalf("recovery observations = %d, want 1", store.recoveryCalls)
	}
	fixture.assertLibraryAbsent(t)
}

func TestEmbyImportKeepsFilesForRetryableStoreFailureThenAdoptsOnRetry(t *testing.T) {
	fixture := newImportRecoveryFixture(t)
	store := &taskImportStoreStub{
		command:     fixture.command,
		completeErr: errors.New("db connection lost"),
		recovery:    domain.ImportRecoveryState{TaskState: domain.TaskImporting, ImportState: "running"},
	}
	err := fixture.handler(store).Handle(context.Background(), fixture.operation())
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "media_storage_unavailable" || !failure.Retryable {
		t.Fatalf("Handle() error = %#v, want retryable media_storage_unavailable", err)
	}
	fixture.assertLibraryPresent(t)

	retryStore := &taskImportStoreStub{command: fixture.command}
	if err := fixture.handler(retryStore).Handle(context.Background(), fixture.operation()); err != nil {
		t.Fatalf("Handle(retry) error = %v", err)
	}
	fixture.assertLibraryPresent(t)
	if retryStore.completion.ImportID != fixture.importID {
		t.Fatalf("retry completion = %#v", retryStore.completion)
	}
}

func TestEmbyImportKeepsFilesWhenRecoveryStateIsUnknown(t *testing.T) {
	fixture := newImportRecoveryFixture(t)
	store := &taskImportStoreStub{
		command:     fixture.command,
		completeErr: errors.New("db connection lost"),
		recoveryErr: errors.New("database still unavailable"),
	}
	err := fixture.handler(store).Handle(context.Background(), fixture.operation())
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Retryable {
		t.Fatalf("Handle() error = %#v, want retryable failure", err)
	}
	fixture.assertLibraryPresent(t)
}

func TestEmbyImportReturnsSuccessWhenCommitActuallySucceeded(t *testing.T) {
	fixture := newImportRecoveryFixture(t)
	store := &taskImportStoreStub{
		command:     fixture.command,
		completeErr: errors.New("connection reset after commit"),
		recovery: domain.ImportRecoveryState{
			TaskState: domain.TaskImported, ImportState: "succeeded", Succeeded: true, DestinationClaimed: true,
		},
	}
	if err := fixture.handler(store).Handle(context.Background(), fixture.operation()); err != nil {
		t.Fatalf("Handle() error = %v, want nil after confirmed commit", err)
	}
	fixture.assertLibraryPresent(t)
}

func TestEmbyImportCompensationSkipsClaimedDestination(t *testing.T) {
	fixture := newImportRecoveryFixture(t)
	store := &taskImportStoreStub{
		command: fixture.command,
		completeErr: &domain.MediaWorkflowError{
			Code: "task_import_state_conflict", Message: "the task import is not running", Retryable: false,
		},
		recovery: domain.ImportRecoveryState{
			TaskState: domain.TaskImporting, ImportState: "running", DestinationClaimed: true,
		},
	}
	err := fixture.handler(store).Handle(context.Background(), fixture.operation())
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "task_import_state_conflict" {
		t.Fatalf("Handle() error = %#v", err)
	}
	fixture.assertLibraryPresent(t)
}

func TestEmbyImportCompensationPreservesRewrittenDestination(t *testing.T) {
	fixture := newImportRecoveryFixture(t)
	store := &taskImportStoreStub{
		command: fixture.command,
		completeErr: &domain.MediaWorkflowError{
			Code: "task_import_state_conflict", Message: "the task import is not running", Retryable: false,
		},
		recovery: domain.ImportRecoveryState{TaskState: domain.TaskImporting, ImportState: "failed"},
		onRecovery: func(domain.ImportCompletion) error {
			// 原地改写目标并恢复原修改时间：inode、大小与 mtime 都相同，
			// 使补偿必须依赖内容校验和才能避免误删替换内容。
			original, err := os.Stat(fixture.destinationVideo)
			if err != nil {
				return err
			}
			if err := os.WriteFile(fixture.destinationVideo, []byte("replaced-video"), 0o640); err != nil {
				return err
			}
			return os.Chtimes(fixture.destinationVideo, original.ModTime(), original.ModTime())
		},
	}
	if err := fixture.handler(store).Handle(context.Background(), fixture.operation()); err == nil {
		t.Fatal("Handle() error = nil, want permanent store failure")
	}
	assertFileContent(t, fixture.destinationVideo, []byte("replaced-video"))
	if _, err := os.Stat(fixture.destinationSubtitle); !os.IsNotExist(err) {
		t.Fatalf("本次创建的字幕未被回滚: %v", err)
	}
}

func TestEmbyImportCompensationPreservesReplacedDestination(t *testing.T) {
	fixture := newImportRecoveryFixture(t)
	store := &taskImportStoreStub{
		command: fixture.command,
		completeErr: &domain.MediaWorkflowError{
			Code: "task_import_state_conflict", Message: "the task import is not running", Retryable: false,
		},
		recovery: domain.ImportRecoveryState{TaskState: domain.TaskImporting, ImportState: "failed"},
		onRecovery: func(domain.ImportCompletion) error {
			// 提交后目标被另一个写入者替换为新 inode，补偿不得删除替换后的文件。
			if err := os.Remove(fixture.destinationVideo); err != nil {
				return err
			}
			return os.WriteFile(fixture.destinationVideo, []byte("replaced-video"), 0o640)
		},
	}
	if err := fixture.handler(store).Handle(context.Background(), fixture.operation()); err == nil {
		t.Fatal("Handle() error = nil, want permanent store failure")
	}
	assertFileContent(t, fixture.destinationVideo, []byte("replaced-video"))
	if _, err := os.Stat(fixture.destinationSubtitle); !os.IsNotExist(err) {
		t.Fatalf("本次创建的字幕未被回滚: %v", err)
	}
}

func TestEmbyImportCompensationPreservesPreExistingDestination(t *testing.T) {
	fixture := newImportRecoveryFixture(t)
	if err := os.MkdirAll(filepath.Dir(fixture.destinationVideo), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.destinationVideo, []byte("recovery-video"), 0o640); err != nil {
		t.Fatal(err)
	}
	store := &taskImportStoreStub{
		command: fixture.command,
		completeErr: &domain.MediaWorkflowError{
			Code: "task_import_state_conflict", Message: "the task import is not running", Retryable: false,
		},
		recovery: domain.ImportRecoveryState{TaskState: domain.TaskImporting, ImportState: "failed"},
	}
	if err := fixture.handler(store).Handle(context.Background(), fixture.operation()); err == nil {
		t.Fatal("Handle() error = nil, want permanent store failure")
	}
	assertFileContent(t, fixture.destinationVideo, []byte("recovery-video"))
	if _, err := os.Stat(fixture.destinationSubtitle); !os.IsNotExist(err) {
		t.Fatalf("本次创建的字幕未被回滚: %v", err)
	}
}
