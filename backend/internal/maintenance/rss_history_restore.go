package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

var restoreBTIHPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

type RestoreCompletedRSSHistoryRequest struct {
	Snapshot             RSSHistorySnapshot
	MappingProfileID     uuid.UUID
	ExpectedEntryCount   int
	ExpectedFinalEpisode int32
}

type RestoreCompletedRSSHistoryResult struct {
	SubscriptionID uuid.UUID
	EntryCount     int
}

type RSSHistoryRestorer struct {
	transactor *database.Transactor
}

func NewRSSHistoryRestorer(transactor *database.Transactor) *RSSHistoryRestorer {
	return &RSSHistoryRestorer{transactor: transactor}
}

func (restorer *RSSHistoryRestorer) Restore(
	ctx context.Context,
	request RestoreCompletedRSSHistoryRequest,
) (RestoreCompletedRSSHistoryResult, error) {
	if restorer == nil || restorer.transactor == nil {
		return RestoreCompletedRSSHistoryResult{}, fmt.Errorf("RSS history restorer is not configured")
	}
	if err := ValidateRestoreRequest(request); err != nil {
		return RestoreCompletedRSSHistoryResult{}, err
	}

	subscription := request.Snapshot.Subscription
	entries := append([]RSSEntryHistory(nil), request.Snapshot.Entries...)
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].SourceEpisode != entries[right].SourceEpisode {
			return entries[left].SourceEpisode < entries[right].SourceEpisode
		}
		return entries[left].ID.String() < entries[right].ID.String()
	})
	result := RestoreCompletedRSSHistoryResult{SubscriptionID: subscription.ID, EntryCount: len(entries)}
	err := restorer.transactor.WithinTx(ctx, pgx.TxOptions{}, func(scope database.TxScope) error {
		if _, err := scope.Queries.GetRSSPollCommand(ctx, repository.UUIDToPG(subscription.ID)); err == nil {
			return fmt.Errorf("RSS subscription %s already exists", subscription.ID)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check existing RSS subscription: %w", err)
		}

		activeOperations, err := scope.Queries.CountSubscriptionDeletionActiveOperations(ctx, db.CountSubscriptionDeletionActiveOperationsParams{
			OperationID:    repository.UUIDToPG(uuid.Nil),
			SubscriptionID: repository.UUIDToPG(subscription.ID),
		})
		if err != nil {
			return fmt.Errorf("check RSS subscription operations: %w", err)
		}
		if activeOperations != 0 {
			return fmt.Errorf("RSS subscription %s has %d active operations", subscription.ID, activeOperations)
		}

		if _, err := scope.Queries.GetMediaSeriesByID(ctx, repository.UUIDToPG(subscription.SeriesID)); errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("RSS history series %s does not exist", subscription.SeriesID)
		} else if err != nil {
			return fmt.Errorf("load RSS history series: %w", err)
		}
		sourceSeason := subscription.SourceSeason
		if _, err := scope.Queries.GetCompatibleActiveMappingProfile(ctx, db.GetCompatibleActiveMappingProfileParams{
			ProfileID:    repository.UUIDToPG(request.MappingProfileID),
			SeriesID:     repository.UUIDToPG(subscription.SeriesID),
			SourceSeason: &sourceSeason,
		}); errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("mapping profile %s is not active and compatible with the restored RSS history", request.MappingProfileID)
		} else if err != nil {
			return fmt.Errorf("validate RSS history mapping profile: %w", err)
		}

		if _, err := scope.Queries.RestoreRSSSubscriptionHistory(ctx, db.RestoreRSSSubscriptionHistoryParams{
			ID:                        repository.UUIDToPG(subscription.ID),
			SeriesID:                  repository.UUIDToPG(subscription.SeriesID),
			MappingProfileID:          repository.UUIDToPG(request.MappingProfileID),
			Name:                      subscription.Name,
			FeedUrl:                   subscription.FeedURL,
			AutoReview:                subscription.AutoReview,
			CleanupSourceOnCompletion: subscription.CleanupSourceOnCompletion,
			PollIntervalSeconds:       subscription.PollIntervalSeconds,
			LastPolledAt:              nullableTimestamp(subscription.LastPolledAt),
			Version:                   subscription.Version,
			SourceSeason:              subscription.SourceSeason,
			CreatedAt:                 pgtype.Timestamptz{Time: subscription.CreatedAt, Valid: true},
		}); err != nil {
			return sanitizedDatabaseError("restore completed RSS subscription", err)
		}

		for _, entry := range entries {
			sourceEpisode := entry.SourceEpisode
			sourceSeason := entry.SourceSeason
			if _, err := scope.Queries.RestoreCompletedRSSEntry(ctx, db.RestoreCompletedRSSEntryParams{
				ID:              repository.UUIDToPG(entry.ID),
				SubscriptionID:  repository.UUIDToPG(subscription.ID),
				IdentityKey:     entry.IdentityKey,
				Guid:            entry.GUID,
				Btih:            entry.BTIH,
				CanonicalUrl:    entry.CanonicalURL,
				Title:           entry.Title,
				PublishedAt:     nullableTimestamp(entry.PublishedAt),
				EnqueueAttempts: entry.EnqueueAttempts,
				UpstreamPayload: entry.UpstreamPayload,
				DiscoveredAt:    pgtype.Timestamptz{Time: entry.DiscoveredAt, Valid: true},
				DownloadUri:     &entry.DownloadURI,
				SourceSeason:    &sourceSeason,
				SourceEpisode:   &sourceEpisode,
				DuplicateCount:  entry.DuplicateCount,
			}); err != nil {
				return sanitizedDatabaseError(fmt.Sprintf("restore completed RSS entry for source episode %d", entry.SourceEpisode), err)
			}
		}

		eventData, err := json.Marshal(map[string]any{
			"entryCount":         len(entries),
			"finalSourceEpisode": request.ExpectedFinalEpisode,
			"sourceSeason":       subscription.SourceSeason,
			"summary":            "已从删除前备份恢复 RSS 去重历史，逐集入库证据需独立核验",
		})
		if err != nil {
			return fmt.Errorf("encode RSS history restoration event: %w", err)
		}
		resourceType := "rss_subscription"
		if _, err := scope.Queries.AppendEvent(ctx, db.AppendEventParams{
			ID:           repository.UUIDToPG(uuid.New()),
			Topic:        "rss.subscription.history_restored",
			ResourceType: &resourceType,
			ResourceID:   repository.UUIDToPG(subscription.ID),
			OperationID:  pgtype.UUID{},
			ActorUserID:  pgtype.UUID{},
			Data:         eventData,
		}); err != nil {
			return fmt.Errorf("append RSS history restoration event: %w", err)
		}
		return nil
	})
	if err != nil {
		return RestoreCompletedRSSHistoryResult{}, err
	}
	return result, nil
}

// ValidateRestoreRequest verifies that a backup contains one complete,
// duplicate-free source season before any database transaction starts.
func ValidateRestoreRequest(request RestoreCompletedRSSHistoryRequest) error {
	subscription := request.Snapshot.Subscription
	if subscription.ID == uuid.Nil || subscription.SeriesID == uuid.Nil {
		return fmt.Errorf("RSS subscription and series IDs are required")
	}
	if request.MappingProfileID == uuid.Nil {
		return fmt.Errorf("an active mapping profile ID is required")
	}
	if strings.TrimSpace(subscription.Name) == "" || strings.TrimSpace(subscription.FeedURL) == "" {
		return fmt.Errorf("RSS subscription name and feed URL are required")
	}
	if subscription.PollIntervalSeconds < 60 || subscription.PollIntervalSeconds > 86400 {
		return fmt.Errorf("RSS poll interval must be between 60 and 86400 seconds")
	}
	if subscription.Version <= 0 || subscription.SourceSeason <= 0 || subscription.CreatedAt.IsZero() {
		return fmt.Errorf("RSS subscription version, source season, and creation time must be valid")
	}
	if request.ExpectedEntryCount <= 0 || len(request.Snapshot.Entries) != request.ExpectedEntryCount {
		return fmt.Errorf("RSS history entry count is %d, expected %d", len(request.Snapshot.Entries), request.ExpectedEntryCount)
	}
	if request.ExpectedFinalEpisode <= 0 || int(request.ExpectedFinalEpisode) != request.ExpectedEntryCount {
		return fmt.Errorf("expected final source episode must equal the expected entry count")
	}

	entryIDs := make(map[uuid.UUID]struct{}, len(request.Snapshot.Entries))
	identities := make(map[string]struct{}, len(request.Snapshot.Entries))
	btihs := make(map[string]struct{}, len(request.Snapshot.Entries))
	episodes := make(map[int32]struct{}, len(request.Snapshot.Entries))
	for _, entry := range request.Snapshot.Entries {
		if entry.ID == uuid.Nil {
			return fmt.Errorf("RSS history contains an entry without an ID")
		}
		if _, exists := entryIDs[entry.ID]; exists {
			return fmt.Errorf("RSS history contains duplicate entry ID %s", entry.ID)
		}
		entryIDs[entry.ID] = struct{}{}

		identity := strings.TrimSpace(entry.IdentityKey)
		if identity == "" || strings.TrimSpace(entry.Title) == "" || strings.TrimSpace(entry.DownloadURI) == "" {
			return fmt.Errorf("RSS history entry %s has blank identity, title, or download URI", entry.ID)
		}
		if _, exists := identities[identity]; exists {
			return fmt.Errorf("RSS history contains duplicate entry identity")
		}
		identities[identity] = struct{}{}
		if entry.BTIH != nil {
			btih := strings.ToLower(strings.TrimSpace(*entry.BTIH))
			if !restoreBTIHPattern.MatchString(btih) {
				return fmt.Errorf("RSS history entry %s has an invalid BTIH", entry.ID)
			}
			if _, exists := btihs[btih]; exists {
				return fmt.Errorf("RSS history contains duplicate BTIH")
			}
			btihs[btih] = struct{}{}
		}
		if entry.SourceSeason != subscription.SourceSeason || entry.SourceEpisode <= 0 || entry.SourceEpisode > request.ExpectedFinalEpisode {
			return fmt.Errorf("RSS history entry %s has an unexpected source coordinate", entry.ID)
		}
		if _, exists := episodes[entry.SourceEpisode]; exists {
			return fmt.Errorf("RSS history contains duplicate source episode %d", entry.SourceEpisode)
		}
		episodes[entry.SourceEpisode] = struct{}{}
		if entry.EnqueueAttempts < 0 || entry.DuplicateCount < 0 || entry.DiscoveredAt.IsZero() {
			return fmt.Errorf("RSS history entry %s has invalid counters or discovery time", entry.ID)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(entry.UpstreamPayload, &payload); err != nil || payload == nil {
			return fmt.Errorf("RSS history entry %s payload must be a JSON object", entry.ID)
		}
	}
	for episode := int32(1); episode <= request.ExpectedFinalEpisode; episode++ {
		if _, exists := episodes[episode]; !exists {
			return fmt.Errorf("RSS history is missing source episode %d", episode)
		}
	}
	return nil
}

func sanitizedDatabaseError(action string, err error) error {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		return fmt.Errorf("%s failed (SQLSTATE %s, constraint %s)", action, pgError.Code, pgError.ConstraintName)
	}
	return fmt.Errorf("%s failed: %w", action, err)
}

func nullableTimestamp(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}
