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

// RSSSubscriptionDeletionPollState 是删除订阅前在订阅行锁内捕获的轮询意图。
// 它随 rss.subscription.delete 操作的 payload 持久化，供终态失败恢复在同一事务内还原；
// 缺少该快照的旧在途操作无法重建原文意图，恢复时不得猜测 enabled。
type RSSSubscriptionDeletionPollState struct {
	Enabled    bool       `json:"enabled"`
	NextPollAt *time.Time `json:"nextPollAt"`
	Version    int32      `json:"version"`
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
	AdjudicateReleases          bool
	RealtimeCheckID             uuid.UUID
	ExpectedSubscriptionVersion int32
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
