package worker

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/riverqueue/river"
)

type taskImportStoreStub struct {
	command     domain.ImportCommand
	beginErr    error
	completion  domain.ImportCompletion
	completeErr error
}

type testImportedLibraryAccess struct {
	paths     []string
	err       error
	failAfter int
}

func (access *testImportedLibraryAccess) Apply(path string) error {
	access.paths = append(access.paths, path)
	if access.err != nil && (access.failAfter == 0 || len(access.paths) >= access.failAfter) {
		return access.err
	}
	return os.Chmod(path, importedLibraryPathMode)
}

func (access *testImportedLibraryAccess) ApplyTree(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink")
		}
		return access.Apply(path)
	})
}

func newTestImportedLibraryAccess() *testImportedLibraryAccess {
	return &testImportedLibraryAccess{}
}

func (stub *taskImportStoreStub) BeginImport(context.Context, uuid.UUID, uuid.UUID) (domain.ImportCommand, error) {
	return stub.command, stub.beginErr
}

func (stub *taskImportStoreStub) CompleteImport(_ context.Context, completion domain.ImportCompletion) error {
	stub.completion = completion
	return stub.completeErr
}

type cleanupStoreStub struct {
	command     domain.CleanupCommand
	beginErr    error
	completion  domain.CleanupCompletion
	completeErr error
}

func (stub *cleanupStoreStub) BeginCleanup(context.Context, uuid.UUID, uuid.UUID) (domain.CleanupCommand, error) {
	return stub.command, stub.beginErr
}

func (stub *cleanupStoreStub) CompleteCleanup(_ context.Context, completion domain.CleanupCompletion) error {
	stub.completion = completion
	return stub.completeErr
}

type cleanupTorrentStub struct {
	loginCalls  int
	deleteCalls []string
	loginErr    error
	deleteErr   error
}

func (stub *cleanupTorrentStub) Login(context.Context) error {
	stub.loginCalls++
	return stub.loginErr
}

func (stub *cleanupTorrentStub) DeleteTorrent(_ context.Context, hash string, _ bool) error {
	stub.deleteCalls = append(stub.deleteCalls, hash)
	return stub.deleteErr
}

func TestEmbyImportCopiesAndCommitsValidatedPair(t *testing.T) {
	root := t.TempDir()
	stagingRoot := filepath.Join(root, "staging")
	animeLibraryRoot := filepath.Join(root, "library", "anime")
	movieLibraryRoot := filepath.Join(root, "library", "movies")
	taskID := uuid.MustParse("78000000-0000-0000-0000-000000000001")
	importID := uuid.MustParse("78000000-0000-0000-0000-000000000002")
	operationID := uuid.MustParse("78000000-0000-0000-0000-000000000003")
	if err := os.MkdirAll(animeLibraryRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(movieLibraryRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	preexistingDirectory := filepath.Join(animeLibraryRoot, "Canonical Show", "Season2")
	metadataDirectory := filepath.Join(preexistingDirectory, "metadata")
	if err := os.MkdirAll(metadataDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(preexistingDirectory, "episode.nfo")
	seriesMetadataPath := filepath.Join(filepath.Dir(preexistingDirectory), "series.nfo")
	artworkPath := filepath.Join(metadataDirectory, "poster.png")
	if err := os.WriteFile(metadataPath, []byte("metadata"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seriesMetadataPath, []byte("series metadata"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artworkPath, []byte("artwork"), 0o640); err != nil {
		t.Fatal(err)
	}
	stageDirectory := filepath.Join(stagingRoot, taskID.String())
	if err := os.MkdirAll(stageDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	baseName := "Canonical Show - S02E01 - Episode Name"
	videoContent := []byte("validated-video")
	subtitleContent := validASSFixture
	videoPath := filepath.Join(stageDirectory, baseName+".mkv")
	subtitlePath := filepath.Join(stageDirectory, baseName+".ass")
	if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, subtitleContent, 0o640); err != nil {
		t.Fatal(err)
	}
	store := &taskImportStoreStub{command: importCommandFixture(taskID, importID, baseName, videoPath, subtitlePath, videoContent, subtitleContent)}
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{
		AnimeLibraryRoot: animeLibraryRoot,
		MovieLibraryRoot: movieLibraryRoot,
	}}}}
	libraryAccess := newTestImportedLibraryAccess()
	handler := NewEmbyImportHandler(configuration, store, libraryAccess)

	err := handler.Handle(context.Background(), domain.Operation{
		ID: operationID, ResourceType: "episode_task", ResourceID: taskID,
		Payload: []byte(`{"importId":"78000000-0000-0000-0000-000000000002"}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	wantDirectory := filepath.Join(animeLibraryRoot, "Canonical Show", "Season2")
	wantVideo := filepath.Join(wantDirectory, baseName+".mkv")
	wantSubtitle := filepath.Join(wantDirectory, baseName+".ass")
	if store.completion.DestinationVideoPath != wantVideo || store.completion.DestinationSubtitlePath != wantSubtitle {
		t.Fatalf("completion = %#v", store.completion)
	}
	assertFileContent(t, wantVideo, videoContent)
	assertFileContent(t, wantSubtitle, subtitleContent)
	assertFilePermissions(t, filepath.Dir(wantDirectory), importedLibraryPathMode)
	assertFilePermissions(t, wantDirectory, importedLibraryPathMode)
	assertFilePermissions(t, metadataDirectory, importedLibraryPathMode)
	assertFilePermissions(t, metadataPath, importedLibraryPathMode)
	assertFilePermissions(t, seriesMetadataPath, importedLibraryPathMode)
	assertFilePermissions(t, artworkPath, importedLibraryPathMode)
	assertFilePermissions(t, wantVideo, importedLibraryPathMode)
	assertFilePermissions(t, wantSubtitle, importedLibraryPathMode)
	assertFileContent(t, videoPath, videoContent)
	matches, err := filepath.Glob(filepath.Join(wantDirectory, "*.part*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary imports = %v, error %v", matches, err)
	}
	if _, err := os.Stat(movieLibraryRoot); err != nil {
		t.Fatalf("movie library root was changed or removed: %v", err)
	}
	for _, path := range libraryAccess.paths {
		if path == animeLibraryRoot || path == movieLibraryRoot {
			t.Fatalf("library access touched configured root %q", path)
		}
	}
	for _, required := range []string{
		filepath.Dir(wantDirectory), wantDirectory, metadataDirectory, metadataPath, seriesMetadataPath, artworkPath, wantVideo, wantSubtitle,
	} {
		if !containsPath(libraryAccess.paths, required) {
			t.Fatalf("library access paths %v do not include %q", libraryAccess.paths, required)
		}
	}
}

func TestEmbyImportCopiesMoviePairToMovieLibraryTitleDirectory(t *testing.T) {
	root := t.TempDir()
	animeRoot := filepath.Join(root, "anime")
	movieRoot := filepath.Join(root, "movies")
	if err := os.MkdirAll(animeRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(movieRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "staging")
	if err := os.MkdirAll(stage, 0o750); err != nil {
		t.Fatal(err)
	}
	taskID := uuid.MustParse("78000000-0000-0000-0000-000000000014")
	importID := uuid.MustParse("78000000-0000-0000-0000-000000000015")
	baseName := "流浪地球(2019)"
	videoContent := []byte("movie-video")
	videoPath := filepath.Join(stage, baseName+".mkv")
	subtitlePath := filepath.Join(stage, baseName+".ass")
	if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, validASSFixture, 0o640); err != nil {
		t.Fatal(err)
	}
	command := importCommandFixture(taskID, importID, baseName, videoPath, subtitlePath, videoContent, validASSFixture)
	command.MediaType, command.MovieTitle, command.ReleaseYear = domain.TaskMediaMovie, "流浪地球", 2019
	store := &taskImportStoreStub{command: command}
	handler := NewEmbyImportHandler(&downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{
		AnimeLibraryRoot: animeRoot, MovieLibraryRoot: movieRoot,
	}}}}, store, newTestImportedLibraryAccess())
	if err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID, Payload: []byte(`{"importId":"78000000-0000-0000-0000-000000000015"}`)}); err != nil {
		t.Fatalf("Handle(movie) error = %v", err)
	}
	wantDirectory := filepath.Join(movieRoot, baseName)
	wantVideo := filepath.Join(wantDirectory, baseName+".mkv")
	wantSubtitle := filepath.Join(wantDirectory, baseName+".ass")
	assertFileContent(t, wantVideo, videoContent)
	assertFileContent(t, wantSubtitle, validASSFixture)
	assertFilePermissions(t, wantDirectory, importedLibraryPathMode)
	assertFilePermissions(t, wantVideo, importedLibraryPathMode)
	assertFilePermissions(t, wantSubtitle, importedLibraryPathMode)
	if _, err := os.Stat(animeRoot); err != nil {
		t.Fatalf("anime library root was changed or removed: %v", err)
	}
}

func TestEmbyImportCrashReplayUsesCommittedPairWithoutStagedFiles(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	taskID := uuid.MustParse("78000000-0000-0000-0000-000000000004")
	importID := uuid.MustParse("78000000-0000-0000-0000-000000000005")
	baseName := "Canonical Show - S01E01 - Pilot"
	videoContent := []byte("already-imported-video")
	subtitleContent := validASSFixture
	destination := filepath.Join(libraryRoot, "Canonical Show", "Season2")
	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatal(err)
	}
	videoDestination := filepath.Join(destination, baseName+".mkv")
	subtitleDestination := filepath.Join(destination, baseName+".ass")
	if err := os.WriteFile(videoDestination, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitleDestination, subtitleContent, 0o640); err != nil {
		t.Fatal(err)
	}
	store := &taskImportStoreStub{command: importCommandFixture(taskID, importID, baseName, filepath.Join(root, "missing", baseName+".mkv"), filepath.Join(root, "missing", baseName+".ass"), videoContent, subtitleContent)}
	handler := NewEmbyImportHandler(&downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{LibraryRoot: libraryRoot}}}}, store, newTestImportedLibraryAccess())
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID, Payload: []byte(`{"importId":"78000000-0000-0000-0000-000000000005"}`)})
	if err != nil {
		t.Fatalf("Handle(replay) error = %v", err)
	}
	if store.completion.DestinationVideoPath != videoDestination || store.completion.DestinationSubtitlePath != subtitleDestination {
		t.Fatalf("replay completion = %#v", store.completion)
	}
	assertFilePermissions(t, destination, importedLibraryPathMode)
	assertFilePermissions(t, videoDestination, importedLibraryPathMode)
	assertFilePermissions(t, subtitleDestination, importedLibraryPathMode)
}

func TestEmbyImportRejectsDifferentExistingDestination(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	stagingRoot := filepath.Join(root, "staging")
	taskID := uuid.MustParse("78000000-0000-0000-0000-000000000006")
	importID := uuid.MustParse("78000000-0000-0000-0000-000000000007")
	baseName := "Canonical Show - S01E01 - Pilot"
	stage := filepath.Join(stagingRoot, taskID.String())
	destination := filepath.Join(libraryRoot, "Canonical Show", "Season2")
	_ = os.MkdirAll(stage, 0o750)
	_ = os.MkdirAll(destination, 0o750)
	videoContent := []byte("expected-video")
	_ = os.WriteFile(filepath.Join(stage, baseName+".mkv"), videoContent, 0o640)
	_ = os.WriteFile(filepath.Join(stage, baseName+".ass"), validASSFixture, 0o640)
	_ = os.WriteFile(filepath.Join(destination, baseName+".mkv"), []byte("different-video"), 0o640)
	store := &taskImportStoreStub{command: importCommandFixture(taskID, importID, baseName, filepath.Join(stage, baseName+".mkv"), filepath.Join(stage, baseName+".ass"), videoContent, validASSFixture)}
	handler := NewEmbyImportHandler(&downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{AnimeLibraryRoot: libraryRoot}}}}, store, newTestImportedLibraryAccess())
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID, Payload: []byte(`{"importId":"78000000-0000-0000-0000-000000000007"}`)})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "library_destination_conflict" || failure.Retryable {
		t.Fatalf("Handle(conflict) error = %#v", err)
	}
	if store.completion.ImportID != uuid.Nil {
		t.Fatalf("conflicting import was completed: %#v", store.completion)
	}
	assertFilePermissions(t, filepath.Join(destination, baseName+".mkv"), 0o640)
}

func TestEmbyImportAccessFailureDoesNotCompleteImport(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	stagingRoot := filepath.Join(root, "staging")
	if err := os.MkdirAll(libraryRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stagingRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	taskID := uuid.New()
	importID := uuid.New()
	baseName := "Access Failure - S01E01 - Pilot"
	videoContent := []byte("video")
	videoPath := filepath.Join(stagingRoot, baseName+".mkv")
	subtitlePath := filepath.Join(stagingRoot, baseName+".ass")
	if err := os.WriteFile(videoPath, videoContent, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitlePath, validASSFixture, 0o640); err != nil {
		t.Fatal(err)
	}
	store := &taskImportStoreStub{command: importCommandFixture(taskID, importID, baseName, videoPath, subtitlePath, videoContent, validASSFixture)}
	libraryAccess := &testImportedLibraryAccess{err: errors.New("chown denied"), failAfter: 3}
	handler := NewEmbyImportHandler(
		&downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{AnimeLibraryRoot: libraryRoot}}}},
		store,
		libraryAccess,
	)
	payload := []byte(`{"importId":"` + importID.String() + `"}`)
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID, Payload: payload})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "library_import_failed" || !failure.Retryable {
		t.Fatalf("Handle(access failure) error = %#v", err)
	}
	if store.completion.ImportID != uuid.Nil {
		t.Fatalf("access failure completed import: %#v", store.completion)
	}
	matches, globErr := filepath.Glob(filepath.Join(libraryRoot, "Access Failure", "Season2", "*"))
	if globErr != nil || len(matches) != 0 {
		t.Fatalf("access failure committed files %v, error %v", matches, globErr)
	}
}

func TestPrepareImportedLibraryDirectoriesRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(libraryRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(libraryRoot, "Linked Show")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	access := newTestImportedLibraryAccess()
	_, err := prepareImportedLibraryDirectories(libraryRoot, filepath.Join(link, "Season1"), access)
	if err == nil {
		t.Fatal("prepareImportedLibraryDirectories() error = nil")
	}
	if len(access.paths) != 0 {
		t.Fatalf("symlink rejection touched paths %v", access.paths)
	}
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

func assertFilePermissions(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("permissions for %q = %04o, want %04o", path, got, want)
	}
}

func TestCleanupRemovesStagedPairButProtectsSharedDownload(t *testing.T) {
	root := t.TempDir()
	stagingRoot := filepath.Join(root, "staging")
	downloadRoot := filepath.Join(root, "downloads")
	taskID := uuid.MustParse("78000000-0000-0000-0000-000000000008")
	cleanupID := uuid.MustParse("78000000-0000-0000-0000-000000000009")
	stage := filepath.Join(stagingRoot, taskID.String())
	download := filepath.Join(downloadRoot, "shared-download")
	_ = os.MkdirAll(stage, 0o750)
	_ = os.MkdirAll(download, 0o750)
	video := filepath.Join(stage, "episode.mkv")
	subtitle := filepath.Join(stage, "episode.ass")
	_ = os.WriteFile(video, []byte("video"), 0o640)
	_ = os.WriteFile(subtitle, validASSFixture, 0o640)
	store := &cleanupStoreStub{command: domain.CleanupCommand{
		TaskID: taskID, CleanupID: cleanupID, TaskState: domain.TaskImported, CleanupState: domain.CleanupQueued,
		DownloadPath: download, StagedVideoPath: video, StagedSubtitlePath: subtitle, DownloadRemovable: false,
	}}
	factoryCalled := false
	handler := NewCleanupRunHandler(
		&downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{StagingRoot: stagingRoot, DownloadRoot: downloadRoot}}}},
		store,
		func(qbittorrent.ClientOptions) (CleanupTorrentClient, error) {
			factoryCalled = true
			return &cleanupTorrentStub{}, nil
		},
	)
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID, Payload: []byte(`{"cleanupId":"78000000-0000-0000-0000-000000000009"}`)})
	var snoozeErr *river.JobSnoozeError
	if !errors.As(err, &snoozeErr) {
		t.Fatalf("Handle() error = %#v, want cleanup guard snooze", err)
	}
	if factoryCalled || store.completion.CleanupID != uuid.Nil {
		t.Fatalf("cleanup guard result = factory %t completion %#v", factoryCalled, store.completion)
	}
	if _, err := os.Stat(download); err != nil {
		t.Fatalf("shared download was removed: %v", err)
	}
	if !pathsAbsent(video, subtitle) {
		t.Fatal("staged artifacts remain while cleanup waits on sibling tasks")
	}
}

func TestCleanupRemovesTorrentWithoutDeleteFilesThenDownloadCache(t *testing.T) {
	root := t.TempDir()
	stagingRoot := filepath.Join(root, "staging")
	downloadRoot := filepath.Join(root, "downloads")
	taskID := uuid.MustParse("78000000-0000-0000-0000-000000000010")
	cleanupID := uuid.MustParse("78000000-0000-0000-0000-000000000011")
	stage := filepath.Join(stagingRoot, taskID.String())
	download := filepath.Join(downloadRoot, taskID.String())
	_ = os.MkdirAll(stage, 0o750)
	_ = os.MkdirAll(download, 0o750)
	video := filepath.Join(stage, "episode.mkv")
	subtitle := filepath.Join(stage, "episode.ass")
	_ = os.WriteFile(video, []byte("video"), 0o640)
	_ = os.WriteFile(subtitle, validASSFixture, 0o640)
	store := &cleanupStoreStub{command: domain.CleanupCommand{
		TaskID: taskID, CleanupID: cleanupID, TaskState: domain.TaskImported, CleanupState: domain.CleanupQueued,
		TorrentHash: workerTorrentHash, DownloadPath: download, StagedVideoPath: video, StagedSubtitlePath: subtitle, DownloadRemovable: true,
	}}
	torrent := &cleanupTorrentStub{}
	configuration := &downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{
		QBittorrent: domain.QBittorrentSettings{URL: "http://qb.test", Username: "admin"},
		Paths:       domain.PathSettings{StagingRoot: stagingRoot, DownloadRoot: downloadRoot},
	}}, password: "secret"}
	handler := NewCleanupRunHandler(configuration, store, func(qbittorrent.ClientOptions) (CleanupTorrentClient, error) { return torrent, nil })
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID, Payload: []byte(`{"cleanupId":"78000000-0000-0000-0000-000000000011"}`)})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if torrent.loginCalls != 1 || len(torrent.deleteCalls) != 1 || torrent.deleteCalls[0] != workerTorrentHash {
		t.Fatalf("torrent calls = login %d delete %v", torrent.loginCalls, torrent.deleteCalls)
	}
	if _, err := os.Stat(download); !os.IsNotExist(err) {
		t.Fatalf("download cache still exists, error = %v", err)
	}
	if !store.completion.TorrentRemoved || !store.completion.StagedFilesRemoved {
		t.Fatalf("completion = %#v", store.completion)
	}
}

func TestCleanupRejectsPathOutsideConfiguredRoots(t *testing.T) {
	root := t.TempDir()
	stagingRoot := filepath.Join(root, "staging")
	outside := filepath.Join(root, "outside.mkv")
	_ = os.MkdirAll(stagingRoot, 0o750)
	_ = os.WriteFile(outside, []byte("do-not-delete"), 0o640)
	taskID := uuid.MustParse("78000000-0000-0000-0000-000000000012")
	cleanupID := uuid.MustParse("78000000-0000-0000-0000-000000000013")
	store := &cleanupStoreStub{command: domain.CleanupCommand{
		TaskID: taskID, CleanupID: cleanupID, TaskState: domain.TaskImported, CleanupState: domain.CleanupQueued,
		StagedVideoPath: outside, StagedSubtitlePath: filepath.Join(stagingRoot, "missing.ass"),
	}}
	handler := NewCleanupRunHandler(&downloadConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Paths: domain.PathSettings{StagingRoot: stagingRoot}}}}, store, func(qbittorrent.ClientOptions) (CleanupTorrentClient, error) { return &cleanupTorrentStub{}, nil })
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID, Payload: []byte(`{"cleanupId":"78000000-0000-0000-0000-000000000013"}`)})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "cleanup_path_unsafe" || failure.Retryable {
		t.Fatalf("Handle(unsafe) error = %#v", err)
	}
	assertFileContent(t, outside, []byte("do-not-delete"))
}

func importCommandFixture(taskID, importID uuid.UUID, baseName, videoPath, subtitlePath string, videoContent, subtitleContent []byte) domain.ImportCommand {
	videoChecksum := sha256.Sum256(videoContent)
	subtitleChecksum := sha256.Sum256(subtitleContent)
	return domain.ImportCommand{
		TaskID: taskID, ImportID: importID, TaskState: domain.TaskImporting, ImportState: "running", SeriesTitle: "Canonical: Show", Season: 2, BaseName: baseName,
		Video:    domain.MediaArtifact{TaskID: taskID, Kind: domain.MediaVideo, BaseName: baseName, FilePath: videoPath, Format: "matroska", SizeBytes: int64(len(videoContent)), ChecksumSHA256: videoChecksum[:]},
		Subtitle: domain.MediaArtifact{TaskID: taskID, Kind: domain.MediaSubtitle, BaseName: baseName, FilePath: subtitlePath, Format: "ass", SizeBytes: int64(len(subtitleContent)), ChecksumSHA256: subtitleChecksum[:]},
	}
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("file %q = %q, error %v", path, got, err)
	}
}
