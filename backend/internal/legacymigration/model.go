package legacymigration

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

const ReportVersion = 1

type Record struct {
	Kind      string
	LegacyID  string
	Payload   map[string]any
	Raw       json.RawMessage
	History   []map[string]any
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Snapshot struct {
	SourceKind       string
	DatabaseIdentity string
	Records          []Record
	RSSDrafts        []Record
	Inventory        map[string]int
}

type PathMapping struct {
	From string
	To   string
}

type ArtifactProfile struct {
	VideoCodec     string   `json:"videoCodec"`
	ContainerNames []string `json:"containerNames"`
	FileExtension  string   `json:"fileExtension"`
	AudioPolicy    string   `json:"audioPolicy"`
	AudioCodec     string   `json:"audioCodec,omitempty"`
}

type ArtifactProbeFunc func(context.Context, string) (domain.MediaProbe, error)

func (probe ArtifactProbeFunc) Probe(ctx context.Context, path string) (domain.MediaProbe, error) {
	return probe(ctx, path)
}

type PlanOptions struct {
	ProfileExtension string
	VerifyFiles      bool
	PathMappings     []PathMapping
	ArtifactProfile  *ArtifactProfile
	Probe            ArtifactProbeFunc
}

type Plan struct {
	SourceKind       string
	SourceIdentity   string
	ProfileExtension string
	ArtifactProfile  *ArtifactProfile
	Fingerprint      []byte
	Tasks            []PlannedTask
	Subscriptions    []PlannedSubscription
	Items            []PlannedItem
	Inventory        map[string]int
	Issues           []Issue
	Counts           Counts
}

type PlannedItem struct {
	ID           uuid.UUID
	SourceKind   string
	LegacyID     string
	Fingerprint  []byte
	Status       string
	ResourceType string
	ResourceID   uuid.UUID
	ErrorCode    string
	ErrorMessage string
	Payload      map[string]any
}

type PlannedTask struct {
	ItemID              uuid.UUID
	LegacyID            string
	Fingerprint         []byte
	SeriesKey           string
	SeriesID            uuid.UUID
	TMDbSeriesID        int64
	SeriesTitle         string
	MappingProfileID    uuid.UUID
	SeasonID            uuid.UUID
	EpisodeID           uuid.UUID
	MappingID           uuid.UUID
	SourceSeason        int
	SourceEpisode       int
	TargetSeason        int
	TargetEpisode       int
	EpisodeTitle        string
	AcquisitionID       uuid.UUID
	AcquisitionKey      string
	DownloadID          uuid.UUID
	TorrentHash         string
	SavePath            string
	SourceFileID        uuid.UUID
	SourceFileIndex     int
	SourceRelativePath  string
	SourceFileSize      int64
	TaskID              uuid.UUID
	TaskState           string
	VideoState          string
	SubtitleState       string
	ErrorCode           string
	ErrorMessage        string
	ArtifactSetID       uuid.UUID
	BaseName            string
	VideoArtifactID     uuid.UUID
	VideoPath           string
	VideoFormat         string
	VideoSize           int64
	VideoChecksum       []byte
	SubtitleArtifactID  uuid.UUID
	SubtitlePath        string
	SubtitleSize        int64
	SubtitleChecksum    []byte
	ReviewID            uuid.UUID
	ReviewDecision      string
	ReviewNotes         string
	ReviewedAt          time.Time
	ImportID            uuid.UUID
	LibraryVideoPath    string
	LibrarySubtitlePath string
	ImportedAt          time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Payload             map[string]any
	History             []map[string]any
}

type PlannedSubscription struct {
	ItemID           uuid.UUID
	LegacyID         string
	Fingerprint      []byte
	SeriesKey        string
	SeriesID         uuid.UUID
	TMDbSeriesID     int64
	SeriesTitle      string
	MappingProfileID uuid.UUID
	SubscriptionID   uuid.UUID
	Name             string
	FeedURL          string
	SourceSeason     int
	Enabled          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Payload          map[string]any
}

type Counts struct {
	Discovered    int `json:"discovered"`
	PlannedTasks  int `json:"plannedTasks"`
	PlannedRSS    int `json:"plannedRss"`
	Imported      int `json:"imported"`
	Unchanged     int `json:"unchanged"`
	Skipped       int `json:"skipped"`
	Invalid       int `json:"invalid"`
	ArtifactPairs int `json:"artifactPairs"`
	Events        int `json:"events"`
}

type Issue struct {
	SourceKind string `json:"sourceKind"`
	LegacyID   string `json:"legacyId,omitempty"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

type Report struct {
	Version      int            `json:"version"`
	RunID        uuid.UUID      `json:"runId"`
	Mode         string         `json:"mode"`
	SourceKind   string         `json:"sourceKind"`
	StartedAt    time.Time      `json:"startedAt"`
	CompletedAt  time.Time      `json:"completedAt"`
	Succeeded    bool           `json:"succeeded"`
	Fingerprint  string         `json:"fingerprint"`
	Inventory    map[string]int `json:"inventory"`
	Counts       Counts         `json:"counts"`
	Issues       []Issue        `json:"issues"`
	ErrorCode    string         `json:"errorCode,omitempty"`
	ErrorMessage string         `json:"errorMessage,omitempty"`
}
