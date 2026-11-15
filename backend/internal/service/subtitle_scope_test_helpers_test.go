//go:build integration

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

// expireSubtitleVideoMatchScopeForTest 是显式的测试专用 raw SQL helper，
// 仅用于合成 generation reset/旧入口基线；生产 handler 不使用 raw expire。
func expireSubtitleVideoMatchScopeForTest(t *testing.T, ctx context.Context, queries *db.Queries, scopeID uuid.UUID) {
	t.Helper()
	if err := expireSubtitleVideoMatchScopeForTestErr(ctx, queries, scopeID); err != nil {
		t.Fatalf("synthetic raw ExpireSubtitleVideoMatchScope() error = %v", err)
	}
}

func expireSubtitleVideoMatchScopeForTestErr(ctx context.Context, queries *db.Queries, scopeID uuid.UUID) error {
	if _, err := queries.ExpireSubtitleVideoMatchScope(ctx, repository.UUIDToPG(scopeID)); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return nil
}
