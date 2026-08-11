package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

type SearchStatus string

const (
	SearchQueued    SearchStatus = "queued"
	SearchRunning   SearchStatus = "running"
	SearchCompleted SearchStatus = "completed"
	SearchFailed    SearchStatus = "failed"
	SearchCancelled SearchStatus = "cancelled"
)

type SearchRun struct {
	ID           uuid.UUID
	Query        string
	Status       SearchStatus
	RequestedBy  uuid.UUID
	ErrorCode    string
	ErrorMessage string
	StartedAt    *time.Time
	CompletedAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Candidates   []ReleaseCandidate
}

type ReleaseCandidate struct {
	ID              uuid.UUID
	SearchRunID     uuid.UUID
	Provider        string
	IdentityKey     string
	Title           string
	DownloadURI     string
	PublishedAt     *time.Time
	SizeBytes       *int64
	Seeders         *int
	UpstreamPayload map[string]any
	CreatedAt       time.Time
}

type CreateSearch struct {
	Query          string
	IdempotencyKey string
	ActorUserID    uuid.UUID
}

type SearchCommand struct {
	ID     uuid.UUID
	Query  string
	Status SearchStatus
}

type SearchProviderFailure struct {
	Provider string
	Code     string
	Message  string
}

type SearchProviderResult struct {
	Candidates []ReleaseCandidate
	Failures   []SearchProviderFailure
}

type SearchCommandResult struct {
	Search    SearchRun
	Operation Operation
}

type CreateSearchAcquisition struct {
	CandidateID      uuid.UUID
	MediaType        TaskMediaType
	TMDbSeriesID     int64
	SeriesTitle      string
	TMDbMovieID      int64
	MovieTitle       string
	ReleaseYear      int
	MappingProfileID uuid.UUID
	SourceSeason     int
	SourceEpisode    int
	SingleEpisode    bool
	IdempotencyKey   string
	ActorUserID      uuid.UUID
}

type SearchAcquisitionResult struct {
	AcquisitionID uuid.UUID
	DownloadID    uuid.UUID
	Operation     Operation
}

func BuildReleaseCandidateIdentity(provider, title, downloadURI, detailURL string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	title = strings.Join(strings.Fields(title), " ")
	if provider == "" || title == "" {
		return "", errors.New("release candidate requires provider and title")
	}

	if parsed, err := url.Parse(strings.TrimSpace(downloadURI)); err == nil && strings.EqualFold(parsed.Scheme, "magnet") {
		for _, exactTopic := range parsed.Query()["xt"] {
			normalized := strings.ToLower(strings.TrimSpace(exactTopic))
			if strings.HasPrefix(normalized, "urn:btih:") && len(normalized) > len("urn:btih:") {
				return "btih:" + strings.TrimPrefix(normalized, "urn:btih:"), nil
			}
		}
	}
	for _, rawURL := range []string{downloadURI, detailURL} {
		if canonical, ok := canonicalHTTPURL(rawURL); ok {
			return "url:" + canonical, nil
		}
	}

	digest := sha256.Sum256([]byte(provider + "\n" + title))
	return "title:" + hex.EncodeToString(digest[:]), nil
}

func IsDownloadURI(rawURL string) bool {
	return isDownloadURI(rawURL)
}
