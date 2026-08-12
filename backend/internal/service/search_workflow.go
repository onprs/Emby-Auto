package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	appqueue "github.com/onprs/emby-auto/backend/internal/platform/riverqueue"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

type SearchWorkflow struct {
	queries    *db.Queries
	transactor *database.Transactor
	operations *OperationScheduler
}

func NewSearchWorkflow(
	queries *db.Queries,
	transactor *database.Transactor,
	operations *OperationScheduler,
) *SearchWorkflow {
	return &SearchWorkflow{queries: queries, transactor: transactor, operations: operations}
}

func (workflow *SearchWorkflow) CreateSearch(
	ctx context.Context,
	input domain.CreateSearch,
) (domain.SearchCommandResult, error) {
	query := strings.Join(strings.Fields(input.Query), " ")
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if query == "" || len(query) > 512 {
		return domain.SearchCommandResult{}, invalidSearch("query", "must contain between 1 and 512 characters")
	}
	if idempotencyKey == "" || len(idempotencyKey) > 256 {
		return domain.SearchCommandResult{}, invalidSearch("idempotencyKey", "must contain between 1 and 256 characters")
	}
	if input.ActorUserID == uuid.Nil {
		return domain.SearchCommandResult{}, invalidSearch("actorUserId", "must be present")
	}

	commandKey := "search.run:" + input.ActorUserID.String() + ":" + idempotencyKey
	searchID := deterministicResourceID(commandKey)
	result := domain.SearchCommandResult{}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		created := true
		row, err := scope.Queries.CreateSearchRun(ctx, db.CreateSearchRunParams{
			ID:          repository.UUIDToPG(searchID),
			Query:       query,
			RequestedBy: repository.UUIDToPG(input.ActorUserID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			created = false
			row, err = scope.Queries.GetSearchRun(ctx, repository.UUIDToPG(searchID))
		}
		if err != nil {
			return fmt.Errorf("create search run: %w", err)
		}
		if row.Query != query || repository.UUIDFromPG(row.RequestedBy) != input.ActorUserID {
			return idempotencyConflict(idempotencyKey)
		}

		scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindSearchRun,
			ResourceType:   "search_run",
			ResourceID:     searchID,
			IdempotencyKey: commandKey,
			MaxAttempts:    3,
			Timeout:        2 * time.Minute,
			Payload:        map[string]any{},
			ActorUserID:    input.ActorUserID,
		})
		if err != nil {
			return fmt.Errorf("schedule search run: %w", err)
		}
		if created {
			if err := appendSearchEvent(ctx, scope.Queries, searchID, scheduled.Operation.ID, input.ActorUserID, "search.created", map[string]any{
				"query":  query,
				"status": domain.SearchQueued,
			}); err != nil {
				return err
			}
		}
		result = domain.SearchCommandResult{Search: searchRunFromDB(row), Operation: scheduled.Operation}
		return nil
	})
	if err != nil {
		return domain.SearchCommandResult{}, searchCommandError("create search", err)
	}
	return result, nil
}

func (workflow *SearchWorkflow) GetSearch(ctx context.Context, id uuid.UUID) (domain.SearchRun, error) {
	row, err := workflow.queries.GetSearchRun(ctx, repository.UUIDToPG(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SearchRun{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.SearchRun{}, fmt.Errorf("get search run: %w", err)
	}
	candidates, err := workflow.queries.ListReleaseCandidates(ctx, repository.UUIDToPG(id))
	if err != nil {
		return domain.SearchRun{}, fmt.Errorf("list release candidates: %w", err)
	}
	result := searchRunFromDB(row)
	result.Candidates = make([]domain.ReleaseCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		mapped, mapErr := releaseCandidateFromDB(candidate)
		if mapErr != nil {
			return domain.SearchRun{}, mapErr
		}
		result.Candidates = append(result.Candidates, mapped)
	}
	return result, nil
}

func (workflow *SearchWorkflow) BeginSearch(
	ctx context.Context,
	searchID uuid.UUID,
	operationID uuid.UUID,
) (domain.SearchCommand, error) {
	command := domain.SearchCommand{}
	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		row, err := scope.Queries.LockSearchRun(ctx, repository.UUIDToPG(searchID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock search run: %w", err)
		}
		command = domain.SearchCommand{ID: searchID, Query: row.Query, Status: domain.SearchStatus(row.Status)}
		switch command.Status {
		case domain.SearchCompleted, domain.SearchFailed, domain.SearchCancelled:
			return nil
		case domain.SearchRunning:
			return nil
		case domain.SearchQueued:
			updated, err := scope.Queries.MarkSearchRunRunning(ctx, repository.UUIDToPG(searchID))
			if err != nil {
				return fmt.Errorf("mark search run running: %w", err)
			}
			command.Status = domain.SearchStatus(updated.Status)
			return appendSearchEvent(ctx, scope.Queries, searchID, operationID, uuid.Nil, "search.started", map[string]any{
				"status": domain.SearchRunning,
			})
		default:
			return fmt.Errorf("unknown search status %q", command.Status)
		}
	})
	if err != nil {
		return domain.SearchCommand{}, err
	}
	return command, nil
}

func (workflow *SearchWorkflow) CompleteSearch(
	ctx context.Context,
	searchID uuid.UUID,
	operationID uuid.UUID,
	result domain.SearchProviderResult,
) error {
	for index := range result.Candidates {
		candidate := &result.Candidates[index]
		candidate.Provider = strings.ToLower(strings.TrimSpace(candidate.Provider))
		candidate.Title = strings.Join(strings.Fields(candidate.Title), " ")
		candidate.DownloadURI = strings.TrimSpace(candidate.DownloadURI)
		if candidate.Provider == "" || candidate.Title == "" || strings.TrimSpace(candidate.IdentityKey) == "" {
			return fmt.Errorf("release candidate %d requires provider, identity, and title", index)
		}
		if candidate.DownloadURI != "" && !domain.IsDownloadURI(candidate.DownloadURI) {
			return fmt.Errorf("release candidate %d has an unsupported download URI", index)
		}
		if candidate.Seeders != nil && (*candidate.Seeders < 0 || *candidate.Seeders > math.MaxInt32) {
			return fmt.Errorf("release candidate %d seeders exceeds database range", index)
		}
	}

	return workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		locked, err := scope.Queries.LockSearchRun(ctx, repository.UUIDToPG(searchID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock search completion: %w", err)
		}
		if locked.Status == string(domain.SearchCompleted) {
			return nil
		}
		if locked.Status != string(domain.SearchQueued) && locked.Status != string(domain.SearchRunning) {
			return fmt.Errorf("search cannot complete from status %q", locked.Status)
		}

		downloadableCount := 0
		for _, candidate := range result.Candidates {
			payload := candidate.UpstreamPayload
			if payload == nil {
				payload = map[string]any{}
			}
			payloadJSON, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("encode release candidate payload: %w", err)
			}
			if candidate.DownloadURI != "" {
				downloadableCount++
			}
			seeders := (*int32)(nil)
			if candidate.Seeders != nil {
				value := int32(*candidate.Seeders)
				seeders = &value
			}
			if _, err := scope.Queries.UpsertReleaseCandidate(ctx, db.UpsertReleaseCandidateParams{
				ID:              repository.UUIDToPG(uuid.New()),
				SearchRunID:     repository.UUIDToPG(searchID),
				Provider:        candidate.Provider,
				IdentityKey:     candidate.IdentityKey,
				Title:           candidate.Title,
				DownloadUri:     optionalString(candidate.DownloadURI),
				PublishedAt:     optionalTimePointerValue(candidate.PublishedAt),
				SizeBytes:       candidate.SizeBytes,
				Seeders:         seeders,
				UpstreamPayload: payloadJSON,
			}); err != nil {
				return fmt.Errorf("persist release candidate: %w", err)
			}
		}
		if _, err := scope.Queries.MarkSearchRunCompleted(ctx, repository.UUIDToPG(searchID)); err != nil {
			return fmt.Errorf("complete search run: %w", err)
		}
		return appendSearchEvent(ctx, scope.Queries, searchID, operationID, uuid.Nil, "search.completed", map[string]any{
			"status":            domain.SearchCompleted,
			"candidateCount":    len(result.Candidates),
			"downloadableCount": downloadableCount,
			"providerFailures":  result.Failures,
		})
	})
}

func (workflow *SearchWorkflow) CreateAcquisition(
	ctx context.Context,
	input domain.CreateSearchAcquisition,
) (domain.SearchAcquisitionResult, error) {
	if input.MediaType == "" {
		input.MediaType = domain.TaskMediaEpisode
	}
	if err := validateSearchAcquisition(input); err != nil {
		return domain.SearchAcquisitionResult{}, err
	}
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	commandKey := "acquisition.create:" + input.ActorUserID.String() + ":" + idempotencyKey
	requestedAcquisitionID := deterministicResourceID(commandKey)
	requestedDownloadID := deterministicResourceID("download:" + requestedAcquisitionID.String())
	result := domain.SearchAcquisitionResult{}

	err := workflow.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		candidate, err := scope.Queries.GetReleaseCandidate(ctx, repository.UUIDToPG(input.CandidateID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load release candidate: %w", err)
		}
		if candidate.DownloadUri == nil || !domain.IsDownloadURI(*candidate.DownloadUri) {
			return NewError(
				"candidate_not_downloadable",
				"the release candidate has no supported download URI",
				ErrStateConflict,
				map[string]any{"candidateId": input.CandidateID},
			)
		}

		var media db.MediaSeries
		switch input.MediaType {
		case domain.TaskMediaMovie:
			releaseYear := int32(input.ReleaseYear)
			media, err = scope.Queries.UpsertSearchMediaMovie(ctx, db.UpsertSearchMediaMovieParams{
				ID: repository.UUIDToPG(uuid.New()), TmdbMovieID: &input.TMDbMovieID,
				Title: strings.TrimSpace(input.MovieTitle), ReleaseYear: &releaseYear,
			})
		case domain.TaskMediaEpisode:
			tmdbID := input.TMDbSeriesID
			media, err = scope.Queries.UpsertSearchMediaSeries(ctx, db.UpsertSearchMediaSeriesParams{
				ID: repository.UUIDToPG(uuid.New()), TmdbSeriesID: &tmdbID, Title: strings.TrimSpace(input.SeriesTitle),
			})
		default:
			return invalidSearchAcquisition("mediaType", "must be episode or movie")
		}
		if err != nil {
			return fmt.Errorf("upsert search media metadata: %w", err)
		}
		payloadJSON, err := json.Marshal(map[string]any{
			"candidateId": input.CandidateID, "searchRunId": repository.UUIDFromPG(candidate.SearchRunID),
			"mediaType": input.MediaType, "sourceSeason": input.SourceSeason,
			"sourceEpisode": input.SourceEpisode, "singleEpisode": input.SingleEpisode,
		})
		if err != nil {
			return fmt.Errorf("encode search acquisition payload: %w", err)
		}

		created := true
		acquisition, err := scope.Queries.CreateSearchAcquisition(ctx, db.CreateSearchAcquisitionParams{
			ID:                 repository.UUIDToPG(requestedAcquisitionID),
			SeriesID:           media.ID,
			MappingProfileID:   nullableUUID(input.MappingProfileID),
			ReleaseCandidateID: candidate.ID,
			SourcePayload:      payloadJSON,
			CreatedBy:          repository.UUIDToPG(input.ActorUserID),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			created = false
			acquisition, err = scope.Queries.GetSearchAcquisitionByCandidate(ctx, candidate.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return idempotencyConflict(idempotencyKey)
			}
		}
		if err != nil {
			return fmt.Errorf("create search acquisition: %w", err)
		}
		if acquisition.SeriesID != media.ID || acquisition.MappingProfileID != nullableUUID(input.MappingProfileID) || !jsonValuesEqual(acquisition.SourcePayload, payloadJSON) {
			return NewError(
				"state_conflict",
				"the release candidate was already selected with different acquisition settings",
				ErrStateConflict,
				map[string]any{"candidateId": input.CandidateID},
			)
		}

		download, err := scope.Queries.CreateSearchDownload(ctx, db.CreateSearchDownloadParams{
			ID:            repository.UUIDToPG(requestedDownloadID),
			AcquisitionID: acquisition.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			download, err = scope.Queries.GetSearchAcquisitionDownload(ctx, acquisition.ID)
		}
		if err != nil {
			return fmt.Errorf("create search download: %w", err)
		}
		downloadID := repository.UUIDFromPG(download.ID)
		defaultSeason, defaultEpisode, singleEpisode := input.SourceSeason, input.SourceEpisode, input.SingleEpisode
		if input.MediaType == domain.TaskMediaMovie {
			defaultSeason, defaultEpisode, singleEpisode = 1, 1, true
		}
		scheduled, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
			Kind:           appqueue.KindDownloadEnqueue,
			ResourceType:   "download",
			ResourceID:     downloadID,
			IdempotencyKey: "download.enqueue:" + downloadID.String(),
			MaxAttempts:    5,
			Timeout:        2 * time.Minute,
			Payload: map[string]any{
				"defaultSeason": defaultSeason, "defaultEpisode": defaultEpisode,
				"singleEpisode": singleEpisode, "mediaType": input.MediaType,
			},
			ActorUserID: input.ActorUserID,
		})
		if err != nil {
			return fmt.Errorf("schedule search download: %w", err)
		}
		acquisitionID := repository.UUIDFromPG(acquisition.ID)
		if input.MediaType == domain.TaskMediaEpisode {
			if _, err := workflow.operations.ScheduleInTx(ctx, scope, ScheduleOperationRequest{
				Kind: appqueue.KindTMDbSync, ResourceType: "media_series", ResourceID: repository.UUIDFromPG(media.ID),
				IdempotencyKey: "tmdb.sync:acquisition:" + acquisitionID.String(), MaxAttempts: 4, Timeout: 10 * time.Minute,
				Payload: map[string]any{"tmdbSeriesId": input.TMDbSeriesID}, ActorUserID: input.ActorUserID,
			}); err != nil {
				return fmt.Errorf("schedule acquisition TMDb sync: %w", err)
			}
		}
		if created {
			if err := appendAcquisitionEvent(ctx, scope.Queries, acquisitionID, scheduled.Operation.ID, input.ActorUserID, "acquisition.created", map[string]any{
				"candidateId": input.CandidateID,
				"downloadId":  downloadID,
				"sourceKind":  "search", "mediaType": input.MediaType,
			}); err != nil {
				return err
			}
		}
		result = domain.SearchAcquisitionResult{
			AcquisitionID: acquisitionID,
			DownloadID:    downloadID,
			Operation:     scheduled.Operation,
		}
		return nil
	})
	if err != nil {
		return domain.SearchAcquisitionResult{}, searchCommandError("create search acquisition", err)
	}
	return result, nil
}

func validateSearchAcquisition(input domain.CreateSearchAcquisition) error {
	if input.CandidateID == uuid.Nil {
		return invalidSearchAcquisition("candidateId", "must be present")
	}
	switch input.MediaType {
	case domain.TaskMediaEpisode:
		if input.TMDbSeriesID <= 0 {
			return invalidSearchAcquisition("tmdbSeriesId", "must be positive for an episode acquisition")
		}
		if strings.TrimSpace(input.SeriesTitle) == "" || len(strings.TrimSpace(input.SeriesTitle)) > 512 {
			return invalidSearchAcquisition("seriesTitle", "must contain between 1 and 512 characters")
		}
		if input.SourceSeason <= 0 || input.SourceSeason > math.MaxInt32 {
			return invalidSearchAcquisition("sourceSeason", "must be between 1 and 2147483647")
		}
		if input.SourceEpisode < 0 || input.SourceEpisode > math.MaxInt32 || (input.SingleEpisode && input.SourceEpisode == 0) {
			return invalidSearchAcquisition("sourceEpisode", "must be positive for a single episode and nonnegative otherwise")
		}
	case domain.TaskMediaMovie:
		if input.TMDbMovieID <= 0 {
			return invalidSearchAcquisition("tmdbMovieId", "must be positive for a movie acquisition")
		}
		if strings.TrimSpace(input.MovieTitle) == "" || len(strings.TrimSpace(input.MovieTitle)) > 512 {
			return invalidSearchAcquisition("movieTitle", "must contain between 1 and 512 characters")
		}
		if input.ReleaseYear < 1870 || input.ReleaseYear > 9999 {
			return invalidSearchAcquisition("releaseYear", "must be between 1870 and 9999")
		}
		if input.MappingProfileID != uuid.Nil {
			return invalidSearchAcquisition("mappingProfileId", "must be omitted for a movie acquisition")
		}
	default:
		return invalidSearchAcquisition("mediaType", "must be episode or movie")
	}
	if key := strings.TrimSpace(input.IdempotencyKey); key == "" || len(key) > 256 {
		return invalidSearchAcquisition("idempotencyKey", "must contain between 1 and 256 characters")
	}
	if input.ActorUserID == uuid.Nil {
		return invalidSearchAcquisition("actorUserId", "must be present")
	}
	return nil
}

func invalidSearch(field, reason string) *Error {
	return NewError("invalid_search", "the search command is invalid", ErrInvalidInput, map[string]any{"field": field, "reason": reason})
}

func invalidSearchAcquisition(field, reason string) *Error {
	return NewError("invalid_acquisition", "the acquisition command is invalid", ErrInvalidInput, map[string]any{"field": field, "reason": reason})
}

func idempotencyConflict(key string) *Error {
	return NewError(
		"idempotency_conflict",
		"the idempotency key was already used for a different command",
		ErrStateConflict,
		map[string]any{"idempotencyKey": key},
	)
}

func searchCommandError(action string, err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrNotFound
	}
	var serviceErr *Error
	if errors.As(err, &serviceErr) {
		return serviceErr
	}
	return fmt.Errorf("%s: %w", action, err)
}

func deterministicResourceID(key string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://emby-auto.local/commands/"+key))
}

func searchRunFromDB(row db.SearchRun) domain.SearchRun {
	return domain.SearchRun{
		ID:           repository.UUIDFromPG(row.ID),
		Query:        row.Query,
		Status:       domain.SearchStatus(row.Status),
		RequestedBy:  repository.UUIDFromPG(row.RequestedBy),
		ErrorCode:    stringValue(row.ErrorCode),
		ErrorMessage: stringValue(row.ErrorMessage),
		StartedAt:    optionalTimePointer(row.StartedAt),
		CompletedAt:  optionalTimePointer(row.CompletedAt),
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
	}
}

func releaseCandidateFromDB(row db.ReleaseCandidate) (domain.ReleaseCandidate, error) {
	payload := map[string]any{}
	if len(row.UpstreamPayload) > 0 {
		if err := json.Unmarshal(row.UpstreamPayload, &payload); err != nil {
			return domain.ReleaseCandidate{}, fmt.Errorf("decode release candidate payload: %w", err)
		}
	}
	seeders := (*int)(nil)
	if row.Seeders != nil {
		value := int(*row.Seeders)
		seeders = &value
	}
	return domain.ReleaseCandidate{
		ID:              repository.UUIDFromPG(row.ID),
		SearchRunID:     repository.UUIDFromPG(row.SearchRunID),
		Provider:        row.Provider,
		IdentityKey:     row.IdentityKey,
		Title:           row.Title,
		DownloadURI:     stringValue(row.DownloadUri),
		PublishedAt:     optionalTimePointer(row.PublishedAt),
		SizeBytes:       row.SizeBytes,
		Seeders:         seeders,
		UpstreamPayload: payload,
		CreatedAt:       row.CreatedAt.Time,
	}, nil
}

func optionalTimePointerValue(value *time.Time) pgtype.Timestamptz {
	if value == nil || value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func appendSearchEvent(
	ctx context.Context,
	queries *db.Queries,
	searchID uuid.UUID,
	operationID uuid.UUID,
	actorUserID uuid.UUID,
	topic string,
	data map[string]any,
) error {
	return appendResourceEvent(ctx, queries, "search_run", searchID, operationID, actorUserID, topic, data)
}

func appendAcquisitionEvent(
	ctx context.Context,
	queries *db.Queries,
	acquisitionID uuid.UUID,
	operationID uuid.UUID,
	actorUserID uuid.UUID,
	topic string,
	data map[string]any,
) error {
	return appendResourceEvent(ctx, queries, "acquisition", acquisitionID, operationID, actorUserID, topic, data)
}

func appendResourceEvent(
	ctx context.Context,
	queries *db.Queries,
	resourceType string,
	resourceID uuid.UUID,
	operationID uuid.UUID,
	actorUserID uuid.UUID,
	topic string,
	data map[string]any,
) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", resourceType, err)
	}
	if _, err := queries.AppendEvent(ctx, db.AppendEventParams{
		ID:           repository.UUIDToPG(uuid.New()),
		Topic:        topic,
		ResourceType: &resourceType,
		ResourceID:   repository.UUIDToPG(resourceID),
		OperationID:  nullableUUID(operationID),
		ActorUserID:  nullableUUID(actorUserID),
		Data:         encoded,
	}); err != nil {
		return fmt.Errorf("append %s event: %w", resourceType, err)
	}
	return nil
}
