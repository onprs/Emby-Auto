package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type SyncTMDbSeries struct {
	TMDbSeriesID   int64
	SeriesTitle    string
	IdempotencyKey string
	ActorUserID    uuid.UUID
}

type CatalogCommandResult struct {
	SeriesID  uuid.UUID
	Operation Operation
}

type TMDbSeriesCatalog struct {
	TMDbSeriesID int64
	Name         string
	OriginalName string
	Payload      json.RawMessage
	Seasons      []TMDbSeasonCatalog
}

type TMDbSeasonCatalog struct {
	TMDbSeasonID int64
	SeasonNumber int
	Name         string
	Payload      json.RawMessage
	Episodes     []TMDbEpisodeCatalog
}

type TMDbEpisodeCatalog struct {
	TMDbEpisodeID int64
	EpisodeNumber int
	Name          string
	AirDate       *time.Time
	Payload       json.RawMessage
}

type EpisodeMappingAnchorInput struct {
	SourceFileID uuid.UUID         `json:"sourceFileId"`
	Target       EpisodeCoordinate `json:"target"`
}

type EpisodeMappingExplicitAction string

const (
	EpisodeMappingExplicitMap     EpisodeMappingExplicitAction = "map"
	EpisodeMappingExplicitExclude EpisodeMappingExplicitAction = "exclude"
)

type EpisodeMappingExplicitInput struct {
	SourceFileID uuid.UUID                    `json:"sourceFileId"`
	Action       EpisodeMappingExplicitAction `json:"action"`
	Target       EpisodeCoordinate            `json:"target,omitempty"`
}

type EpisodeMappingPlanInput struct {
	AcquisitionID  uuid.UUID
	Mode           EpisodeMappingMode
	Anchor         EpisodeMappingAnchorInput
	Assignments    []EpisodeMappingExplicitInput
	IdempotencyKey string
	ActorUserID    uuid.UUID
}

type EpisodeMappingRow struct {
	SourceFileID                    uuid.UUID
	RelativePath                    string
	SourceSeason                    int
	SourceEpisode                   int
	SourceEpisodeFractionHundredths int
	AbsoluteEpisode                 int
	Status                          MappingStatus
	TargetSeason                    int
	TargetEpisode                   int
	TargetEpisodeID                 uuid.UUID
	TargetTitle                     string
	MatchSource                     MappingMatchSource
	ErrorCode                       string
}

type EpisodeMappingPreview struct {
	AcquisitionID uuid.UUID
	SeriesID      uuid.UUID
	Mode          EpisodeMappingMode
	Anchor        EpisodeMappingAnchorInput
	Rows          []EpisodeMappingRow
}

type SavedEpisodeMapping struct {
	ProfileID uuid.UUID
	Version   int
	Preview   EpisodeMappingPreview
}

type EmbyScanStatus string

const (
	EmbyScanQueued    EmbyScanStatus = "queued"
	EmbyScanRunning   EmbyScanStatus = "running"
	EmbyScanSucceeded EmbyScanStatus = "succeeded"
	EmbyScanFailed    EmbyScanStatus = "failed"
	EmbyScanCancelled EmbyScanStatus = "cancelled"
)

type CreateEmbyScan struct {
	IdempotencyKey string
	ActorUserID    uuid.UUID
}

type CreateEmbyRefresh struct {
	IdempotencyKey string
	ActorUserID    uuid.UUID
}

type EmbyScan struct {
	ID           uuid.UUID
	OperationID  uuid.UUID
	Status       EmbyScanStatus
	LibraryCount int
	ItemCount    int
	ErrorCode    string
	ErrorMessage string
	StartedAt    *time.Time
	CompletedAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type EmbyScanCommandResult struct {
	Scan      EmbyScan
	Operation Operation
}

type EmbyScanPage struct {
	Items      []EmbyScan
	NextCursor *uuid.UUID
}

type EmbyLibraryCatalog struct {
	EmbyID         string
	Name           string
	CollectionType string
	Locations      []string
	Payload        json.RawMessage
}

type EmbyLibrarySnapshot struct {
	Library EmbyLibraryCatalog
	Items   []EmbyLibraryItemCatalog
}

type EmbyLibraryItemCatalog struct {
	EmbyID        string
	ParentEmbyID  string
	ItemType      string
	Name          string
	Path          string
	ProviderIDs   map[string]string
	SeasonNumber  *int
	EpisodeNumber *int
	Payload       json.RawMessage
}

type EmbyLibrary struct {
	ID             uuid.UUID
	EmbyID         string
	Name           string
	CollectionType string
	Locations      []string
	Present        bool
	LastSeenAt     time.Time
}

type EmbyLibraryItem struct {
	ID             uuid.UUID
	EmbyID         string
	LibraryID      uuid.UUID
	ParentEmbyID   string
	ItemType       string
	Name           string
	Path           string
	ProviderIDs    map[string]string
	SeasonNumber   *int
	EpisodeNumber  *int
	Present        bool
	ImportedTaskID *uuid.UUID
	LastSeenAt     time.Time
}

type EmbyLibraryItemPage struct {
	Items      []EmbyLibraryItem
	NextCursor *uuid.UUID
}

type EmbyLibraryItemFilter struct {
	ItemType   string
	Name       string
	ProviderID string
	Present    *bool
}
