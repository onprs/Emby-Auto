package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type timeoutConnectivityResolver struct {
	configuration domain.Configuration
}

func (resolver timeoutConnectivityResolver) Load(context.Context) (domain.Configuration, error) {
	return resolver.configuration, nil
}

func (timeoutConnectivityResolver) ResolveSecret(context.Context, string) (string, error) {
	return "fixture-token", nil
}

type recordingConnectivityResultStore struct {
	called     bool
	contextErr error
	params     db.UpsertConnectivityTestResultParams
}

func (store *recordingConnectivityResultStore) UpsertConnectivityTestResult(
	ctx context.Context,
	params db.UpsertConnectivityTestResultParams,
) (db.ConnectivityTestResult, error) {
	store.called = true
	store.contextErr = ctx.Err()
	store.params = params
	return db.ConnectivityTestResult{}, nil
}

func TestNetworkProxyConnectivityUsesUnsavedCandidateSettings(t *testing.T) {
	var calls int
	proxy := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.URL.Host != "tmdb.example.test" || request.URL.Path != "/3/configuration" {
			t.Fatalf("proxied URL = %s", request.URL.String())
		}
		_, _ = response.Write([]byte(`{}`))
	}))
	defer proxy.Close()

	connectivity := NewConnectivityService(timeoutConnectivityResolver{}, nil, nil, "http://tmdb.example.test/3")
	result, err := connectivity.Test(context.Background(), domain.ConnectivityTestRequest{
		Target: "network_proxy",
		NetworkProxy: &domain.NetworkProxySettings{
			Enabled: true,
			URL:     proxy.URL,
		},
	})
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if !result.Success || result.Code != "ok" || calls != 1 {
		t.Fatalf("result = %#v, proxy calls = %d", result, calls)
	}
}

func TestNetworkProxyConnectivityRequiresEnabledCandidateSettings(t *testing.T) {
	connectivity := NewConnectivityService(timeoutConnectivityResolver{}, nil, nil)
	for _, request := range []domain.ConnectivityTestRequest{
		{Target: "network_proxy"},
		{Target: "network_proxy", NetworkProxy: &domain.NetworkProxySettings{}},
	} {
		_, err := connectivity.Test(context.Background(), request)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Test(%#v) error = %v, want ErrInvalidInput", request, err)
		}
	}
}

func TestConnectivityTimeoutPersistsFailureWithParentContext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer upstream.Close()

	service := NewConnectivityService(timeoutConnectivityResolver{}, nil, nil, upstream.URL)
	service.requestTimeout = 25 * time.Millisecond
	store := &recordingConnectivityResultStore{}
	service.results = store

	result, err := service.Test(context.Background(), domain.ConnectivityTestRequest{Target: "tmdb"})
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if result.Success || result.Code != "connection_failed" || !strings.Contains(result.Message, "context deadline exceeded") {
		t.Fatalf("Test() result = %#v, want persisted timeout failure", result)
	}
	if !store.called || store.contextErr != nil {
		t.Fatalf("result store called = %t, context error = %v", store.called, store.contextErr)
	}
	if store.params.Target != "tmdb" || store.params.Success || store.params.Code != "connection_failed" {
		t.Fatalf("persisted result = %#v", store.params)
	}
}
