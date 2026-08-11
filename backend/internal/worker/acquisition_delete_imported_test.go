package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
)

func TestAcquisitionDeleteExplicitlyRemovesImportedLibraryFilesExceptSharedTargets(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	removePath := filepath.Join(libraryRoot, "Show", "Season1", "episode.mkv")
	preservePath := filepath.Join(libraryRoot, "Show", "Season1", "shared.ass")
	for _, path := range []string{removePath, preservePath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	acquisitionID := uuid.New()
	store := &acquisitionDeleteStoreStub{ready: true, command: domain.AcquisitionDeletionCommand{
		AcquisitionID: acquisitionID,
		LibraryFiles: []domain.AcquisitionDeletionLibraryFile{
			{FilePath: removePath},
			{FilePath: preservePath, Preserve: true},
		},
	}}
	configuration := &acquisitionDeleteConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{
		AnimeLibraryRoot: libraryRoot,
	}}}}
	handler := NewAcquisitionDeleteHandler(configuration, store, func(qbittorrent.ClientOptions) (AcquisitionDeleteTorrentClient, error) {
		return &acquisitionDeleteTorrentStub{}, nil
	})
	operation := domain.Operation{
		ID: uuid.New(), ResourceType: "acquisition", ResourceID: acquisitionID,
		Payload: []byte(`{"deleteImported":true}`),
	}
	if err := handler.Handle(context.Background(), operation); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if _, err := os.Stat(removePath); !os.IsNotExist(err) {
		t.Fatalf("imported file remains: %v", err)
	}
	if _, err := os.Stat(preservePath); err != nil {
		t.Fatalf("shared imported file was removed: %v", err)
	}
	if !store.completed {
		t.Fatal("database workflow was not completed")
	}
}

func TestAcquisitionDeleteRejectsImportedFileOutsideLibraryRoots(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	outside := filepath.Join(root, "outside", "episode.mkv")
	if err := os.MkdirAll(filepath.Dir(outside), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("fixture"), 0o640); err != nil {
		t.Fatal(err)
	}
	acquisitionID := uuid.New()
	store := &acquisitionDeleteStoreStub{ready: true, command: domain.AcquisitionDeletionCommand{
		AcquisitionID: acquisitionID,
		LibraryFiles:  []domain.AcquisitionDeletionLibraryFile{{FilePath: outside}},
	}}
	configuration := &acquisitionDeleteConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{
		AnimeLibraryRoot: libraryRoot,
	}}}}
	handler := NewAcquisitionDeleteHandler(configuration, store, func(qbittorrent.ClientOptions) (AcquisitionDeleteTorrentClient, error) {
		return &acquisitionDeleteTorrentStub{}, nil
	})
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "acquisition", ResourceID: acquisitionID,
		Payload: []byte(`{"deleteImported":true}`),
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "acquisition_delete_library_path_unsafe" || failure.Retryable {
		t.Fatalf("Handle() error = %#v", err)
	}
	if store.completed {
		t.Fatal("database workflow was removed after unsafe library cleanup")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file changed: %v", err)
	}
}
