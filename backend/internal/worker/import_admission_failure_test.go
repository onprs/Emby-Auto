package worker

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

// 这些用例验证 Handler 的文件失败恢复；事务互斥由真实 PostgreSQL 集成验证。
type importPostCommitFailureAccess struct {
	*testImportedLibraryAccess
	failure error
}

func (access *importPostCommitFailureAccess) ApplyTree(context.Context, string) error {
	return access.failure
}

func TestImportArtifactPairReturnsReceiptsWithoutDeletingBeforeRecovery(t *testing.T) {
	fixture := newImportRecoveryFixture(t)
	failure := errors.New("提交后权限应用失败")
	access := &importPostCommitFailureAccess{testImportedLibraryAccess: newTestImportedLibraryAccess(), failure: failure}
	created, err := importArtifactPair(context.Background(), fixture.command.Video, fixture.command.Subtitle,
		fixture.libraryRoot, fixture.destinationVideo, fixture.destinationSubtitle, fixture.operation().ID, access)
	if !errors.Is(err, failure) {
		t.Fatalf("导入错误 = %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("失败回执数 = %d，预期 2", len(created))
	}
	fixture.assertLibraryPresent(t)
}

func TestEmbyImportRecoversCommittedPairAfterFilePhaseFailure(t *testing.T) {
	for _, test := range []struct {
		name        string
		failure     error
		claimed     bool
		unknown     bool
		wantPresent bool
		wantRetry   bool
	}{
		{name: "普通错误清理本次文件并允许重新复制", failure: errors.New("提交后权限应用失败"), wantRetry: true},
		{name: "目录错误也经安全补偿", failure: &unsupportedLibraryTreeError{reason: "目录被替换"}},
		{name: "其它采用者保护文件", failure: errors.New("提交后权限应用失败"), claimed: true, wantPresent: true, wantRetry: true},
		{name: "状态未知保留文件和恢复回执", failure: errors.New("提交后权限应用失败"), unknown: true, wantPresent: true, wantRetry: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newImportRecoveryFixture(t)
			store := &taskImportStoreStub{
				command:  fixture.command,
				recovery: domain.ImportRecoveryState{TaskState: domain.TaskImporting, ImportState: "running", DestinationClaimed: test.claimed},
			}
			if test.unknown {
				store.recoveryErr = errors.New("恢复状态暂不可读取")
			}
			handler := fixture.handler(store)
			handler.libraryAccess = &importPostCommitFailureAccess{testImportedLibraryAccess: newTestImportedLibraryAccess(), failure: test.failure}
			err := handler.Handle(context.Background(), fixture.operation())
			var failure *Failure
			if !errors.As(err, &failure) || failure.Retryable != test.wantRetry {
				t.Fatalf("处理结果 = %#v", err)
			}
			if store.recoveryCalls != 1 {
				t.Fatalf("恢复状态确认次数 = %d，预期 1", store.recoveryCalls)
			}
			if test.wantPresent {
				fixture.assertLibraryPresent(t)
			} else {
				fixture.assertLibraryAbsent(t)
			}
			assertFileContent(t, fixture.stagedVideo, []byte("recovery-video"))
			assertFileContent(t, fixture.stagedSubtitle, validASSFixture)
		})
	}
}

func TestEmbyImportStopsBeforeFilesWhenAdmissionIsNotGranted(t *testing.T) {
	fixture := newImportRecoveryFixture(t)
	store := &taskImportStoreStub{command: fixture.command, admissionOccupied: true}
	err := fixture.handler(store).Handle(context.Background(), fixture.operation())
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Retryable {
		t.Fatalf("未准入结果 = %#v，预期可重试失败", err)
	}
	fixture.assertLibraryAbsent(t)
	if store.completion.ImportID != uuid.Nil {
		t.Fatalf("未准入却提交 = %#v", store.completion)
	}
	if _, err := os.Stat(fixture.stagedVideo); err != nil {
		t.Fatal(err)
	}
}
