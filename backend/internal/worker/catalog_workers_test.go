package worker

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/emby"
	"github.com/onprs/emby-auto/backend/internal/platform/tmdb"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type catalogConfigurationStub struct {
	configuration domain.Configuration
	secrets       map[string]string
}

func (stub *catalogConfigurationStub) Load(context.Context) (domain.Configuration, error) {
	return stub.configuration, nil
}

func (stub *catalogConfigurationStub) ResolveSecret(_ context.Context, name string) (string, error) {
	value := stub.secrets[name]
	if value == "" {
		return "", domain.ErrNotFound
	}
	return value, nil
}

type embyCatalogClientStub struct {
	libraries []domain.EmbyLibraryCatalog
	pages     map[int]emby.ItemPage
	starts    []int
}

func (stub *embyCatalogClientStub) Libraries(context.Context) ([]domain.EmbyLibraryCatalog, error) {
	return stub.libraries, nil
}

func (stub *embyCatalogClientStub) LibraryItems(_ context.Context, _ string, start, _ int) (emby.ItemPage, error) {
	stub.starts = append(stub.starts, start)
	page, ok := stub.pages[start]
	if !ok {
		return emby.ItemPage{}, errors.New("unexpected page")
	}
	return page, nil
}

type embyCatalogStoreStub struct {
	begin    domain.EmbyScan
	complete []domain.EmbyLibrarySnapshot
}

func (stub *embyCatalogStoreStub) BeginScan(context.Context, domain.Operation) (domain.EmbyScan, error) {
	return stub.begin, nil
}

func (stub *embyCatalogStoreStub) CompleteScan(_ context.Context, _ domain.Operation, snapshots []domain.EmbyLibrarySnapshot) error {
	stub.complete = snapshots
	return nil
}

func TestEmbyScanHandlerPaginatesAndCompletesOneSnapshot(t *testing.T) {
	scanID := uuid.MustParse("82000000-0000-0000-0000-000000000001")
	configuration := &catalogConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{Emby: domain.EmbySettings{URL: "https://emby.test"}}},
		secrets:       map[string]string{domain.SecretEmbyAPIKey: "emby-key"},
	}
	client := &embyCatalogClientStub{
		libraries: []domain.EmbyLibraryCatalog{{EmbyID: "library-1", Name: "Anime", Payload: []byte(`{"ItemId":"library-1"}`)}},
		pages: map[int]emby.ItemPage{
			0: {TotalRecordCount: 3, Items: []domain.EmbyLibraryItemCatalog{
				{EmbyID: "series-1", ItemType: "Series", Name: "Show", ProviderIDs: map[string]string{}, Payload: []byte(`{"Id":"series-1"}`)},
				{EmbyID: "season-1", ItemType: "Season", Name: "Season 1", ProviderIDs: map[string]string{}, Payload: []byte(`{"Id":"season-1"}`)},
			}},
			2: {TotalRecordCount: 3, Items: []domain.EmbyLibraryItemCatalog{
				{EmbyID: "episode-1", ItemType: "Episode", Name: "Pilot", ProviderIDs: map[string]string{"Tmdb": "42"}, Payload: []byte(`{"Id":"episode-1"}`)},
			}},
		},
	}
	store := &embyCatalogStoreStub{begin: domain.EmbyScan{ID: scanID, Status: domain.EmbyScanRunning}}
	handler := NewEmbyScanHandler(configuration, store, func(options emby.ClientOptions) (EmbyCatalogClient, error) {
		if options.BaseURL != "https://emby.test" || options.APIKey != "emby-key" {
			t.Fatalf("client options = %#v", options)
		}
		return client, nil
	})
	operation := domain.Operation{ID: uuid.MustParse("82000000-0000-0000-0000-000000000002"), ResourceType: "emby_scan", ResourceID: scanID}
	if err := handler.Handle(context.Background(), operation); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(client.starts) != 2 || client.starts[0] != 0 || client.starts[1] != 2 {
		t.Fatalf("page starts = %#v", client.starts)
	}
	if len(store.complete) != 1 || len(store.complete[0].Items) != 3 {
		t.Fatalf("completed snapshots = %#v", store.complete)
	}
}

func TestEmbyScanHandlerRetriesWhenCatalogChangesBetweenPages(t *testing.T) {
	scanID := uuid.MustParse("82000000-0000-0000-0000-000000000003")
	client := &embyCatalogClientStub{
		libraries: []domain.EmbyLibraryCatalog{{EmbyID: "library-1", Name: "Anime", Payload: []byte(`{}`)}},
		pages: map[int]emby.ItemPage{
			0: {TotalRecordCount: 2, Items: []domain.EmbyLibraryItemCatalog{{EmbyID: "one", ItemType: "Series", Name: "One", Payload: []byte(`{}`)}}},
			1: {TotalRecordCount: 3, Items: []domain.EmbyLibraryItemCatalog{{EmbyID: "two", ItemType: "Series", Name: "Two", Payload: []byte(`{}`)}}},
		},
	}
	handler := NewEmbyScanHandler(
		&catalogConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Emby: domain.EmbySettings{URL: "https://emby.test"}}}, secrets: map[string]string{domain.SecretEmbyAPIKey: "key"}},
		&embyCatalogStoreStub{begin: domain.EmbyScan{ID: scanID, Status: domain.EmbyScanRunning}},
		func(emby.ClientOptions) (EmbyCatalogClient, error) { return client, nil },
	)
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "emby_scan", ResourceID: scanID})
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Retryable || failure.Code != "emby_catalog_changed" {
		t.Fatalf("Handle() error = %#v", err)
	}
}

type tmdbCatalogClientStub struct {
	catalog domain.TMDbSeriesCatalog
}

func (stub *tmdbCatalogClientStub) Series(context.Context, int64) (domain.TMDbSeriesCatalog, error) {
	return stub.catalog, nil
}

type tmdbCatalogStoreStub struct {
	catalog      domain.TMDbSeriesCatalog
	acquisitions []uuid.UUID
}

func (stub *tmdbCatalogStoreStub) SaveTMDbCatalog(_ context.Context, _ domain.Operation, catalog domain.TMDbSeriesCatalog) error {
	stub.catalog = catalog
	return nil
}

func (stub *tmdbCatalogStoreStub) ListAgentMappingAcquisitions(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return stub.acquisitions, nil
}

type tmdbAgentResolutionStub struct {
	store               *tmdbCatalogStoreStub
	reconciledSeries    []uuid.UUID
	created             []service.AutomaticAgentResolutionRequest
	reconciliationErr   error
	catalogWasCommitted bool
}

func (stub *tmdbAgentResolutionStub) CreateAutomatic(
	_ context.Context,
	input service.AutomaticAgentResolutionRequest,
) (service.AgentResolutionCommandResult, error) {
	stub.created = append(stub.created, input)
	return service.AgentResolutionCommandResult{}, nil
}

func (stub *tmdbAgentResolutionStub) ReconcileAutomaticRSSPreacquisitionMappingsForSeries(
	_ context.Context,
	seriesID uuid.UUID,
) (int, error) {
	stub.reconciledSeries = append(stub.reconciledSeries, seriesID)
	stub.catalogWasCommitted = stub.store != nil && stub.store.catalog.TMDbSeriesID > 0
	if stub.reconciliationErr != nil {
		return 0, stub.reconciliationErr
	}
	return 1, nil
}

func TestTMDbSyncHandlerReconcilesPendingRSSMappingsAfterCatalogCommit(t *testing.T) {
	seriesID := uuid.MustParse("82000000-0000-0000-0000-000000000005")
	acquisitionID := uuid.MustParse("82000000-0000-0000-0000-000000000006")
	catalog := domain.TMDbSeriesCatalog{TMDbSeriesID: 43, Name: "Show", Payload: []byte(`{"id":43}`)}
	store := &tmdbCatalogStoreStub{acquisitions: []uuid.UUID{acquisitionID}}
	agent := &tmdbAgentResolutionStub{store: store}
	handler := NewTMDbSyncHandler(
		&catalogConfigurationStub{
			configuration: domain.Configuration{Settings: domain.RuntimeSettings{}},
			secrets:       map[string]string{domain.SecretTMDbAPIToken: "tmdb-token"},
		},
		store,
		func(tmdb.ClientOptions) (TMDbCatalogClient, error) {
			return &tmdbCatalogClientStub{catalog: catalog}, nil
		},
		agent,
	)
	operation := domain.Operation{ID: uuid.New(), ResourceType: "media_series", ResourceID: seriesID, Payload: []byte(`{"tmdbSeriesId":43}`)}
	if err := handler.Handle(context.Background(), operation); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !agent.catalogWasCommitted || len(agent.reconciledSeries) != 1 || agent.reconciledSeries[0] != seriesID {
		t.Fatalf("catalog committed/reconciled series = %v/%v", agent.catalogWasCommitted, agent.reconciledSeries)
	}
	if len(agent.created) != 1 || agent.created[0].Capability != domain.AgentCapabilityEpisodeMapping || agent.created[0].ResourceID != acquisitionID {
		t.Fatalf("legacy Mapping continuation = %#v", agent.created)
	}
}

func TestTMDbSyncHandlerRetriesWhenRSSMappingContinuationFails(t *testing.T) {
	seriesID := uuid.MustParse("82000000-0000-0000-0000-000000000007")
	catalog := domain.TMDbSeriesCatalog{TMDbSeriesID: 44, Name: "Show", Payload: []byte(`{"id":44}`)}
	store := &tmdbCatalogStoreStub{acquisitions: []uuid.UUID{uuid.New()}}
	agent := &tmdbAgentResolutionStub{store: store, reconciliationErr: errors.New("database unavailable")}
	handler := NewTMDbSyncHandler(
		&catalogConfigurationStub{
			configuration: domain.Configuration{Settings: domain.RuntimeSettings{}},
			secrets:       map[string]string{domain.SecretTMDbAPIToken: "tmdb-token"},
		},
		store,
		func(tmdb.ClientOptions) (TMDbCatalogClient, error) {
			return &tmdbCatalogClientStub{catalog: catalog}, nil
		},
		agent,
	)
	err := handler.Handle(context.Background(), domain.Operation{
		ID: uuid.New(), ResourceType: "media_series", ResourceID: seriesID, Payload: []byte(`{"tmdbSeriesId":44}`),
	})
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Retryable || failure.Code != "agent_resolution_schedule_failed" {
		t.Fatalf("Handle() error = %#v", err)
	}
	if !agent.catalogWasCommitted || len(agent.created) != 0 {
		t.Fatalf("catalog committed/downstream Agent calls = %v/%#v", agent.catalogWasCommitted, agent.created)
	}
}

func TestTMDbSyncHandlerLoadsConfiguredCatalog(t *testing.T) {
	seriesID := uuid.MustParse("82000000-0000-0000-0000-000000000004")
	catalog := domain.TMDbSeriesCatalog{TMDbSeriesID: 42, Name: "Show", Payload: []byte(`{"id":42}`)}
	store := &tmdbCatalogStoreStub{}
	handler := NewTMDbSyncHandler(
		&catalogConfigurationStub{
			configuration: domain.Configuration{Settings: domain.RuntimeSettings{NetworkProxy: domain.NetworkProxySettings{
				Enabled: true,
				URL:     "http://127.0.0.1:7890",
			}}},
			secrets: map[string]string{domain.SecretTMDbAPIToken: "tmdb-token"},
		},
		store,
		func(options tmdb.ClientOptions) (TMDbCatalogClient, error) {
			if options.APIToken != "tmdb-token" {
				t.Fatalf("token = %q", options.APIToken)
			}
			transport, ok := options.HTTPClient.Transport.(*http.Transport)
			if !ok || transport.Proxy == nil {
				t.Fatalf("HTTP transport = %#v", options.HTTPClient.Transport)
			}
			proxyURL, err := transport.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "api.themoviedb.org"}})
			if err != nil || proxyURL.String() != "http://127.0.0.1:7890" {
				t.Fatalf("proxy URL = %v, error = %v", proxyURL, err)
			}
			return &tmdbCatalogClientStub{catalog: catalog}, nil
		},
	)
	operation := domain.Operation{ID: uuid.New(), ResourceType: "media_series", ResourceID: seriesID, Payload: []byte(`{"tmdbSeriesId":42}`)}
	if err := handler.Handle(context.Background(), operation); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if store.catalog.TMDbSeriesID != 42 {
		t.Fatalf("stored catalog = %#v", store.catalog)
	}
}
