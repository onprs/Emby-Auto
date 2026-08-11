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
	"github.com/riverqueue/river"
)

type acquisitionDeleteStoreStub struct {
	ready      bool
	command    domain.AcquisitionDeletionCommand
	completion domain.AcquisitionDeletionResult
	completed  bool
}

func (stub *acquisitionDeleteStoreStub) DeletionReady(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return stub.ready, nil
}
func (stub *acquisitionDeleteStoreStub) LoadDeletionCommand(context.Context, uuid.UUID) (domain.AcquisitionDeletionCommand, error) {
	return stub.command, nil
}
func (stub *acquisitionDeleteStoreStub) CompleteDeletion(_ context.Context, acquisitionID, _ uuid.UUID) (domain.AcquisitionDeletionResult, error) {
	stub.completed = true
	stub.completion.AcquisitionID = acquisitionID
	return stub.completion, nil
}

type acquisitionDeleteConfigurationStub struct {
	configuration domain.Configuration
	secret        string
	loaded        bool
}

func (stub *acquisitionDeleteConfigurationStub) Load(context.Context) (domain.Configuration, error) {
	stub.loaded = true
	return stub.configuration, nil
}
func (stub *acquisitionDeleteConfigurationStub) ResolveSecret(context.Context, string) (string, error) {
	return stub.secret, nil
}

type acquisitionDeleteTorrentStub struct {
	hashes      []string
	deleteFiles []bool
}

func (stub *acquisitionDeleteTorrentStub) Login(context.Context) error { return nil }
func (stub *acquisitionDeleteTorrentStub) DeleteTorrent(_ context.Context, hash string, deleteFiles bool) error {
	stub.hashes = append(stub.hashes, hash)
	stub.deleteFiles = append(stub.deleteFiles, deleteFiles)
	return nil
}

func TestAcquisitionDeleteWaitsForRelatedOperations(t *testing.T) {
	store := &acquisitionDeleteStoreStub{}
	configuration := &acquisitionDeleteConfigurationStub{}
	handler := NewAcquisitionDeleteHandler(configuration, store, func(qbittorrent.ClientOptions) (AcquisitionDeleteTorrentClient, error) {
		return &acquisitionDeleteTorrentStub{}, nil
	})
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "acquisition", ResourceID: uuid.New()})
	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) || snooze.Duration != acquisitionDeletionGuardInterval {
		t.Fatalf("Handle() error = %v, want deletion guard snooze", err)
	}
	if configuration.loaded || store.completed {
		t.Fatalf("cleanup ran before related operations exited: loaded=%t completed=%t", configuration.loaded, store.completed)
	}
}

func TestAcquisitionDeleteRemovesSourceAndTemporaryFilesButKeepsLibrary(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "downloads")
	stagingRoot := filepath.Join(root, "staging")
	workRoot := filepath.Join(root, "work")
	libraryRoot := filepath.Join(root, "library")
	taskID := uuid.New()
	acquisitionID := uuid.New()
	downloadPath := filepath.Join(downloadRoot, "source")
	artifactPath := filepath.Join(stagingRoot, taskID.String(), "Show", "Season1", "episode.mkv")
	workPath := filepath.Join(workRoot, taskID.String(), "ffmpeg.part")
	libraryPath := filepath.Join(libraryRoot, "Show", "Season1", "episode.mkv")
	for _, path := range []string{filepath.Join(downloadPath, "source.mkv"), artifactPath, workPath, libraryPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	store := &acquisitionDeleteStoreStub{ready: true, command: domain.AcquisitionDeletionCommand{
		AcquisitionID: acquisitionID,
		TaskIDs:       []uuid.UUID{taskID},
		ArtifactPaths: []string{artifactPath},
		Downloads: []domain.AcquisitionDeletionDownload{{
			ID: uuid.New(), TorrentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SavePath: downloadPath,
		}},
	}}
	configuration := &acquisitionDeleteConfigurationStub{secret: "password", configuration: domain.Configuration{Settings: domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://qb.test", Username: "user"},
		Paths:       domain.PathSettings{DownloadRoot: downloadRoot, StagingRoot: stagingRoot, WorkRoot: workRoot, AnimeLibraryRoot: libraryRoot},
	}}}
	torrent := &acquisitionDeleteTorrentStub{}
	handler := NewAcquisitionDeleteHandler(configuration, store, func(qbittorrent.ClientOptions) (AcquisitionDeleteTorrentClient, error) { return torrent, nil })
	operationID := uuid.New()
	if err := handler.Handle(context.Background(), domain.Operation{ID: operationID, ResourceType: "acquisition", ResourceID: acquisitionID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	for _, path := range []string{downloadPath, filepath.Join(stagingRoot, taskID.String()), filepath.Join(workRoot, taskID.String())} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("temporary path %q still exists: %v", path, err)
		}
	}
	if _, err := os.Stat(libraryPath); err != nil {
		t.Fatalf("imported library file was removed: %v", err)
	}
	if !store.completed || len(torrent.hashes) != 1 || torrent.deleteFiles[0] {
		t.Fatalf("cleanup result = completed %t hashes %#v deleteFiles %#v", store.completed, torrent.hashes, torrent.deleteFiles)
	}
}

func TestAcquisitionDeletePreservesSharedTorrentAndDownloadPath(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "downloads")
	sharedPath := filepath.Join(downloadRoot, "shared-source")
	if err := os.MkdirAll(sharedPath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedPath, "episode.mkv"), []byte("shared"), 0o640); err != nil {
		t.Fatal(err)
	}
	store := &acquisitionDeleteStoreStub{ready: true, command: domain.AcquisitionDeletionCommand{
		AcquisitionID: uuid.New(),
		Downloads: []domain.AcquisitionDeletionDownload{{
			ID: uuid.New(), TorrentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SavePath: sharedPath,
			PreserveTorrent: true, PreservePath: true,
		}},
	}}
	configuration := &acquisitionDeleteConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{DownloadRoot: downloadRoot}}}}
	torrent := &acquisitionDeleteTorrentStub{}
	handler := NewAcquisitionDeleteHandler(configuration, store, func(qbittorrent.ClientOptions) (AcquisitionDeleteTorrentClient, error) { return torrent, nil })
	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "acquisition", ResourceID: store.command.AcquisitionID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(sharedPath, "episode.mkv")); err != nil {
		t.Fatalf("shared download was removed: %v", err)
	}
	if len(torrent.hashes) != 0 || !store.completed {
		t.Fatalf("shared cleanup = hashes %#v completed %t", torrent.hashes, store.completed)
	}
}

func TestAcquisitionDeleteNeverRemovesPathOverlappingMediaLibrary(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "downloads")
	libraryRoot := filepath.Join(downloadRoot, "library")
	libraryPath := filepath.Join(libraryRoot, "Show", "Season1")
	libraryFile := filepath.Join(libraryPath, "episode.mkv")
	if err := os.MkdirAll(filepath.Dir(libraryFile), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(libraryFile, []byte("library"), 0o640); err != nil {
		t.Fatal(err)
	}
	store := &acquisitionDeleteStoreStub{ready: true, command: domain.AcquisitionDeletionCommand{
		AcquisitionID: uuid.New(),
		Downloads:     []domain.AcquisitionDeletionDownload{{ID: uuid.New(), SavePath: libraryPath}},
	}}
	configuration := &acquisitionDeleteConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{
		DownloadRoot: downloadRoot, AnimeLibraryRoot: libraryRoot,
	}}}}
	handler := NewAcquisitionDeleteHandler(configuration, store, func(qbittorrent.ClientOptions) (AcquisitionDeleteTorrentClient, error) {
		return &acquisitionDeleteTorrentStub{}, nil
	})
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "acquisition", ResourceID: store.command.AcquisitionID})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "acquisition_delete_path_unsafe" || failure.Retryable {
		t.Fatalf("Handle() error = %#v, want permanent library path safety failure", err)
	}
	if store.completed {
		t.Fatal("database workflow was removed after protected library path cleanup")
	}
	if _, err := os.Stat(libraryFile); err != nil {
		t.Fatalf("library file changed: %v", err)
	}
}

func TestAcquisitionDeleteRejectsPathOutsideCleanupRoots(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "library", "episode.mkv")
	if err := os.MkdirAll(filepath.Dir(outside), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("library"), 0o640); err != nil {
		t.Fatal(err)
	}
	store := &acquisitionDeleteStoreStub{ready: true, command: domain.AcquisitionDeletionCommand{
		AcquisitionID: uuid.New(), ArtifactPaths: []string{outside},
	}}
	configuration := &acquisitionDeleteConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{
		StagingRoot: filepath.Join(root, "staging"), WorkRoot: filepath.Join(root, "work"),
	}}}}
	handler := NewAcquisitionDeleteHandler(configuration, store, func(qbittorrent.ClientOptions) (AcquisitionDeleteTorrentClient, error) {
		return &acquisitionDeleteTorrentStub{}, nil
	})
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "acquisition", ResourceID: store.command.AcquisitionID})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "acquisition_delete_path_unsafe" || failure.Retryable {
		t.Fatalf("Handle() error = %#v, want permanent path safety failure", err)
	}
	if store.completed {
		t.Fatal("database workflow was removed after unsafe file cleanup")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("protected file changed: %v", err)
	}
}
