package proxyhttp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestNewClientRoutesEnabledRequestsThroughConfiguredProxy(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Host != "upstream.example.test" {
			t.Fatalf("proxied request host = %q", request.URL.Host)
		}
		_, _ = response.Write([]byte("proxied"))
	}))
	defer proxy.Close()

	client, err := NewClient(domain.NetworkProxySettings{Enabled: true, URL: proxy.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	response, err := client.Get("http://upstream.example.test/resource")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "proxied" || calls.Load() != 1 {
		t.Fatalf("response = %q, proxy calls = %d", body, calls.Load())
	}
}

func TestNewClientDisablesInheritedDefaultTransportProxy(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		proxyCalls.Add(1)
	}))
	defer proxy.Close()
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("direct"))
	}))
	defer target.Close()

	original := http.DefaultTransport
	http.DefaultTransport = &http.Transport{Proxy: http.ProxyURL(mustParseURL(t, proxy.URL))}
	t.Cleanup(func() { http.DefaultTransport = original })

	client, err := NewClient(domain.NetworkProxySettings{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	response, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	if string(body) != "direct" || proxyCalls.Load() != 0 {
		t.Fatalf("response = %q, inherited proxy calls = %d", body, proxyCalls.Load())
	}
}

func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	return parsed
}

func TestNewClientRejectsInvalidEnabledProxy(t *testing.T) {
	for _, settings := range []domain.NetworkProxySettings{
		{Enabled: true},
		{Enabled: true, URL: "socks5://127.0.0.1:1080"},
		{Enabled: true, URL: "http://user:password@127.0.0.1:8080"},
	} {
		if _, err := NewClient(settings); err == nil {
			t.Fatalf("NewClient(%#v) error = nil", settings)
		}
	}
}
