package domain

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

type TaskMediaType string

const (
	TaskMediaEpisode TaskMediaType = "episode"
	TaskMediaMovie   TaskMediaType = "movie"
)

type TaskReview struct {
	ID         uuid.UUID
	Decision   TaskState
	Notes      string
	ReviewedBy uuid.UUID
	ReviewedAt time.Time
}

type TaskImport struct {
	ID                      uuid.UUID
	Attempt                 int
	Status                  string
	DestinationVideoPath    string
	DestinationSubtitlePath string
	ErrorCode               string
	ErrorMessage            string
	StartedAt               *time.Time
	CompletedAt             *time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type TaskCleanup struct {
	ID                 uuid.UUID
	Attempt            int
	Status             CleanupState
	TorrentRemoved     bool
	StagedFilesRemoved bool
	ErrorCode          string
	ErrorMessage       string
	StartedAt          *time.Time
	CompletedAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type TaskArtifactSet struct {
	ID       uuid.UUID
	BaseName string
	Video    MediaArtifact
	Subtitle MediaArtifact
}

type EpisodeTask struct {
	ID                              uuid.UUID
	MediaType                       TaskMediaType
	MovieTitle                      string
	ReleaseYear                     int
	AcquisitionID                   uuid.UUID
	DownloadID                      uuid.UUID
	SeriesTitle                     string
	SourceSeason                    int
	SourceEpisode                   int
	SourceEpisodeFractionHundredths int
	TargetSeason                    int
	TargetEpisode                   int
	TargetEpisodeTitle              string
	State                           TaskState
	VideoState                      VideoState
	SubtitleState                   SubtitleState
	Version                         int32
	FailureStage                    string
	ErrorCode                       string
	ErrorMessage                    string
	Artifacts                       *TaskArtifactSet
	Review                          *TaskReview
	Import                          *TaskImport
	Cleanup                         *TaskCleanup
	EmbyItemID                      *uuid.UUID
	EmbyLibraryID                   *uuid.UUID
	Operations                      []OperationSummary
	Actions                         TaskActions
	CreatedAt                       time.Time
	UpdatedAt                       time.Time
}

type OperationSummary struct {
	ID           uuid.UUID
	Kind         string
	Status       string
	MaxAttempts  int
	AttemptCount int
	ErrorCode    string
	ErrorMessage string
	StartedAt    *time.Time
	FinishedAt   *time.Time
	UpdatedAt    time.Time
}

type TaskActions struct {
	CanRetry  bool
	CanCancel bool
	CanReview bool
	CanImport bool
}

type EpisodeTaskPage struct {
	Items      []EpisodeTask
	NextCursor *uuid.UUID
}

type ReviewTask struct {
	TaskID          uuid.UUID
	ExpectedVersion int32
	Decision        TaskState
	Notes           string
	IdempotencyKey  string
	ActorUserID     uuid.UUID
}

type QueueTaskImport struct {
	TaskID          uuid.UUID
	ExpectedVersion int32
	IdempotencyKey  string
	ActorUserID     uuid.UUID
}

type TaskImportResult struct {
	Task      EpisodeTask
	Operation Operation
}

type ImportCommand struct {
	TaskID      uuid.UUID
	ImportID    uuid.UUID
	TaskState   TaskState
	ImportState string
	MediaType   TaskMediaType
	SeriesTitle string
	MovieTitle  string
	ReleaseYear int
	Season      int
	BaseName    string
	Video       MediaArtifact
	Subtitle    MediaArtifact
}

type ImportCompletion struct {
	TaskID                  uuid.UUID
	ImportID                uuid.UUID
	OperationID             uuid.UUID
	DestinationVideoPath    string
	DestinationSubtitlePath string
}

type CleanupCommand struct {
	TaskID             uuid.UUID
	CleanupID          uuid.UUID
	TaskState          TaskState
	CleanupState       CleanupState
	DownloadID         uuid.UUID
	TorrentHash        string
	DownloadPath       string
	StagedVideoPath    string
	StagedSubtitlePath string
	DownloadRemovable  bool
}

type CleanupCompletion struct {
	TaskID             uuid.UUID
	CleanupID          uuid.UUID
	OperationID        uuid.UUID
	TorrentRemoved     bool
	StagedFilesRemoved bool
}

type LibraryRelativePaths struct {
	Directory string
	Video     string
	Subtitle  string
}

func BuildMovieLibraryRelativePaths(movieTitle string, releaseYear int, videoName, subtitleName string) (LibraryRelativePaths, error) {
	movie := sanitizeMediaNamePart(movieTitle)
	if movie == "" {
		return LibraryRelativePaths{}, fmt.Errorf("library movie title must not be blank")
	}
	for _, name := range []string{videoName, subtitleName} {
		if strings.TrimSpace(name) == "" || filepath.Base(name) != name {
			return LibraryRelativePaths{}, fmt.Errorf("library filenames must be non-empty base names")
		}
	}
	videoExtension := strings.TrimPrefix(strings.ToLower(filepath.Ext(videoName)), ".")
	names, err := BuildMovieFileNames(MovieNamingRequest{MovieTitle: movie, ReleaseYear: releaseYear, VideoExtension: videoExtension})
	if err != nil {
		return LibraryRelativePaths{}, err
	}
	if videoName != names.VideoName || subtitleName != names.SubtitleName {
		return LibraryRelativePaths{}, fmt.Errorf("library movie filenames must use the canonical title and year basename")
	}
	directory := names.BaseName
	return LibraryRelativePaths{
		Directory: directory,
		Video:     filepath.Join(directory, videoName), Subtitle: filepath.Join(directory, subtitleName),
	}, nil
}

func BuildLibraryRelativePaths(seriesTitle string, season int, videoName, subtitleName string) (LibraryRelativePaths, error) {
	series := sanitizeMediaNamePart(seriesTitle)
	if series == "" {
		return LibraryRelativePaths{}, fmt.Errorf("library series title must not be blank")
	}
	if season < 0 {
		return LibraryRelativePaths{}, fmt.Errorf("library season must be nonnegative")
	}
	for _, name := range []string{videoName, subtitleName} {
		if strings.TrimSpace(name) == "" || filepath.Base(name) != name {
			return LibraryRelativePaths{}, fmt.Errorf("library filenames must be non-empty base names")
		}
	}
	directory := filepath.Join(series, fmt.Sprintf("Season%d", season))
	return LibraryRelativePaths{
		Directory: directory,
		Video:     filepath.Join(directory, videoName),
		Subtitle:  filepath.Join(directory, subtitleName),
	}, nil
}
