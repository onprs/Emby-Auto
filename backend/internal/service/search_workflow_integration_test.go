//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func TestSearchAcquisitionSchedulesRealRiverJobsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	transactor := database.NewTransactor(pool)
	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	workflow := NewSearchWorkflow(queries, transactor, NewOperationScheduler(transactor, riverClient))
	actorID, searchID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "search-acquisition-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateSearchRun(ctx, db.CreateSearchRunParams{ID: repository.UUIDToPG(searchID), Query: "Fixture Show", RequestedBy: repository.UUIDToPG(actorID)}); err != nil {
		t.Fatal(err)
	}
	if err := workflow.CompleteSearch(ctx, searchID, uuid.Nil, domain.SearchProviderResult{Candidates: []domain.ReleaseCandidate{{
		Provider: "fixture", IdentityKey: "fixture:acquisition", Title: "Fixture Show - S01E01",
		DownloadURI: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
	}}}); err != nil {
		t.Fatal(err)
	}
	var candidateID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM release_candidates WHERE search_run_id = $1`, searchID).Scan(&candidateID); err != nil {
		t.Fatal(err)
	}
	result, err := workflow.CreateAcquisition(ctx, domain.CreateSearchAcquisition{
		CandidateID: candidateID, TMDbSeriesID: 100, SeriesTitle: "Fixture Show", SourceSeason: 1, SourceEpisode: 1,
		SingleEpisode: true, IdempotencyKey: "fixture-acquisition", ActorUserID: actorID,
	})
	if err != nil {
		t.Fatalf("CreateAcquisition() error = %v", err)
	}
	if result.AcquisitionID == uuid.Nil || result.DownloadID == uuid.Nil || result.Operation.ID == uuid.Nil {
		t.Fatalf("CreateAcquisition() result = %#v", result)
	}
	var operationCount, jobCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM operations WHERE resource_id IN ($1, (SELECT series_id FROM acquisitions WHERE id = $2))`, result.DownloadID, result.AcquisitionID).Scan(&operationCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM river_job`).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if operationCount != 2 || jobCount != 2 {
		t.Fatalf("operation/job counts = %d/%d, want 2/2", operationCount, jobCount)
	}
}

func TestSearchWorkerEventsAllowSystemActorIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	workflow := NewSearchWorkflow(queries, database.NewTransactor(pool), nil)
	actorID, searchID, operationID := uuid.New(), uuid.New(), uuid.New()

	if _, err := pool.Exec(ctx, `INSERT INTO admin_users (id, username, password_hash) VALUES ($1, $2, 'fixture-hash')`, actorID, "search-system-event-"+actorID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateSearchRun(ctx, db.CreateSearchRunParams{ID: repository.UUIDToPG(searchID), Query: "Fixture Show", RequestedBy: repository.UUIDToPG(actorID)}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO operations (id, kind, resource_type, resource_id, idempotency_key, status, max_attempts, timeout_seconds)
		VALUES ($1, 'search.run', 'search_run', $2, $3, 'running', 3, 120)`, operationID, searchID, "search-system-event-"+operationID.String()); err != nil {
		t.Fatal(err)
	}

	command, err := workflow.BeginSearch(ctx, searchID, operationID)
	if err != nil {
		t.Fatalf("BeginSearch() error = %v", err)
	}
	if command.Status != domain.SearchRunning {
		t.Fatalf("BeginSearch() status = %q, want running", command.Status)
	}
	if err := workflow.CompleteSearch(ctx, searchID, operationID, domain.SearchProviderResult{Candidates: []domain.ReleaseCandidate{{
		Provider: "fixture", IdentityKey: "fixture:one", Title: "Fixture Show - S01E01",
		DownloadURI: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
	}}}); err != nil {
		t.Fatalf("CompleteSearch() error = %v", err)
	}

	var nullActorEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE resource_type = 'search_run' AND resource_id = $1 AND actor_user_id IS NULL`, searchID).Scan(&nullActorEvents); err != nil {
		t.Fatal(err)
	}
	if nullActorEvents != 2 {
		t.Fatalf("system actor event count = %d, want 2", nullActorEvents)
	}
}
