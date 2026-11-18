//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/subtitlebinding"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestZZR1P3HeartbeatOperationUpdateZeroRollsBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	lifecycle := NewOperationLifecycle(db.New(pool), database.NewTransactor(pool))
	operationID, auditID, taskID := uuid.New(), uuid.New(), uuid.New()
	baseline := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO operations (
    id, kind, resource_type, resource_id, idempotency_key, status,
    max_attempts, attempt_count, timeout_seconds, payload, started_at, heartbeat_at
) VALUES ($1, 'subtitle.prepare', 'episode_task', $2, $3, 'running', 3, 1, 60, '{}', $4, $4);
INSERT INTO operation_attempts (id, operation_id, attempt, status, worker_id, started_at, heartbeat_at)
VALUES ($5, $1, 1, 'running', 'zz-r1-heartbeat', $4, $4);
`, UUIDToPG(operationID), UUIDToPG(taskID), "zz-r1-heartbeat-"+operationID.String(), baseline, UUIDToPG(auditID)); err != nil {
		t.Fatal(err)
	}
	handle := subtitlebinding.AttemptHandle{
		OperationID: operationID, JobAttempt: 1, AttemptID: auditID, OperationAttemptCount: 1,
		ResourceType: "episode_task", ResourceID: taskID,
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "zz_hb_block_" + suffix
	triggerName := "zz_hb_trigger_" + suffix
	if _, err := testutil.ExecFixture(ctx, pool, fmt.Sprintf(`
CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id = '%s'::uuid THEN
        RETURN NULL;
    END IF;
    RETURN NEW;
END
$$`, functionName, operationID.String())); err != nil {
		t.Fatal(err)
	}
	if _, err := testutil.ExecFixture(ctx, pool, fmt.Sprintf(`
CREATE TRIGGER %s BEFORE UPDATE ON operations FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, functionName)); err != nil {
		t.Fatal(err)
	}
	var operationBefore, auditBefore time.Time
	if err := pool.QueryRow(ctx, `SELECT heartbeat_at FROM operations WHERE id = $1`, UUIDToPG(operationID)).Scan(&operationBefore); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT heartbeat_at FROM operation_attempts WHERE id = $1`, UUIDToPG(auditID)).Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	t.Logf("before heartbeat operation=%s audit=%s baseline=%s", operationBefore, auditBefore, baseline)
	alive, err := lifecycle.HeartbeatSubtitleAttempt(ctx, handle)
	var operationAfter, auditAfter time.Time
	if queryErr := pool.QueryRow(ctx, `SELECT heartbeat_at FROM operations WHERE id = $1`, UUIDToPG(operationID)).Scan(&operationAfter); queryErr != nil {
		t.Fatalf("read operation heartbeat after: %v", queryErr)
	}
	if queryErr := pool.QueryRow(ctx, `SELECT heartbeat_at FROM operation_attempts WHERE id = $1`, UUIDToPG(auditID)).Scan(&auditAfter); queryErr != nil {
		t.Fatalf("read audit heartbeat after: %v", queryErr)
	}
	t.Logf("after heartbeat alive=%t err=%v operation=%s audit=%s", alive, err, operationAfter, auditAfter)
	if err == nil {
		t.Errorf("HeartbeatSubtitleAttempt error = nil, want error after operations UPDATE 0 rows")
	}
	if alive {
		t.Errorf("HeartbeatSubtitleAttempt alive = true, want false")
	}
	if !operationAfter.Equal(baseline) {
		t.Errorf("operation heartbeat = %s, want rollback to baseline %s", operationAfter, baseline)
	}
	if !auditAfter.Equal(baseline) {
		t.Errorf("audit heartbeat = %s, want rollback to baseline %s (audit-only update must not commit)", auditAfter, baseline)
	}
}
