//go:build integration

package service

// 字幕租约锁序并发回归：小池真实 PG，S/T/R/O/FK 双方向。断言不出现
// PostgreSQL 40P01，且最终状态为业务上合法的两种串行结果之一。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func newSmallPoolSubtitleLeaseFixture(t *testing.T) *subtitleLeaseFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	databaseURL, _ := testutil.NewMigratedPostgres(t)
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 4
	config.MinConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	transactor := database.NewTransactor(pool)
	scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
	taskID, scopeID := seedSubtitleAgentEntities(t, ctx, pool)
	return &subtitleLeaseFixture{
		ctx: ctx, pool: pool, transactor: transactor, scheduler: scheduler,
		workflow:  NewMediaWorkflow(db.New(pool), transactor, scheduler),
		lifecycle: repository.NewOperationLifecycle(db.New(pool), transactor),
		taskID:    taskID, scopeID: scopeID,
	}
}

func assertNoDeadlock(t *testing.T, label string, err error) {
	t.Helper()
	if err == nil {
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
		t.Fatalf("%s deadlock detected: %v", label, err)
	}
	if strings.Contains(err.Error(), "deadlock detected") {
		t.Fatalf("%s deadlock detected: %v", label, err)
	}
}

func TestSubtitleAgentVideoReadyVersusTerminalConcurrencyIntegration(t *testing.T) {
	for iteration := 0; iteration < 3; iteration++ {
		fixture := newSmallPoolSubtitleLeaseFixture(t)
		service := fixture.service(subtitleLeaseConfiguration(1, "lease-model", "https://provider.invalid/v1"))
		created, err := service.CreateAutomatic(fixture.ctx, fixture.request())
		if err != nil {
			t.Fatalf("CreateAutomatic() error = %v", err)
		}
		if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, created.Operation.ID, 1, "concurrency-worker"); err != nil {
			t.Fatalf("StartAttempt() error = %v", err)
		}
		start := make(chan struct{})
		var wait sync.WaitGroup
		var videoErr, terminalErr error
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			_, videoErr = db.New(fixture.pool).MarkTaskVideoReady(fixture.ctx, repository.UUIDToPG(fixture.taskID))
		}()
		go func() {
			defer wait.Done()
			<-start
			terminalErr = fixture.lifecycle.FailAttempt(fixture.ctx, created.Operation.ID, 1, "agent_request_timeout", "concurrent terminal", false)
		}()
		close(start)
		wait.Wait()
		assertNoDeadlock(t, "video-ready/terminal", videoErr)
		assertNoDeadlock(t, "video-ready/terminal", terminalErr)
		if videoErr != nil {
			t.Fatalf("iteration %d MarkTaskVideoReady() error = %v", iteration, videoErr)
		}
		if terminalErr != nil {
			t.Fatalf("iteration %d FailAttempt() error = %v", iteration, terminalErr)
		}
		scopeStatus, _, _, taskState, videoState, subtitleState := fixture.scopeAndTask(t)
		if scopeStatus != "expired" || taskState != string(domain.TaskFailed) || subtitleState != string(domain.SubtitleFailed) {
			t.Fatalf("iteration %d terminal state scope=%s task=%s/%s", iteration, scopeStatus, taskState, subtitleState)
		}
		if videoState != string(domain.VideoReady) {
			t.Fatalf("iteration %d video state = %s, want video_ready", iteration, videoState)
		}
		var failedEvents int
		if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM events WHERE topic = 'task.media_failed' AND resource_id = $1`, repository.UUIDToPG(fixture.taskID)).Scan(&failedEvents); err != nil {
			t.Fatal(err)
		}
		if failedEvents != 1 {
			t.Fatalf("iteration %d media_failed events = %d, want 1", iteration, failedEvents)
		}
	}
}

func TestSubtitleAgentScopeReuseVersusLateTerminalConcurrencyIntegration(t *testing.T) {
	for iteration := 0; iteration < 3; iteration++ {
		fixture := newSmallPoolSubtitleLeaseFixture(t)
		service := fixture.service(subtitleLeaseConfiguration(1, "lease-model", "https://provider.invalid/v1"))
		created, err := service.CreateAutomatic(fixture.ctx, fixture.request())
		if err != nil {
			t.Fatalf("CreateAutomatic() error = %v", err)
		}
		if _, err := fixture.lifecycle.StartAttempt(fixture.ctx, created.Operation.ID, 1, "concurrency-worker"); err != nil {
			t.Fatalf("StartAttempt() error = %v", err)
		}
		start := make(chan struct{})
		var wait sync.WaitGroup
		var reuseErr, terminalErr error
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			if expireErr := fixture.workflow.ExpireSubtitleVideoMatchScope(fixture.ctx, fixture.scopeID); expireErr != nil {
				reuseErr = expireErr
				return
			}
			_, reuseErr = fixture.workflow.CreateSubtitleVideoMatchScope(fixture.ctx, fixture.taskID, []domain.SubtitleMatchCandidate{
				{CandidateID: "embedded-6", Source: domain.SubtitleSourceEmbedded, StreamIndex: 6, Format: domain.SubtitleASS, Language: "chi", Title: "并发新代一"},
				{CandidateID: "embedded-7", Source: domain.SubtitleSourceEmbedded, StreamIndex: 7, Format: domain.SubtitleASS, Language: "chi", Title: "并发新代二"},
			})
		}()
		go func() {
			defer wait.Done()
			<-start
			terminalErr = fixture.lifecycle.FailAttempt(fixture.ctx, created.Operation.ID, 1, "agent_request_timeout", "concurrent terminal", false)
		}()
		close(start)
		wait.Wait()
		assertNoDeadlock(t, "scope-reuse/terminal", reuseErr)
		assertNoDeadlock(t, "scope-reuse/terminal", terminalErr)
		if reuseErr != nil {
			t.Fatalf("iteration %d scope reuse error = %v", iteration, reuseErr)
		}
		if terminalErr != nil {
			t.Fatalf("iteration %d FailAttempt() error = %v", iteration, terminalErr)
		}
		scopeStatus, _, _, taskState, _, subtitleState := fixture.scopeAndTask(t)
		if scopeStatus != "pending" {
			t.Fatalf("iteration %d final scope = %s, want pending new generation", iteration, scopeStatus)
		}
		var candidateIDs []string
		rows, err := fixture.pool.Query(fixture.ctx, `SELECT candidate_id FROM subtitle_video_match_candidates WHERE scope_id = $1 ORDER BY candidate_id`, repository.UUIDToPG(fixture.scopeID))
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var candidateID string
			if err := rows.Scan(&candidateID); err != nil {
				t.Fatal(err)
			}
			candidateIDs = append(candidateIDs, candidateID)
		}
		rows.Close()
		if len(candidateIDs) != 2 || candidateIDs[0] != "embedded-6" || candidateIDs[1] != "embedded-7" {
			t.Fatalf("iteration %d final candidates = %v, want new generation", iteration, candidateIDs)
		}
		old := fixture.resolution(t, created.Resolution.ID)
		if old.Status != string(domain.AgentResolutionCancelled) && old.Status != string(domain.AgentResolutionFailed) {
			t.Fatalf("iteration %d old resolution = %s, want terminal", iteration, old.Status)
		}
		if taskState == string(domain.TaskFailed) {
			if subtitleState != string(domain.SubtitleFailed) {
				t.Fatalf("iteration %d failed task subtitle = %s", iteration, subtitleState)
			}
			var failedEvents int
			if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM events WHERE topic = 'task.media_failed' AND resource_id = $1`, repository.UUIDToPG(fixture.taskID)).Scan(&failedEvents); err != nil {
				t.Fatal(err)
			}
			if failedEvents != 1 {
				t.Fatalf("iteration %d failed task events = %d, want 1", iteration, failedEvents)
			}
		} else if taskState != string(domain.TaskProcessing) {
			t.Fatalf("iteration %d unexpected task state = %s", iteration, taskState)
		}
	}
}
