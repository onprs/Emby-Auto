//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestStartAttemptMissingOperationKeepsNotRunnable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	lifecycle := NewOperationLifecycle(db.New(pool), database.NewTransactor(pool))
	if _, err := lifecycle.StartAttempt(ctx, uuid.New(), 1, "missing-operation"); !errors.Is(err, domain.ErrOperationNotRunnable) {
		t.Fatalf("missing operation StartAttempt error = %v, want ErrOperationNotRunnable", err)
	}
}
