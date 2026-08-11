package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type catalogCommandForwardingStub struct {
	syncInput domain.SyncTMDbSeries
}

func (stub *catalogCommandForwardingStub) ScheduleTMDbSync(_ context.Context, input domain.SyncTMDbSeries) (domain.CatalogCommandResult, error) {
	stub.syncInput = input
	return domain.CatalogCommandResult{
		SeriesID:  uuid.MustParse("74000000-0000-4000-8000-000000000001"),
		Operation: domain.Operation{ID: uuid.MustParse("74000000-0000-4000-8000-000000000002"), Status: "queued"},
	}, nil
}

func (*catalogCommandForwardingStub) PreviewEpisodeMapping(context.Context, domain.EpisodeMappingPlanInput) (domain.EpisodeMappingPreview, error) {
	return domain.EpisodeMappingPreview{}, nil
}

func (*catalogCommandForwardingStub) SaveEpisodeMapping(context.Context, domain.EpisodeMappingPlanInput) (domain.SavedEpisodeMapping, error) {
	return domain.SavedEpisodeMapping{}, nil
}

type embyCatalogCommandForwardingStub struct {
	refreshInput domain.CreateEmbyRefresh
	scanInput    domain.CreateEmbyScan
}

func (stub *embyCatalogCommandForwardingStub) ScheduleRefresh(_ context.Context, input domain.CreateEmbyRefresh) (domain.Operation, error) {
	stub.refreshInput = input
	return domain.Operation{ID: uuid.MustParse("74000000-0000-4000-8000-000000000003"), Status: "queued"}, nil
}

func (stub *embyCatalogCommandForwardingStub) ScheduleScan(_ context.Context, input domain.CreateEmbyScan) (domain.EmbyScanCommandResult, error) {
	stub.scanInput = input
	now := time.Date(2026, time.August, 4, 2, 0, 0, 0, time.UTC)
	return domain.EmbyScanCommandResult{
		Scan:      domain.EmbyScan{ID: uuid.MustParse("74000000-0000-4000-8000-000000000004"), Status: domain.EmbyScanQueued, CreatedAt: now, UpdatedAt: now},
		Operation: domain.Operation{ID: uuid.MustParse("74000000-0000-4000-8000-000000000005"), Status: "queued"},
	}, nil
}

func (*embyCatalogCommandForwardingStub) GetScan(context.Context, uuid.UUID) (domain.EmbyScan, error) {
	return domain.EmbyScan{}, domain.ErrNotFound
}

func (*embyCatalogCommandForwardingStub) ListScans(context.Context, *uuid.UUID, int) (domain.EmbyScanPage, error) {
	return domain.EmbyScanPage{}, nil
}

func (*embyCatalogCommandForwardingStub) ListLibraries(context.Context) ([]domain.EmbyLibrary, error) {
	return nil, nil
}

func (*embyCatalogCommandForwardingStub) ListLibraryItems(context.Context, uuid.UUID, domain.EmbyLibraryItemFilter, *uuid.UUID, int) (domain.EmbyLibraryItemPage, error) {
	return domain.EmbyLibraryItemPage{}, nil
}

func TestSyncTMDbSeriesForwardsOriginalIdempotencyKey(t *testing.T) {
	authentication, actorID := authenticatedServer()
	stub := &catalogCommandForwardingStub{}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithCatalog(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tmdb/series/42/sync", strings.NewReader(`{"seriesTitle":"Canonical Show"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "tmdb-sync-browser-key")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if stub.syncInput.TMDbSeriesID != 42 || stub.syncInput.SeriesTitle != "Canonical Show" || stub.syncInput.IdempotencyKey != "tmdb-sync-browser-key" || stub.syncInput.ActorUserID != actorID {
		t.Fatalf("sync input = %#v", stub.syncInput)
	}
}

func TestEmbyCommandsForwardOriginalIdempotencyKey(t *testing.T) {
	authentication, actorID := authenticatedServer()
	stub := &embyCatalogCommandForwardingStub{}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithEmbyCatalog(stub)))

	for _, test := range []struct {
		name string
		path string
		key  string
		got  func() (string, uuid.UUID)
	}{
		{name: "scan", path: "/api/v1/emby/scans", key: "emby-scan-browser-key", got: func() (string, uuid.UUID) { return stub.scanInput.IdempotencyKey, stub.scanInput.ActorUserID }},
		{name: "refresh", path: "/api/v1/emby/refresh", key: "emby-refresh-browser-key", got: func() (string, uuid.UUID) { return stub.refreshInput.IdempotencyKey, stub.refreshInput.ActorUserID }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			request.Header.Set("Idempotency-Key", test.key)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusAccepted {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			key, actor := test.got()
			if key != test.key || actor != actorID {
				t.Fatalf("forwarded key/actor = %q/%s", key, actor)
			}
		})
	}
}
