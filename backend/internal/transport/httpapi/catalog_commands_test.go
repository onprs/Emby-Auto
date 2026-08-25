package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type catalogCommandForwardingStub struct {
	syncInput    domain.SyncTMDbSeries
	previewInput domain.EpisodeMappingPlanInput
	saveInput    domain.EpisodeMappingPlanInput
	previewCalls int
	saveCalls    int
}

func (stub *catalogCommandForwardingStub) ScheduleTMDbSync(_ context.Context, input domain.SyncTMDbSeries) (domain.CatalogCommandResult, error) {
	stub.syncInput = input
	return domain.CatalogCommandResult{
		SeriesID:  uuid.MustParse("74000000-0000-4000-8000-000000000001"),
		Operation: domain.Operation{ID: uuid.MustParse("74000000-0000-4000-8000-000000000002"), Status: "queued"},
	}, nil
}

func (stub *catalogCommandForwardingStub) PreviewEpisodeMapping(_ context.Context, input domain.EpisodeMappingPlanInput) (domain.EpisodeMappingPreview, error) {
	stub.previewCalls++
	stub.previewInput = input
	return domain.EpisodeMappingPreview{AcquisitionID: input.AcquisitionID, Mode: input.Mode, Anchor: input.Anchor, Rows: []domain.EpisodeMappingRow{}}, nil
}

func (stub *catalogCommandForwardingStub) SaveEpisodeMapping(_ context.Context, input domain.EpisodeMappingPlanInput) (domain.SavedEpisodeMapping, error) {
	stub.saveCalls++
	stub.saveInput = input
	return domain.SavedEpisodeMapping{
		ProfileID: uuid.MustParse("74000000-0000-4000-8000-000000000006"), Version: 1,
		Preview: domain.EpisodeMappingPreview{AcquisitionID: input.AcquisitionID, Mode: input.Mode, Anchor: input.Anchor, Rows: []domain.EpisodeMappingRow{}},
	}, nil
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

func TestEpisodeMappingCommandsDecodeLegacyAnchorAndExplicitPlans(t *testing.T) {
	authentication, actorID := authenticatedServer()
	acquisitionID := uuid.MustParse("74000000-0000-4000-8000-000000000011")
	firstFileID := uuid.MustParse("74000000-0000-4000-8000-000000000012")
	secondFileID := uuid.MustParse("74000000-0000-4000-8000-000000000013")

	tests := []struct {
		name  string
		body  string
		check func(t *testing.T, input domain.EpisodeMappingPlanInput)
	}{
		{
			name: "legacy anchor",
			body: `{"anchor":{"sourceFileId":"74000000-0000-4000-8000-000000000012","targetSeason":1,"targetEpisode":3}}`,
			check: func(t *testing.T, input domain.EpisodeMappingPlanInput) {
				if input.Mode != domain.EpisodeMappingModeAnchor || input.Anchor.SourceFileID != firstFileID || input.Anchor.Target != (domain.EpisodeCoordinate{Season: 1, Episode: 3}) {
					t.Fatalf("legacy input = %#v", input)
				}
			},
		},
		{
			name: "explicit special and exclusion",
			body: `{"mode":"explicit","assignments":[{"sourceFileId":"74000000-0000-4000-8000-000000000012","action":"map","targetSeason":0,"targetEpisode":2},{"sourceFileId":"74000000-0000-4000-8000-000000000013","action":"exclude"}]}`,
			check: func(t *testing.T, input domain.EpisodeMappingPlanInput) {
				if input.Mode != domain.EpisodeMappingModeExplicit || len(input.Assignments) != 2 || input.Assignments[0].SourceFileID != firstFileID || input.Assignments[0].Target != (domain.EpisodeCoordinate{Season: 0, Episode: 2}) || input.Assignments[1].SourceFileID != secondFileID || input.Assignments[1].Action != domain.EpisodeMappingExplicitExclude {
					t.Fatalf("explicit input = %#v", input)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &catalogCommandForwardingStub{}
			handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithCatalog(stub)))
			request := httptest.NewRequest(http.MethodPut, "/api/v1/acquisitions/"+acquisitionID.String()+"/episode-mapping", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "mapping-browser-key")
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if stub.saveInput.AcquisitionID != acquisitionID || stub.saveInput.IdempotencyKey != "mapping-browser-key" || stub.saveInput.ActorUserID != actorID {
				t.Fatalf("save input metadata = %#v", stub.saveInput)
			}
			test.check(t, stub.saveInput)
		})
	}
}

func TestEpisodeMappingCommandsRejectNullMixedAndUnknownShapes(t *testing.T) {
	authentication, _ := authenticatedServer()
	acquisitionID := uuid.MustParse("74000000-0000-4000-8000-000000000021")
	fileID := uuid.MustParse("74000000-0000-4000-8000-000000000022")
	anchor := `{"sourceFileId":"` + fileID.String() + `","targetSeason":1,"targetEpisode":1}`
	exclude := `{"sourceFileId":"` + fileID.String() + `","action":"exclude"}`
	tests := []struct {
		name string
		body string
	}{
		{name: "map missing targetSeason", body: `{"mode":"explicit","assignments":[{"sourceFileId":"` + fileID.String() + `","action":"map","targetEpisode":1}]}`},
		{name: "map null targetSeason", body: `{"mode":"explicit","assignments":[{"sourceFileId":"` + fileID.String() + `","action":"map","targetSeason":null,"targetEpisode":1}]}`},
		{name: "map null targetEpisode", body: `{"mode":"explicit","assignments":[{"sourceFileId":"` + fileID.String() + `","action":"map","targetSeason":0,"targetEpisode":null}]}`},
		{name: "exclude numeric target", body: `{"mode":"explicit","assignments":[{"sourceFileId":"` + fileID.String() + `","action":"exclude","targetSeason":0}]}`},
		{name: "exclude null targetSeason", body: `{"mode":"explicit","assignments":[{"sourceFileId":"` + fileID.String() + `","action":"exclude","targetSeason":null}]}`},
		{name: "exclude null targetEpisode", body: `{"mode":"explicit","assignments":[{"sourceFileId":"` + fileID.String() + `","action":"exclude","targetEpisode":null}]}`},
		{name: "explicit null anchor", body: `{"mode":"explicit","anchor":null,"assignments":[` + exclude + `]}`},
		{name: "anchor null assignments", body: `{"mode":"anchor","anchor":` + anchor + `,"assignments":null}`},
		{name: "null mode", body: `{"mode":null,"anchor":` + anchor + `}`},
		{name: "mixed modes", body: `{"mode":"explicit","anchor":` + anchor + `,"assignments":[` + exclude + `]}`},
		{name: "unknown envelope field", body: `{"anchor":` + anchor + `,"unexpected":true}`},
		{name: "unknown disposition field", body: `{"mode":"explicit","assignments":[{"sourceFileId":"` + fileID.String() + `","action":"exclude","unexpected":true}]}`},
	}
	endpoints := []struct {
		name   string
		method string
		path   string
	}{
		{name: "preview", method: http.MethodPost, path: "/api/v1/acquisitions/" + acquisitionID.String() + "/episode-mapping/preview"},
		{name: "save", method: http.MethodPut, path: "/api/v1/acquisitions/" + acquisitionID.String() + "/episode-mapping"},
	}
	for _, test := range tests {
		for _, endpoint := range endpoints {
			t.Run(test.name+"/"+endpoint.name, func(t *testing.T) {
				stub := &catalogCommandForwardingStub{}
				handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithCatalog(stub)))
				request := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(test.body))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Idempotency-Key", "mapping-invalid-key")
				request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
				response := httptest.NewRecorder()

				handler.ServeHTTP(response, request)

				if response.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
				}
				var apiError ApiError
				if err := json.Unmarshal(response.Body.Bytes(), &apiError); err != nil {
					t.Fatalf("decode structured error: %v, body = %s", err, response.Body.String())
				}
				if apiError.Code != "invalid_request" {
					t.Fatalf("error = %#v", apiError)
				}
				if stub.previewCalls != 0 || stub.saveCalls != 0 {
					t.Fatalf("invalid request reached catalog: preview=%d save=%d", stub.previewCalls, stub.saveCalls)
				}
			})
		}
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
