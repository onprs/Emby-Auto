package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type AgentCapability string

const (
	AgentCapabilityRSSCoordinate            AgentCapability = "rss_coordinate"
	AgentCapabilityRSSReleaseAdjudication   AgentCapability = "rss_release_adjudication"
	AgentCapabilityRSSPreacquisitionMapping AgentCapability = "rss_preacquisition_mapping"
	AgentCapabilityDownloadFileResolution   AgentCapability = "download_file_resolution"
	AgentCapabilityCatalogCandidate         AgentCapability = "catalog_candidate"
	AgentCapabilityEpisodeMapping           AgentCapability = "episode_mapping"
	AgentCapabilitySubtitleVideoMatch       AgentCapability = "subtitle_video_match"
)

type AgentResolutionStatus string

const (
	AgentResolutionQueued         AgentResolutionStatus = "queued"
	AgentResolutionRunning        AgentResolutionStatus = "running"
	AgentResolutionProposed       AgentResolutionStatus = "proposed"
	AgentResolutionReviewRequired AgentResolutionStatus = "review_required"
	AgentResolutionApplied        AgentResolutionStatus = "applied"
	AgentResolutionRejected       AgentResolutionStatus = "rejected"
	AgentResolutionFailed         AgentResolutionStatus = "failed"
	AgentResolutionCancelled      AgentResolutionStatus = "cancelled"
	AgentResolutionExpired        AgentResolutionStatus = "expired"
)

type AgentValidationVerdict string

const (
	AgentValidationAutoApplicable AgentValidationVerdict = "auto_applicable"
	AgentValidationReviewRequired AgentValidationVerdict = "review_required"
	AgentValidationInvalid        AgentValidationVerdict = "invalid"
)

type DecisionSource string

const (
	DecisionSourceDeterministic DecisionSource = "deterministic"
	DecisionSourceAgentAuto     DecisionSource = "agent_auto"
	DecisionSourceAgentAccepted DecisionSource = "agent_accepted"
	DecisionSourceUser          DecisionSource = "user"
	DecisionSourceLegacy        DecisionSource = "legacy"
)

type AgentProposalValidation struct {
	Verdict     AgentValidationVerdict `json:"verdict"`
	ReasonCodes []string               `json:"reasonCodes"`
}

type AgentRSSCoordinateProposal struct {
	EntryID       uuid.UUID `json:"entryId"`
	SourceSeason  int       `json:"sourceSeason"`
	SourceEpisode int       `json:"sourceEpisode"`
	EvidenceCodes []string  `json:"evidenceCodes"`
	Decision      string    `json:"decision"`
}

type AgentRSSReleaseDisposition struct {
	EntryID        uuid.UUID  `json:"entryId"`
	Disposition    string     `json:"disposition"`
	SourceSeason   *int       `json:"sourceSeason,omitempty"`
	SourceEpisode  *int       `json:"sourceEpisode,omitempty"`
	RelatedEntryID *uuid.UUID `json:"relatedEntryId,omitempty"`
	EvidenceCodes  []string   `json:"evidenceCodes"`
}

type AgentRSSReleaseAdjudicationProposal struct {
	BatchID        uuid.UUID                    `json:"batchId"`
	ScopedEntryIDs []uuid.UUID                  `json:"scopedEntryIds"`
	Entries        []AgentRSSReleaseDisposition `json:"entries"`
	Decision       string                       `json:"decision"`
}

type AgentRSSPreacquisitionMappingProposal struct {
	ScopeID       uuid.UUID `json:"scopeId"`
	SourceSeason  int       `json:"sourceSeason"`
	SourceEpisode int       `json:"sourceEpisode"`
	TargetSeason  int       `json:"targetSeason"`
	TargetEpisode int       `json:"targetEpisode"`
	EvidenceCodes []string  `json:"evidenceCodes"`
	Decision      string    `json:"decision"`
}

type AgentDownloadVideoProposal struct {
	FileID        uuid.UUID `json:"fileId"`
	SourceSeason  int       `json:"sourceSeason"`
	SourceEpisode int       `json:"sourceEpisode"`
}

type AgentDownloadSubtitleProposal struct {
	FileID      uuid.UUID `json:"fileId"`
	VideoFileID uuid.UUID `json:"videoFileId"`
}

type AgentDownloadFileResolutionProposal struct {
	Videos    []AgentDownloadVideoProposal    `json:"videos"`
	Subtitles []AgentDownloadSubtitleProposal `json:"subtitles"`
	Decision  string                          `json:"decision"`
}

type AgentCatalogCandidateProposal struct {
	Query         string   `json:"query"`
	CandidateIDs  []int64  `json:"candidateIds"`
	EvidenceCodes []string `json:"evidenceCodes"`
	Decision      string   `json:"decision"`
}

type AgentEpisodeMappingProposal struct {
	AcquisitionID uuid.UUID `json:"acquisitionId"`
	SourceFileID  uuid.UUID `json:"sourceFileId"`
	TargetSeason  int       `json:"targetSeason"`
	TargetEpisode int       `json:"targetEpisode"`
	EvidenceCodes []string  `json:"evidenceCodes"`
	Decision      string    `json:"decision"`
}

type SubtitleCandidateSelection struct {
	CandidateID string `json:"candidateId"`
}

type AgentSubtitleVideoMatchProposal struct {
	TaskID        uuid.UUID                  `json:"taskId"`
	Selected      SubtitleCandidateSelection `json:"selected"`
	EvidenceCodes []string                   `json:"evidenceCodes"`
	Decision      string                     `json:"decision"`
}

type AgentResolution struct {
	ID                   uuid.UUID
	OperationID          uuid.UUID
	Version              int
	Capability           AgentCapability
	ResourceType         string
	ResourceID           uuid.UUID
	ResourceVersion      *int
	Trigger              string
	Status               AgentResolutionStatus
	InputFingerprint     []byte
	ConfigurationVersion int
	Protocol             string
	ProviderOrigin       string
	Model                string
	PromptVersion        string
	ToolsetVersion       string
	Proposal             json.RawMessage
	Validation           AgentProposalValidation
	ErrorCode            string
	ErrorMessage         string
	InputTokens          *int64
	OutputTokens         *int64
	ToolCallCount        int
	LatencyMilliseconds  *int64
	CreatedBy            *uuid.UUID
	ReviewedBy           *uuid.UUID
	ReviewDecision       string
	CreatedAt            time.Time
	StartedAt            *time.Time
	CompletedAt          *time.Time
	ReviewedAt           *time.Time
	AppliedAt            *time.Time
	UpdatedAt            time.Time
}

type AgentResolutionPage struct {
	Items      []AgentResolution
	NextCursor *uuid.UUID
}

type AgentResolutionStep struct {
	Sequence             int
	ToolName             string
	Status               string
	ArgumentsDigest      []byte
	ResultDigest         []byte
	DurationMilliseconds *int64
	ErrorCode            string
	CreatedAt            time.Time
}
