//go:build integration

package service

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestArtifactPreviewSurvivesSuccessfulImportAndStagingCleanupIntegration(t *testing.T) {
	fixture := newRecoveryFixture(t)
	taskID := createReviewableTask(t, fixture)
	queries := db.New(fixture.pool)
	workflow := NewTaskWorkflow(queries, fixture.transactor, fixture.scheduler)
	contentService := NewArtifactContentService(queries)
	ctx := context.Background()
	root := t.TempDir()

	type fixtureArtifact struct {
		id          uuid.UUID
		kind        string
		staged      string
		destination string
		content     string
	}
	artifacts := []fixtureArtifact{
		{kind: "video", staged: filepath.Join(root, "staged.mkv"), destination: filepath.Join(root, "imported.mkv"), content: "视频入库内容"},
		{kind: "subtitle", staged: filepath.Join(root, "staged.ass"), destination: filepath.Join(root, "imported.ass"), content: "字幕入库内容"},
	}
	for index := range artifacts {
		artifact := &artifacts[index]
		if err := fixture.pool.QueryRow(ctx, `
UPDATE media_artifacts SET file_path = $3, size_bytes = $4
WHERE task_id = $1 AND kind = $2 RETURNING id
`, taskID, artifact.kind, artifact.staged, len(artifact.content)).Scan(&artifact.id); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(artifact.staged, []byte(artifact.content), 0o640); err != nil {
			t.Fatal(err)
		}
		assertArtifactContent(t, contentService, taskID, artifact.id, artifact.content)
	}

	task, err := workflow.ReviewTask(ctx, domain.ReviewTask{
		TaskID: taskID, ExpectedVersion: 1, Decision: domain.TaskApproved,
		IdempotencyKey: "preview-after-import", ActorUserID: fixture.actorID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.BeginImport(ctx, taskID, task.Import.ID); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		if err := os.WriteFile(artifact.destination, []byte(artifact.content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := workflow.CompleteImport(ctx, domain.ImportCompletion{
		TaskID: taskID, ImportID: task.Import.ID, OperationID: task.Operations[0].ID,
		DestinationVideoPath: artifacts[0].destination, DestinationSubtitlePath: artifacts[1].destination,
	}); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		if err := os.Remove(artifact.staged); err != nil {
			t.Fatal(err)
		}
		assertArtifactContent(t, contentService, taskID, artifact.id, artifact.content)
	}
	if _, err := contentService.OpenArtifact(ctx, uuid.New(), artifacts[0].id); err != domain.ErrNotFound {
		t.Fatalf("跨任务预览应不可访问，实际错误为 %v", err)
	}
}

func assertArtifactContent(t *testing.T, contentService *ArtifactContentService, taskID, artifactID uuid.UUID, want string) {
	t.Helper()
	content, err := contentService.OpenArtifact(context.Background(), taskID, artifactID)
	if err != nil {
		t.Fatalf("打开媒体预览失败：%v", err)
	}
	defer func() { _ = content.Reader.Close() }()
	actual, err := io.ReadAll(content.Reader)
	if err != nil || string(actual) != want || content.SizeBytes != int64(len(want)) {
		t.Fatalf("媒体预览内容 = %q，大小 = %d，错误 = %v", actual, content.SizeBytes, err)
	}
}
