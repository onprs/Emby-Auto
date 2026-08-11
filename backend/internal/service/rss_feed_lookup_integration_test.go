//go:build integration

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

func TestAgentCatalogContextLoadsPreSubscriptionRSSLookupIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, pool := testutil.NewMigratedPostgres(t)
	queries := db.New(pool)
	lookupID := uuid.New()
	_, err := queries.CreateRSSFeedCatalogLookup(ctx, db.CreateRSSFeedCatalogLookupParams{
		ID: repository.UUIDToPG(lookupID), FeedTitle: "发布组 候选剧集 内封",
		SuggestedQueries: []string{"发布组 候选剧集 内封", "候选剧集"},
		SampleTitles:     []string{"[发布组] 候选剧集 - 01 [1080p]"},
		ExpiresAt:        pgtype.Timestamptz{Time: time.Now().UTC().Add(30 * time.Minute), Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateRSSFeedCatalogLookup() error = %v", err)
	}

	service := &AgentResolutionService{queries: queries}
	snapshot, err := service.buildCatalogAgentContext(ctx, lookupID)
	if err != nil {
		t.Fatalf("buildCatalogAgentContext() error = %v", err)
	}
	if snapshot.ResourceType != "rss_feed_lookup" || len(snapshot.Tools) != 1 {
		t.Fatalf("catalog snapshot = %+v", snapshot)
	}
	var resource struct {
		LookupID         uuid.UUID `json:"lookupId"`
		SuggestedQueries []string  `json:"suggestedQueries"`
		SampleTitles     []string  `json:"sampleTitles"`
		FeedURL          string    `json:"feedUrl"`
	}
	if err := json.Unmarshal(snapshot.Resource, &resource); err != nil {
		t.Fatalf("decode snapshot resource: %v", err)
	}
	if resource.LookupID != lookupID || len(resource.SuggestedQueries) != 2 || len(resource.SampleTitles) != 1 || resource.FeedURL != "" {
		t.Fatalf("snapshot resource = %+v", resource)
	}
}
