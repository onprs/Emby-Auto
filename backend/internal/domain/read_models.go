package domain

import (
	"time"

	"github.com/google/uuid"
)

// DownloadView is the read model for one download and its classified files.
type DownloadView struct {
	ID                   uuid.UUID
	AcquisitionID        uuid.UUID
	Attempt              int
	ClientName           string
	ClientState          string
	TorrentHash          string
	Status               string
	Progress             float64
	SavePath             string
	Version              int
	FailureStage         string
	FileResolutionSource string
	AgentResolutionID    *uuid.UUID
	LastSyncedAt         *time.Time
	ErrorCode            string
	ErrorMessage         string
	StartedAt            *time.Time
	CompletedAt          *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	Files                []DownloadFileView
	Actions              DownloadActions
}

type DownloadActions struct {
	CanRetry             bool
	CanCancel            bool
	CanDelete            bool
	CanEditFileSelection bool
	CanResolveFiles      bool
	CanRequestAgent      bool
}

type DownloadFileView struct {
	ID              uuid.UUID
	FileIndex       int
	RelativePath    string
	SizeBytes       int64
	MediaKind       string
	Selected        bool
	SourceSeason    *int
	SourceEpisode   *int
	Language        string
	ExclusionReason string
}

type DownloadPage struct {
	Items      []DownloadView
	NextCursor *uuid.UUID
}

// AcquisitionView is the read model for one acquisition and its relations.
type AcquisitionView struct {
	ID                       uuid.UUID
	Archived                 bool
	ArchivedAt               *time.Time
	MediaType                TaskMediaType
	SeriesID                 uuid.UUID
	TMDbSeriesID             int64
	SeriesTitle              string
	TMDbMovieID              int64
	MovieTitle               string
	ReleaseYear              int
	SourceKind               string
	SourceSeason             *int
	SourceEpisode            *int
	SingleEpisode            *bool
	MappingProfileID         *uuid.UUID
	MappingDecisionSource    string
	MappingAgentResolutionID *uuid.UUID
	ReleaseCandidateID       *uuid.UUID
	RSSEntryID               *uuid.UUID
	DownloadID               *uuid.UUID
	Download                 *AcquisitionDownloadSummary
	Tasks                    []AcquisitionTaskSummary
	Mapping                  AcquisitionMappingCompleteness
	AggregateStatus          string
	CurrentStage             string
	OverallProgress          float64
	Stages                   []AcquisitionStageView
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// AcquisitionProgressView is the compact lifecycle summary embedded in parent lists.
type AcquisitionProgressView struct {
	AggregateStatus string
	CurrentStage    string
	OverallProgress float64
}

// AcquisitionStageView is one derived stage of the user-facing task lifecycle.
// It is computed from durable workflow records rather than persisted separately.
type AcquisitionStageView struct {
	Key            string
	Status         string
	Progress       float64
	CompletedItems int
	TotalItems     int
	UpdatedAt      *time.Time
}

type AcquisitionDownloadSummary struct {
	ID           uuid.UUID
	Attempt      int
	Status       string
	Progress     float64
	FailureStage string
	ClientState  string
	ErrorCode    string
	ErrorMessage string
	UpdatedAt    time.Time
}

type AcquisitionTaskSummary struct {
	ID                      uuid.UUID
	MediaType               TaskMediaType
	DownloadID              uuid.UUID
	SourceSeason            int
	SourceEpisode           int
	TargetSeason            *int
	TargetEpisode           *int
	TargetEpisodeTitle      string
	State                   string
	VideoState              string
	SubtitleState           string
	ArtifactBasename        string
	ReviewDecision          string
	ReviewedAt              *time.Time
	ImportStatus            string
	DestinationVideoPath    string
	DestinationSubtitlePath string
	EmbyRefreshStatus       string
	CleanupStatus           string
	FailureStage            string
	ErrorCode               string
	ErrorMessage            string
	UpdatedAt               time.Time
}

type AcquisitionMappingCompleteness struct {
	SelectedVideoCount int
	MappedVideoCount   int
	Complete           bool
}

type AcquisitionPage struct {
	Items      []AcquisitionView
	NextCursor *uuid.UUID
}

// RSSEntryView is the read model for one persisted RSS entry.
type RSSEntryView struct {
	ID                       uuid.UUID
	SubscriptionID           uuid.UUID
	ReleaseCandidateID       *uuid.UUID
	AcquisitionID            *uuid.UUID
	AcquisitionProgress      *AcquisitionProgressView
	DownloadID               *uuid.UUID
	Title                    string
	Status                   string
	Classification           string
	DuplicateCount           int
	DownloadURIAvailable     bool
	PublishedAt              *time.Time
	SourceSeason             *int
	SourceEpisode            *int
	CoordinateSource         string
	AgentResolutionID        *uuid.UUID
	AdjudicationBatchID      *uuid.UUID
	AdjudicationState        string
	AdjudicationSource       string
	AdjudicationResolutionID *uuid.UUID
	RelatedEntryID           *uuid.UUID
	RejectReason             string
	ErrorCode                string
	ErrorMessage             string
	ImportedAt               *time.Time
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

type RSSEntryPage struct {
	Items      []RSSEntryView
	NextCursor *uuid.UUID
}

// OperationView is the audit read model for one operation and its attempts.
type OperationView struct {
	ID             uuid.UUID
	Kind           string
	ResourceType   string
	ResourceID     *uuid.UUID
	ResourceHref   string
	Status         string
	IdempotencyKey string
	MaxAttempts    int
	AttemptCount   int
	HeartbeatAt    *time.Time
	ErrorCode      string
	ErrorMessage   string
	CreatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
	UpdatedAt      time.Time
	Attempts       []OperationAttemptView
}

type OperationAttemptView struct {
	ID           uuid.UUID
	Attempt      int
	Status       string
	WorkerID     string
	ErrorCode    string
	ErrorMessage string
	StartedAt    time.Time
	HeartbeatAt  *time.Time
	FinishedAt   *time.Time
}

type OperationPage struct {
	Items      []OperationView
	NextCursor *uuid.UUID
}

// EventRecordView is one persisted domain event.
type EventRecordView struct {
	ID           uuid.UUID
	Topic        string
	ResourceType string
	ResourceID   *uuid.UUID
	OperationID  *uuid.UUID
	Data         []byte
	OccurredAt   time.Time
}

type EventRecordPage struct {
	Items      []EventRecordView
	NextCursor *uuid.UUID
}

// SearchRunSummary is the list read model for one search run.
type SearchRunSummary struct {
	ID           uuid.UUID
	Query        string
	Status       string
	ErrorCode    string
	ErrorMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type SearchRunSummaryPage struct {
	Items      []SearchRunSummary
	NextCursor *uuid.UUID
}

// TMDbSeriesSearchResult is one upstream TV series search hit.
type TMDbSeriesSearchResult struct {
	TMDbSeriesID int64
	Name         string
	OriginalName string
	FirstAirDate string
	Overview     string
}

// TMDbMovieSearchResult is one upstream movie search hit.
type TMDbMovieSearchResult struct {
	TMDbMovieID   int64
	Title         string
	OriginalTitle string
	ReleaseDate   string
	ReleaseYear   int
	Overview      string
}

// TMDbSeriesCatalogView is the locally synced catalog read model.
type TMDbSeriesCatalogView struct {
	SeriesID      uuid.UUID
	TMDbSeriesID  int64
	Title         string
	OriginalTitle string
	Synced        bool
	LastSyncedAt  *time.Time
	Seasons       []TMDbSeasonCatalogView
}

type TMDbSeasonCatalogView struct {
	SeasonNumber int
	Name         string
	EpisodeCount int
	Special      bool
	Episodes     []TMDbEpisodeCatalogView
}

type TMDbEpisodeCatalogView struct {
	EpisodeNumber int
	Title         string
	AirDate       string
}

// DashboardSummary aggregates counts and recent activity.
type DashboardSummary struct {
	Counts           DashboardStatusCounts
	AttentionItems   []DashboardAttentionItem
	RecentOperations []DashboardRecentOperation
	RecentImports    []DashboardRecentImport
	RecentScans      []DashboardRecentScan
	Dependencies     DashboardDependencies
	AgentResolutions DashboardAgentResolutionStats
	Links            DashboardLinks
}

type DashboardAgentResolutionStats struct {
	Total                      int
	ReviewPending              int
	Applied                    int
	AutoApplied                int
	Accepted                   int
	Rejected                   int
	Failed                     int
	InputTokens                int64
	OutputTokens               int64
	AverageLatencyMilliseconds int64
}

type DashboardStatusCounts struct {
	Downloading    int
	Processing     int
	AwaitingReview int
	Importing      int
	Attention      int
	Failed         int
	CleanupFailed  int
	MappingPending int
}

type DashboardAttentionItem struct {
	Acquisition  AcquisitionView
	Reason       string
	ErrorCode    string
	ErrorMessage string
}

type DashboardRecentOperation struct {
	ID           uuid.UUID
	Kind         string
	ResourceType string
	ResourceID   *uuid.UUID
	ResourceHref string
	Status       string
	ErrorCode    string
	ErrorMessage string
	UpdatedAt    time.Time
}

type DashboardRecentImport struct {
	TaskID          uuid.UUID
	AcquisitionID   uuid.UUID
	MediaType       TaskMediaType
	SeriesTitle     string
	MovieTitle      string
	ReleaseYear     int
	SeasonNumber    *int
	EpisodeNumber   *int
	DestinationPath string
	CompletedAt     time.Time
}

type DashboardRecentScan struct {
	ID           uuid.UUID
	OperationID  uuid.UUID
	Status       string
	LibraryCount int
	ItemCount    int
	ErrorCode    string
	ErrorMessage string
	CreatedAt    time.Time
	CompletedAt  *time.Time
}

type DashboardDependencyStatus struct {
	Success  bool
	Code     string
	Message  string
	TestedAt time.Time
	HasTest  bool
}

type DashboardDependencies struct {
	QBittorrent  DashboardDependencyStatus
	TMDb         DashboardDependencyStatus
	Emby         DashboardDependencyStatus
	MediaTools   DashboardDependencyStatus
	NetworkProxy DashboardDependencyStatus
	Agent        DashboardDependencyStatus
}

type DashboardLinks struct {
	Downloading    string
	Processing     string
	AwaitingReview string
	Importing      string
	Failed         string
	CleanupFailed  string
	MappingPending string
}

// ConnectivityTestRequest selects one dependency and supplies unsaved overrides.
type ConnectivityTestRequest struct {
	Target       string
	QBittorrent  *QBittorrentTestConfig
	Emby         *EmbyTestConfig
	TMDb         *TMDbTestConfig
	NetworkProxy *NetworkProxySettings
	Agent        *AgentTestConfig
}

type QBittorrentTestConfig struct {
	URL      string
	Username string
	Password *string
}

type EmbyTestConfig struct {
	URL    string
	APIKey *string
}

type TMDbTestConfig struct {
	APIToken *string
}

type AgentTestConfig struct {
	Protocol        string
	BaseURL         string
	Model           string
	APIKey          *string
	UseNetworkProxy bool
}

type ConnectivityTestResult struct {
	Target    string
	Success   bool
	Code      string
	Message   string
	CheckedAt time.Time
}
