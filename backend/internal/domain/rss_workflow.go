package domain

import (
	"time"

	"github.com/google/uuid"
)

type RSSSubscription struct {
	ID                        uuid.UUID
	SeriesID                  uuid.UUID
	SeriesTitle               string
	TMDbSeriesID              int64
	MappingProfileID          uuid.UUID
	Name                      string
	FeedURL                   string
	IncludeKeywords           []string
	ExcludeKeywords           []string
	Enabled                   bool
	AutoEpisodeMapping        bool
	AutoReview                bool
	CleanupSourceOnCompletion bool
	SourceSeason              int
	PollInterval              time.Duration
	LastPolledAt              *time.Time
	NextPollAt                *time.Time
	CompletedAt               *time.Time
	OverallProgress           float64
	TaskCount                 int
	CompletedTaskCount        int
	AttentionTaskCount        int
	RetryableTaskCount        int
	Version                   int32
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

type CreateRSSSubscription struct {
	TMDbSeriesID              int64
	SeriesTitle               string
	MappingProfileID          uuid.UUID
	Name                      string
	FeedURL                   string
	IncludeKeywords           []string
	ExcludeKeywords           []string
	Enabled                   bool
	AutoEpisodeMapping        bool
	AutoReview                bool
	CleanupSourceOnCompletion bool
	SourceSeason              int
	PollInterval              time.Duration
	ActorUserID               uuid.UUID
}

type UpdateRSSSubscription struct {
	ID                        uuid.UUID
	ExpectedVersion           int32
	MappingProfileID          uuid.UUID
	Name                      string
	FeedURL                   string
	IncludeKeywords           []string
	ExcludeKeywords           []string
	Enabled                   bool
	AutoEpisodeMapping        bool
	AutoReview                bool
	CleanupSourceOnCompletion bool
	SourceSeason              int
	PollInterval              time.Duration
	ActorUserID               uuid.UUID
}

type RSSSubscriptionPage struct {
	Items      []RSSSubscription
	NextCursor *uuid.UUID
}

type RSSPollCommand struct {
	SubscriptionID     uuid.UUID
	FeedURL            string
	IncludeKeywords    []string
	ExcludeKeywords    []string
	Enabled            bool
	AutoEpisodeMapping bool
	Deleted            bool
	Completed          bool
	SourceSeason       int
	PollInterval       time.Duration
	Version            int32
}

type RSSPollMappingPreparation struct {
	Ready                     bool
	Applied                   bool
	ScopeID                   uuid.UUID
	AgentCoordinateCandidates []uuid.UUID
}

type RSSPollPersistOptions struct {
	AdjudicateReleases bool
	RealtimeCheckID    uuid.UUID
}

type RSSPollPersistResult struct {
	FetchedCount              int
	Candidates                []RSSEnqueueCandidate
	AgentCoordinateCandidates []uuid.UUID
	AgentAdjudicationBatchIDs []uuid.UUID
}

// RSSCascadeItem is one acquisition affected by a subscription deletion.
type RSSCascadeItem struct {
	AcquisitionID   uuid.UUID
	DownloadID      uuid.UUID
	DownloadStatus  string
	TorrentHash     string
	SavePath        string
	DownloadVersion int32
	ActiveTasks     int
	ImportedTasks   int
}

// RSSCascadeResult summarizes a cascading subscription deletion.
type RSSCascadeResult struct {
	SubscriptionID   uuid.UUID
	Acquisitions     int
	TasksCancelled   int
	DownloadsRemoved int
	ImportedKept     int
	FailedItems      []RSSCascadeFailure
}

type RSSCascadeFailure struct {
	AcquisitionID uuid.UUID
	Stage         string
	Reason        string
}

type RSSPollBatchSummary struct {
	FetchedCount   int
	EligibleCount  int
	ScheduledCount int
	FailedCount    int
}
