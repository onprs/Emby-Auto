//go:build integration

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

func addTargetSearchCandidate(t *testing.T, fixture rssTargetFixture, suffix string) uuid.UUID {
	t.Helper()
	searchID, candidateID := uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO search_runs (id, query, status, requested_by)
VALUES ($1, $2, 'completed', $3)`, searchID, "target reservation "+suffix, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO release_candidates (id, search_run_id, provider, identity_key, title, download_uri)
VALUES ($1, $2, 'fixture', $3, $4, $5)`,
		candidateID,
		searchID,
		"target-reservation:"+suffix,
		"Target Occupancy Series "+suffix,
		"https://example.test/target-reservation-"+suffix+".torrent",
	); err != nil {
		t.Fatal(err)
	}
	return candidateID
}

func targetSearchAcquisitionInput(fixture rssTargetFixture, candidateID uuid.UUID, sourceEpisode int, key string) domain.CreateSearchAcquisition {
	return domain.CreateSearchAcquisition{
		CandidateID: candidateID, TMDbSeriesID: 9100, SeriesTitle: "Target Occupancy Series",
		SourceSeason: 1, SourceEpisode: sourceEpisode, SingleEpisode: true,
		MappingProfileID: fixture.profileID, IdempotencyKey: key, ActorUserID: fixture.actorID,
	}
}

func requireMappingTargetOccupied(t *testing.T, err error) {
	t.Helper()
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "mapping_target_occupied" {
		t.Fatalf("error = %#v, want mapping_target_occupied", err)
	}
}

func waitForBackendBlockedByPID(
	t *testing.T,
	ctx context.Context,
	fixture rssTargetFixture,
	blockerPID int32,
	earlyResult <-chan error,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-earlyResult:
			t.Fatalf("operation returned before target lock was released: %v", err)
		default:
		}
		var blocked bool
		if err := fixture.pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM pg_stat_activity AS activity
    WHERE $1 = ANY(pg_blocking_pids(activity.pid))
)`, blockerPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for target-lock contention")
}

func TestSearchAcquisitionRejectsExistingRSSTargetReservationIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	persisted, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "rss-owner")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(persisted.Candidates) != 1 {
		t.Fatalf("PersistPoll() = %#v, %v", persisted, err)
	}
	if err := fixture.workflow.ScheduleRSSDownload(fixture.ctx, persisted.Candidates[0]); err != nil {
		t.Fatalf("ScheduleRSSDownload() error = %v", err)
	}

	candidateID := addTargetSearchCandidate(t, fixture, "sequential")
	search := NewSearchWorkflow(fixture.queries, database.NewTransactor(fixture.pool), fixture.workflow.operations)
	_, err = search.CreateAcquisition(fixture.ctx, targetSearchAcquisitionInput(fixture, candidateID, 1, "search-after-rss"))
	requireMappingTargetOccupied(t, err)

	var searchAcquisitions int
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT count(*)
FROM acquisitions
WHERE release_candidate_id = $1`, candidateID).Scan(&searchAcquisitions); err != nil {
		t.Fatal(err)
	}
	if searchAcquisitions != 0 {
		t.Fatalf("search acquisition count = %d, want 0", searchAcquisitions)
	}
}

func TestSearchAcquisitionMappingReservationIsIdempotentIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	candidateID := addTargetSearchCandidate(t, fixture, "idempotent")
	search := NewSearchWorkflow(fixture.queries, database.NewTransactor(fixture.pool), fixture.workflow.operations)
	first, err := search.CreateAcquisition(
		fixture.ctx,
		targetSearchAcquisitionInput(fixture, candidateID, 1, "search-mapping-first"),
	)
	if err != nil {
		t.Fatalf("first CreateAcquisition() error = %v", err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE episode_mapping_profiles SET active = false WHERE id = $1`, fixture.profileID); err != nil {
		t.Fatal(err)
	}
	second, err := search.CreateAcquisition(
		fixture.ctx,
		targetSearchAcquisitionInput(fixture, candidateID, 1, "search-mapping-replay"),
	)
	if err != nil {
		t.Fatalf("replayed CreateAcquisition() error = %v", err)
	}
	if second.AcquisitionID != first.AcquisitionID || second.DownloadID != first.DownloadID || second.Operation.ID != first.Operation.ID {
		t.Fatalf("replayed result = %#v, want same resources as %#v", second, first)
	}
}

func TestSearchAcquisitionSerializesWithTargetReservationIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	persisted, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(1, "concurrent-rss-owner")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(persisted.Candidates) != 1 {
		t.Fatalf("PersistPoll() = %#v, %v", persisted, err)
	}
	candidateID := addTargetSearchCandidate(t, fixture, "concurrent")
	search := NewSearchWorkflow(fixture.queries, database.NewTransactor(fixture.pool), fixture.workflow.operations)

	reservationTx, err := fixture.pool.BeginTx(fixture.ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reservationTx.Rollback(context.Background()) }()
	var blockerPID int32
	if err := reservationTx.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := reservationTx.Exec(
		fixture.ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended('rss-target:' || $1::text, 0))",
		fixture.targetEpisodeIDs[1],
	); err != nil {
		t.Fatal(err)
	}

	searchResult := make(chan error, 1)
	go func() {
		_, createErr := search.CreateAcquisition(
			fixture.ctx,
			targetSearchAcquisitionInput(fixture, candidateID, 2, "search-concurrent-reservation"),
		)
		searchResult <- createErr
	}()
	waitForBackendBlockedByPID(t, fixture.ctx, fixture, blockerPID, searchResult)

	txQueries := db.New(reservationTx)
	if _, err := txQueries.MarkRSSEntryEnqueueing(fixture.ctx, repository.UUIDToPG(persisted.Candidates[0].EntryID)); err != nil {
		t.Fatal(err)
	}
	sourcePayload, err := json.Marshal(map[string]any{
		"rssEntryId": persisted.Candidates[0].EntryID, "sourceSeason": 1, "sourceEpisode": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	acquisition, err := txQueries.UpsertRSSAcquisition(fixture.ctx, db.UpsertRSSAcquisitionParams{
		ID: repository.UUIDToPG(uuid.New()), SeriesID: repository.UUIDToPG(fixture.seriesID),
		MappingProfileID: repository.UUIDToPG(fixture.profileID),
		RssEntryID:       repository.UUIDToPG(persisted.Candidates[0].EntryID), SourcePayload: sourcePayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := txQueries.CreateRSSDownloadAttempt(fixture.ctx, db.CreateRSSDownloadAttemptParams{
		ID: repository.UUIDToPG(uuid.New()), AcquisitionID: acquisition.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := reservationTx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-searchResult:
		requireMappingTargetOccupied(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("search acquisition did not resume after target lock release")
	}
	var rssAcquisitions, searchAcquisitions int
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT
    count(*) FILTER (WHERE rss_entry_id = $1),
    count(*) FILTER (WHERE release_candidate_id = $2)
FROM acquisitions`, persisted.Candidates[0].EntryID, candidateID).Scan(&rssAcquisitions, &searchAcquisitions); err != nil {
		t.Fatal(err)
	}
	if rssAcquisitions != 1 || searchAcquisitions != 0 {
		t.Fatalf("reservation counts = rss %d search %d, want 1/0", rssAcquisitions, searchAcquisitions)
	}
}

func addRSSAnchorMappingAcquisition(t *testing.T, fixture rssTargetFixture, suffix string) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	entryID, acquisitionID, downloadID, sourceFileID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO rss_entries (
    id, subscription_id, identity_key, title, status, download_uri,
    downloadable, rejection_reasons, source_season, source_episode
) VALUES ($1, $2, $3, $4, 'enqueued', $5, true, ARRAY[]::text[], 1, 1)`,
		entryID,
		fixture.subscriptionID,
		"guid:anchor-lock-"+suffix,
		"Anchor lock "+suffix,
		"https://example.test/anchor-lock-"+suffix+".torrent",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO acquisitions (
    id, series_id, source_kind, rss_entry_id, source_payload, created_by
) VALUES ($1, $2, 'rss', $3, '{"sourceSeason":1,"sourceEpisode":1}', $4)`,
		acquisitionID, fixture.seriesID, entryID, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO downloads (id, acquisition_id, attempt, status, progress)
VALUES ($1, $2, 1, 'completed', 1)`, downloadID, acquisitionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO download_files (
    id, download_id, file_index, relative_path, size_bytes, media_kind,
    selected, source_season, source_episode
) VALUES ($1, $2, 0, $3, 1024, 'video', true, 1, 1)`,
		sourceFileID, downloadID, "Anchor.Lock."+suffix+".S01E01.mkv"); err != nil {
		t.Fatal(err)
	}
	return acquisitionID, sourceFileID, downloadID
}

func TestAnchorMappingLocksRSSSubscriptionBeforeAcquisitionAndSeriesIntegration(t *testing.T) {
	for _, blockedResource := range []string{"acquisition", "series"} {
		t.Run(blockedResource, func(t *testing.T) {
			fixture := newRSSTargetFixture(t)
			acquisitionID, sourceFileID, _ := addRSSAnchorMappingAcquisition(t, fixture, blockedResource)
			catalog := NewCatalogWorkflow(
				fixture.queries,
				database.NewTransactor(fixture.pool),
				fixture.workflow.operations,
			)

			blockerTx, err := fixture.pool.BeginTx(fixture.ctx, pgx.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blockerTx.Rollback(context.Background()) }()
			var blockerPID int32
			if err := blockerTx.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			switch blockedResource {
			case "acquisition":
				if _, err := blockerTx.Exec(fixture.ctx, `SELECT id FROM acquisitions WHERE id = $1 FOR UPDATE`, acquisitionID); err != nil {
					t.Fatal(err)
				}
			case "series":
				if _, err := blockerTx.Exec(fixture.ctx, `SELECT id FROM media_series WHERE id = $1 FOR UPDATE`, fixture.seriesID); err != nil {
					t.Fatal(err)
				}
			}

			saveResult := make(chan error, 1)
			go func() {
				_, saveErr := catalog.SaveEpisodeMapping(fixture.ctx, domain.EpisodeMappingPlanInput{
					AcquisitionID: acquisitionID,
					Mode:          domain.EpisodeMappingModeAnchor,
					Anchor: domain.EpisodeMappingAnchorInput{
						SourceFileID: sourceFileID,
						Target:       domain.EpisodeCoordinate{Season: 2, Episode: 1},
					},
					IdempotencyKey: "anchor-lock-order-" + blockedResource,
					ActorUserID:    fixture.actorID,
				})
				saveResult <- saveErr
			}()
			waitForBackendBlockedByPID(t, fixture.ctx, fixture, blockerPID, saveResult)

			probeTx, err := fixture.pool.BeginTx(fixture.ctx, pgx.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_, probeErr := probeTx.Exec(
				fixture.ctx,
				`SELECT id FROM rss_subscriptions WHERE id = $1 FOR UPDATE NOWAIT`,
				fixture.subscriptionID,
			)
			_ = probeTx.Rollback(context.Background())
			var databaseErr *pgconn.PgError
			lockHeld := errors.As(probeErr, &databaseErr) && databaseErr.Code == "55P03"
			if err := blockerTx.Commit(fixture.ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-saveResult:
				if err != nil {
					t.Fatalf("SaveEpisodeMapping() error = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("mapping save did not resume after blocker release")
			}
			if !lockHeld {
				t.Fatalf("subscription NOWAIT probe error = %#v, want lock_not_available", probeErr)
			}
		})
	}
}

func TestRSSPollLocksSubscriptionBeforeUpdatingEntriesIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	feed := domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "poll-lock-order")},
	}
	initial, err := fixture.workflow.PersistPoll(
		fixture.ctx,
		fixture.pollOperationID,
		fixture.subscriptionID,
		feed,
		domain.RSSPollPersistOptions{},
	)
	if err != nil || len(initial.Candidates) != 1 {
		t.Fatalf("initial PersistPoll() = %#v, %v", initial, err)
	}

	entryTx, err := fixture.pool.BeginTx(fixture.ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = entryTx.Rollback(context.Background()) }()
	var blockerPID int32
	if err := entryTx.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := entryTx.Exec(fixture.ctx, `SELECT id FROM rss_entries WHERE id = $1 FOR UPDATE`, initial.Candidates[0].EntryID); err != nil {
		t.Fatal(err)
	}

	pollResult := make(chan error, 1)
	go func() {
		_, pollErr := fixture.workflow.PersistPoll(
			fixture.ctx,
			fixture.pollOperationID,
			fixture.subscriptionID,
			feed,
			domain.RSSPollPersistOptions{},
		)
		pollResult <- pollErr
	}()
	waitForBackendBlockedByPID(t, fixture.ctx, fixture, blockerPID, pollResult)

	probeTx, err := fixture.pool.BeginTx(fixture.ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, probeErr := probeTx.Exec(
		fixture.ctx,
		`SELECT id FROM rss_subscriptions WHERE id = $1 FOR UPDATE NOWAIT`,
		fixture.subscriptionID,
	)
	_ = probeTx.Rollback(context.Background())
	var databaseErr *pgconn.PgError
	lockHeld := errors.As(probeErr, &databaseErr) && databaseErr.Code == "55P03"
	if err := entryTx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-pollResult:
		if err != nil {
			t.Fatalf("blocked PersistPoll() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("poll did not resume after entry lock release")
	}
	if !lockHeld {
		t.Fatalf("subscription NOWAIT probe error = %#v, want lock_not_available", probeErr)
	}
}

func TestRSSEnqueueSerializesSubscriptionMappingUpdateIntegration(t *testing.T) {
	fixture := newRSSTargetFixture(t)
	newProfileID := uuid.New()
	if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO episode_mapping_profiles (
    id, series_id, name, version, source_season_lengths, active,
    anchor_source_season, anchor_source_episode, anchor_target_episode_id, target_episode_offset,
    created_by, decision_source
) VALUES ($1, $2, 'target-occupancy-updated', 1, ARRAY[2], true, 1, 1, $3, 0, $4, 'user')`,
		newProfileID, fixture.seriesID, fixture.targetEpisodeIDs[0], fixture.actorID); err != nil {
		t.Fatal(err)
	}
	for index, targetID := range fixture.targetEpisodeIDs {
		if _, err := fixture.pool.Exec(fixture.ctx, `
INSERT INTO episode_mappings (
    id, profile_id, source_season, source_episode, absolute_episode,
    target_episode_id, mapping_status, match_source
) VALUES ($1, $2, 1, $3, $3, $4, 'mapped', 'explicit')`,
			uuid.New(), newProfileID, index+1, targetID); err != nil {
			t.Fatal(err)
		}
	}
	persisted, err := fixture.workflow.PersistPoll(fixture.ctx, fixture.pollOperationID, fixture.subscriptionID, domain.RSSFeed{
		Title: "Target Occupancy", Entries: []domain.RSSFeedEntry{targetFeedEntry(0, "profile-update")},
	}, domain.RSSPollPersistOptions{})
	if err != nil || len(persisted.Candidates) != 1 {
		t.Fatalf("PersistPoll() = %#v, %v", persisted, err)
	}

	blockerTx, err := fixture.pool.BeginTx(fixture.ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blockerTx.Rollback(context.Background()) }()
	var blockerPID int32
	if err := blockerTx.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := blockerTx.Exec(
		fixture.ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended('rss-target:' || $1::text, 0))",
		fixture.targetEpisodeIDs[0],
	); err != nil {
		t.Fatal(err)
	}

	scheduleResult := make(chan error, 1)
	go func() {
		scheduleResult <- fixture.workflow.ScheduleRSSDownload(fixture.ctx, persisted.Candidates[0])
	}()
	waitForBackendBlockedByPID(t, fixture.ctx, fixture, blockerPID, scheduleResult)

	type updateResult struct {
		subscription domain.RSSSubscription
		err          error
	}
	updateResults := make(chan updateResult, 1)
	go func() {
		updated, updateErr := fixture.workflow.UpdateSubscription(fixture.ctx, domain.UpdateRSSSubscription{
			ID: fixture.subscriptionID, ExpectedVersion: 1,
			Name: "Target Occupancy Feed", FeedURL: "https://example.test/target-occupancy.xml",
			Enabled: false, SourceSeason: 1, MappingProfileID: newProfileID,
			PollInterval: 15 * time.Minute, ActorUserID: fixture.actorID,
		})
		updateResults <- updateResult{subscription: updated, err: updateErr}
	}()

	deadline := time.Now().Add(5 * time.Second)
	updateBlocked := false
	for time.Now().Before(deadline) {
		select {
		case result := <-updateResults:
			t.Fatalf("subscription update crossed an in-flight reservation: %v", result.err)
		default:
		}
		if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT EXISTS (
    SELECT 1
    FROM pg_stat_activity AS activity
    WHERE activity.datname = current_database()
      AND activity.wait_event_type = 'Lock'
      AND activity.query LIKE '%UPDATE rss_subscriptions%'
)`).Scan(&updateBlocked); err != nil {
			t.Fatal(err)
		}
		if updateBlocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !updateBlocked {
		t.Fatal("subscription update did not wait for the reservation transaction")
	}
	if err := blockerTx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-scheduleResult:
		if err != nil {
			t.Fatalf("ScheduleRSSDownload() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RSS reservation did not complete after target lock release")
	}
	select {
	case result := <-updateResults:
		if result.err != nil {
			t.Fatalf("UpdateSubscription() error = %v", result.err)
		}
		if result.subscription.MappingProfileID != newProfileID {
			t.Fatalf("updated mapping profile = %s, want %s", result.subscription.MappingProfileID, newProfileID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription update did not complete after reservation commit")
	}

	var acquisitionProfileID uuid.UUID
	if err := fixture.pool.QueryRow(fixture.ctx, `
SELECT acquisition.mapping_profile_id
FROM acquisitions AS acquisition
WHERE acquisition.rss_entry_id = $1`, persisted.Candidates[0].EntryID).Scan(&acquisitionProfileID); err != nil {
		t.Fatal(err)
	}
	if acquisitionProfileID != newProfileID {
		t.Fatalf("acquisition mapping profile = %s, want propagated %s", acquisitionProfileID, newProfileID)
	}
}
