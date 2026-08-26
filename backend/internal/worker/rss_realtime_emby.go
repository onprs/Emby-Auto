package worker

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/platform/emby"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type RSSRealtimeEmbyClient interface {
	SeriesEpisodesByTMDb(context.Context, int64) ([]domain.EmbyLibraryItemCatalog, error)
}

type RSSRealtimeEmbyClientFactory func(emby.ClientOptions) (RSSRealtimeEmbyClient, error)

type rssRealtimeTarget struct {
	source          domain.EpisodeCoordinate
	targetEpisodeID uuid.UUID
	target          domain.EpisodeCoordinate
	tmdbEpisodeID   *int64
	tmdbSeriesID    int64
}

type RSSRealtimeEmbyVerifier struct {
	configuration DownloadConfiguration
	queries       *db.Queries
	transactor    *database.Transactor
	newClient     RSSRealtimeEmbyClientFactory
	now           func() time.Time
}

func NewRSSRealtimeEmbyVerifier(
	configuration DownloadConfiguration,
	queries *db.Queries,
	transactor *database.Transactor,
	newClient RSSRealtimeEmbyClientFactory,
) *RSSRealtimeEmbyVerifier {
	return &RSSRealtimeEmbyVerifier{
		configuration: configuration,
		queries:       queries,
		transactor:    transactor,
		newClient:     newClient,
		now:           time.Now,
	}
}

func (verifier *RSSRealtimeEmbyVerifier) VerifySubscription(ctx context.Context, subscriptionID uuid.UUID) (uuid.UUID, error) {
	if subscriptionID == uuid.Nil {
		return uuid.Nil, realtimeVerificationError("rss_realtime_scope_invalid", "the RSS subscription scope is invalid", false, nil)
	}
	rows, err := verifier.queries.ListRSSMappedRealtimeTargets(ctx, repository.UUIDToPG(subscriptionID))
	if err != nil {
		return uuid.Nil, realtimeVerificationError("rss_realtime_storage_unavailable", "mapped RSS targets are unavailable", true, err)
	}
	if len(rows) == 0 {
		return uuid.Nil, realtimeVerificationError("rss_realtime_mapping_unavailable", "the RSS subscription does not have mapped targets", true, nil)
	}
	targets := make([]rssRealtimeTarget, 0, len(rows))
	for _, row := range rows {
		target, err := realtimeTargetFromRow(
			row.SourceSeason, row.SourceEpisode, row.TargetEpisodeID,
			row.TargetSeason, row.TargetEpisode, row.TmdbEpisodeID, row.TmdbSeriesID,
		)
		if err != nil {
			return uuid.Nil, err
		}
		targets = append(targets, target)
	}
	return verifier.verify(ctx, targets)
}

func (verifier *RSSRealtimeEmbyVerifier) VerifyEntry(ctx context.Context, entryID uuid.UUID) (uuid.UUID, error) {
	if entryID == uuid.Nil {
		return uuid.Nil, realtimeVerificationError("rss_realtime_scope_invalid", "the RSS entry scope is invalid", false, nil)
	}
	row, err := verifier.queries.GetRSSEntryMappedRealtimeTarget(ctx, repository.UUIDToPG(entryID))
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, realtimeVerificationError(
			"rss_realtime_mapping_unavailable", "the RSS entry does not have a mapped target", true, nil,
		)
	}
	if err != nil {
		return uuid.Nil, realtimeVerificationError("rss_realtime_storage_unavailable", "the mapped RSS target is unavailable", true, err)
	}
	target, err := realtimeTargetFromRow(
		row.SourceSeason, row.SourceEpisode, row.TargetEpisodeID,
		row.TargetSeason, row.TargetEpisode, row.TmdbEpisodeID, row.TmdbSeriesID,
	)
	if err != nil {
		return uuid.Nil, err
	}
	return verifier.verify(ctx, []rssRealtimeTarget{target})
}

func (verifier *RSSRealtimeEmbyVerifier) VerifyCoordinates(
	ctx context.Context,
	subscriptionID uuid.UUID,
	coordinates []domain.EpisodeCoordinate,
) (uuid.UUID, error) {
	if len(coordinates) == 0 {
		return uuid.Nil, nil
	}
	rows, err := verifier.queries.ListRSSMappedRealtimeTargets(ctx, repository.UUIDToPG(subscriptionID))
	if err != nil {
		return uuid.Nil, realtimeVerificationError("rss_realtime_storage_unavailable", "mapped RSS targets are unavailable", true, err)
	}
	requested := make(map[domain.EpisodeCoordinate]struct{}, len(coordinates))
	for _, coordinate := range coordinates {
		if coordinate.Season <= 0 || coordinate.Episode <= 0 {
			return uuid.Nil, realtimeVerificationError("rss_realtime_scope_invalid", "an RSS source coordinate is invalid", false, nil)
		}
		requested[coordinate] = struct{}{}
	}
	targets := make([]rssRealtimeTarget, 0, len(requested))
	for _, row := range rows {
		source := domain.EpisodeCoordinate{Season: int(row.SourceSeason), Episode: int(row.SourceEpisode)}
		if _, ok := requested[source]; !ok {
			continue
		}
		target, err := realtimeTargetFromRow(
			row.SourceSeason, row.SourceEpisode, row.TargetEpisodeID,
			row.TargetSeason, row.TargetEpisode, row.TmdbEpisodeID, row.TmdbSeriesID,
		)
		if err != nil {
			return uuid.Nil, err
		}
		targets = append(targets, target)
	}
	if len(targets) != len(requested) {
		return uuid.Nil, realtimeVerificationError(
			"rss_realtime_mapping_unavailable", "one or more RSS source coordinates do not have mapped targets", true, nil,
		)
	}
	return verifier.verify(ctx, targets)
}

func (verifier *RSSRealtimeEmbyVerifier) verify(ctx context.Context, targets []rssRealtimeTarget) (uuid.UUID, error) {
	if len(targets) == 0 {
		return uuid.Nil, nil
	}
	if verifier == nil || verifier.configuration == nil || verifier.queries == nil || verifier.transactor == nil || verifier.newClient == nil {
		return uuid.Nil, realtimeVerificationError("rss_realtime_not_configured", "real-time Emby target verification is unavailable", false, nil)
	}
	seriesID := targets[0].tmdbSeriesID
	for _, target := range targets[1:] {
		if target.tmdbSeriesID != seriesID {
			return uuid.Nil, realtimeVerificationError("rss_realtime_scope_invalid", "mapped RSS targets span multiple TMDb series", false, nil)
		}
	}
	configuration, err := verifier.configuration.Load(ctx)
	if err != nil {
		return uuid.Nil, realtimeVerificationError("configuration_unavailable", "runtime configuration is unavailable", true, err)
	}
	if strings.TrimSpace(configuration.Settings.Emby.URL) == "" {
		return uuid.Nil, realtimeVerificationError("emby_not_configured", "Emby is not configured", false, nil)
	}
	apiKey, err := verifier.configuration.ResolveSecret(ctx, domain.SecretEmbyAPIKey)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return uuid.Nil, realtimeVerificationError("emby_not_configured", "the Emby API key is not configured", false, err)
		}
		return uuid.Nil, realtimeVerificationError("configuration_unavailable", "the Emby API key is unavailable", true, err)
	}
	client, err := verifier.newClient(emby.ClientOptions{
		BaseURL:        configuration.Settings.Emby.URL,
		APIKey:         apiKey,
		RequestTimeout: embyRequestTimeout,
	})
	if err != nil {
		return uuid.Nil, realtimeVerificationError("emby_configuration_invalid", "the Emby configuration is invalid", false, err)
	}
	episodes, err := client.SeriesEpisodesByTMDb(ctx, seriesID)
	if err != nil {
		return uuid.Nil, classifyRealtimeEmbyRequestError(err)
	}
	matches := matchRealtimeTargets(targets, episodes)
	checkID := uuid.New()
	checkedAt := verifier.now().UTC()
	err = verifier.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		if err := scope.Queries.DeleteExpiredRSSRealtimeTargetChecks(ctx); err != nil {
			return err
		}
		for index, target := range targets {
			match := matches[index]
			params := db.UpsertRSSRealtimeTargetCheckParams{
				TargetEpisodeID: repository.UUIDToPG(target.targetEpisodeID),
				CheckID:         repository.UUIDToPG(checkID),
				Present:         match.present,
				MatchSource:     match.source,
				CheckedAt:       pgtype.Timestamptz{Time: checkedAt, Valid: true},
			}
			if err := scope.Queries.UpsertRSSRealtimeTargetCheck(ctx, params); err != nil {
				return err
			}
			if err := scope.Queries.RefreshRSSEmbyCatalogFulfillmentsForRealtimeTarget(
				ctx,
				db.RefreshRSSEmbyCatalogFulfillmentsForRealtimeTargetParams{
					Present:         params.Present,
					CheckedAt:       params.CheckedAt,
					TargetEpisodeID: params.TargetEpisodeID,
				},
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, realtimeVerificationError("rss_realtime_storage_unavailable", "real-time Emby checks could not be recorded", true, err)
	}
	return checkID, nil
}

type realtimeTargetMatch struct {
	present bool
	source  string
}

func matchRealtimeTargets(targets []rssRealtimeTarget, episodes []domain.EmbyLibraryItemCatalog) []realtimeTargetMatch {
	matches := make([]realtimeTargetMatch, len(targets))
	for index := range matches {
		matches[index].source = "absent"
	}
	for index, target := range targets {
		for _, item := range episodes {
			if item.ItemType != "Episode" || strings.TrimSpace(item.Path) == "" {
				continue
			}
			if target.tmdbEpisodeID != nil {
				if providerIDMatches(item.ProviderIDs, "tmdb", *target.tmdbEpisodeID) ||
					providerIDMatches(item.ProviderIDs, "themoviedb", *target.tmdbEpisodeID) {
					matches[index] = realtimeTargetMatch{present: true, source: "tmdb_episode"}
					break
				}
				if !hasTMDbProviderID(item.ProviderIDs) && item.SeasonNumber != nil && item.EpisodeNumber != nil &&
					*item.SeasonNumber == target.target.Season && *item.EpisodeNumber == target.target.Episode {
					matches[index] = realtimeTargetMatch{present: true, source: "target_coordinate"}
				}
				continue
			}
			if item.SeasonNumber != nil && item.EpisodeNumber != nil &&
				*item.SeasonNumber == target.target.Season && *item.EpisodeNumber == target.target.Episode {
				matches[index] = realtimeTargetMatch{present: true, source: "target_coordinate"}
			}
		}
	}
	return matches
}

func hasTMDbProviderID(providerIDs map[string]string) bool {
	for key, value := range providerIDs {
		if (strings.EqualFold(strings.TrimSpace(key), "tmdb") || strings.EqualFold(strings.TrimSpace(key), "themoviedb")) &&
			strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func providerIDMatches(providerIDs map[string]string, provider string, expected int64) bool {
	want := strconv.FormatInt(expected, 10)
	for key, value := range providerIDs {
		if strings.EqualFold(strings.TrimSpace(key), provider) && strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

func realtimeTargetFromRow(
	sourceSeason, sourceEpisode int32,
	targetEpisodeIDPG pgtype.UUID,
	targetSeason, targetEpisode int32,
	tmdbEpisodeID, tmdbSeriesID *int64,
) (rssRealtimeTarget, error) {
	targetEpisodeID := repository.UUIDFromPG(targetEpisodeIDPG)
	if sourceSeason <= 0 || sourceEpisode <= 0 || targetEpisodeID == uuid.Nil || targetSeason <= 0 || targetEpisode <= 0 || tmdbSeriesID == nil || *tmdbSeriesID <= 0 {
		return rssRealtimeTarget{}, realtimeVerificationError("rss_realtime_mapping_invalid", "an RSS target mapping is incomplete", false, nil)
	}
	return rssRealtimeTarget{
		source:          domain.EpisodeCoordinate{Season: int(sourceSeason), Episode: int(sourceEpisode)},
		targetEpisodeID: targetEpisodeID,
		target:          domain.EpisodeCoordinate{Season: int(targetSeason), Episode: int(targetEpisode)},
		tmdbEpisodeID:   tmdbEpisodeID,
		tmdbSeriesID:    *tmdbSeriesID,
	}, nil
}

func classifyRealtimeEmbyRequestError(err error) error {
	var httpErr *emby.HTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden:
			return realtimeVerificationError("emby_authentication_failed", "Emby rejected the configured API key", false, err)
		case httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= http.StatusInternalServerError:
			return realtimeVerificationError("emby_realtime_request_failed", "real-time Emby target verification failed", true, err)
		case httpErr.StatusCode >= http.StatusBadRequest:
			return realtimeVerificationError("emby_realtime_request_rejected", "Emby rejected the real-time target query", false, err)
		}
	}
	return realtimeVerificationError("emby_realtime_request_failed", "real-time Emby target verification failed", true, err)
}

func realtimeVerificationError(code, message string, retryable bool, cause error) error {
	return &service.RSSRealtimeVerificationError{Code: code, Message: message, Retryable: retryable, Cause: cause}
}

var _ service.RSSRealtimeTargetVerifier = (*RSSRealtimeEmbyVerifier)(nil)
