package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestImportArtifactPairHonorsCancellationBeforeLibraryCommit(t *testing.T) {
	root := t.TempDir()
	videoPath := filepath.Join(root, "staged.mkv")
	subtitlePath := filepath.Join(root, "staged.ass")
	videoContent := []byte("cancelled-import-video")
	subtitleContent := []byte(validASSFixture)
	if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, subtitleContent, 0o640); err != nil {
		t.Fatal(err)
	}
	baseName := "Cancelled Show - S01E01 - Pilot"
	command := importCommandFixture(uuid.New(), uuid.New(), baseName, videoPath, subtitlePath, videoContent, subtitleContent)
	destinationVideo := filepath.Join(root, "library", baseName+".mkv")
	destinationSubtitle := filepath.Join(root, "library", baseName+".ass")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrCancellationRequested)

	err := importArtifactPair(
		ctx,
		command.Video,
		command.Subtitle,
		filepath.Join(root, "library"),
		destinationVideo,
		destinationSubtitle,
		uuid.New(),
		newTestImportedLibraryAccess(),
	)
	if !errors.Is(err, ErrCancellationRequested) {
		t.Fatalf("importArtifactPair() error = %v", err)
	}
	for _, path := range []string{destinationVideo, destinationSubtitle} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("cancelled import created %s: %v", path, statErr)
		}
	}
}
