package domain

import (
	"path/filepath"
	"testing"
)

func TestBuildMovieLibraryRelativePathsUsesTitleAndYearDirectoryAndBasename(t *testing.T) {
	paths, err := BuildMovieLibraryRelativePaths("流浪地球", 2019, "流浪地球(2019).mkv", "流浪地球(2019).ass")
	if err != nil {
		t.Fatalf("BuildMovieLibraryRelativePaths() error = %v", err)
	}
	wantDirectory := filepath.Join("流浪地球(2019)")
	if paths.Directory != wantDirectory || paths.Video != filepath.Join(wantDirectory, "流浪地球(2019).mkv") || paths.Subtitle != filepath.Join(wantDirectory, "流浪地球(2019).ass") {
		t.Fatalf("BuildMovieLibraryRelativePaths() = %#v", paths)
	}
}

func TestBuildMovieLibraryRelativePathsRejectsWrongBasename(t *testing.T) {
	if _, err := BuildMovieLibraryRelativePaths("Movie", 2024, "Other(2024).mp4", "Movie(2024).ass"); err == nil {
		t.Fatal("BuildMovieLibraryRelativePaths(wrong basename) error = nil")
	}
}

func TestBuildLibraryRelativePathsUsesCanonicalSeriesAndSeasonDirectory(t *testing.T) {
	paths, err := BuildLibraryRelativePaths(
		`Canonical: Show`,
		2,
		"Canonical Show - S02E01 - Episode Name.mkv",
		"Canonical Show - S02E01 - Episode Name.ass",
	)
	if err != nil {
		t.Fatalf("BuildLibraryRelativePaths() error = %v", err)
	}
	wantDirectory := filepath.Join("Canonical Show", "Season2")
	if paths.Directory != wantDirectory {
		t.Fatalf("directory = %q, want %q", paths.Directory, wantDirectory)
	}
	if paths.Video != filepath.Join(wantDirectory, "Canonical Show - S02E01 - Episode Name.mkv") {
		t.Fatalf("video path = %q", paths.Video)
	}
	if paths.Subtitle != filepath.Join(wantDirectory, "Canonical Show - S02E01 - Episode Name.ass") {
		t.Fatalf("subtitle path = %q", paths.Subtitle)
	}
}

func TestBuildLibraryRelativePathsRejectsTraversalFilename(t *testing.T) {
	if _, err := BuildLibraryRelativePaths("Show", 1, filepath.Join("..", "episode.mkv"), "episode.ass"); err == nil {
		t.Fatal("BuildLibraryRelativePaths(traversal) error = nil")
	}
}
