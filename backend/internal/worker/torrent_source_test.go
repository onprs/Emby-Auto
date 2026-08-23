package worker

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

var testTorrentPayload = []byte("d8:announce35:http://tracker.example.test:8080/announce4:infod6:lengthi123e4:name8:test.mkveee")

func TestValidateTorrentSourceURLRejectsPrivateAndMalformed(t *testing.T) {
	allowPrivateTorrentSourceForTest = false
	invalid := []string{
		"ftp://example.test/file.torrent",
		"http://user:pass@example.test/file.torrent",
		"http://localhost/file.torrent",
		"http://127.0.0.1/file.torrent",
		"http://10.0.0.5/file.torrent",
		"http://192.168.1.10/file.torrent",
		"http://172.16.5.4/file.torrent",
		"http://[::1]/file.torrent",
		"http://[fe80::1]/file.torrent",
		"http://[ff02::1]/file.torrent",
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			err := validateTorrentSourceURL(raw)
			var failure *Failure
			if err == nil {
				t.Fatalf("validateTorrentSourceURL(%q) = nil, want error", raw)
			}
			if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" || failure.Retryable {
				t.Fatalf("error = %#v, want permanent torrent_source_invalid", err)
			}
			if strings.Contains(failure.Message, raw) || (failure.Cause != nil && strings.Contains(failure.Cause.Error(), raw)) {
				t.Fatalf("error leaks raw URL")
			}
		})
	}
	valid := []string{
		"https://cdn.example.test/file.torrent",
		"http://example.test/file.torrent?token=abc",
		"https://8.8.8.8/file.torrent",
	}
	for _, raw := range valid {
		t.Run("valid:"+raw, func(t *testing.T) {
			if err := validateTorrentSourceURL(raw); err != nil {
				t.Fatalf("validateTorrentSourceURL(%q) error = %v, want nil", raw, err)
			}
		})
	}
}

func errorIsFailure(err error, target **Failure) bool {
	for err != nil {
		if f, ok := err.(*Failure); ok {
			*target = f
			return true
		}
		if ue, ok := err.(interface{ Unwrap() error }); ok {
			err = ue.Unwrap()
			continue
		}
		if ue, ok := err.(interface{ Unwrap() []error }); ok {
			for _, e := range ue.Unwrap() {
				if errorIsFailure(e, target) {
					return true
				}
			}
		}
		break
	}
	return false
}

func withAllowPrivate(t *testing.T, allow bool) {
	t.Helper()
	old := allowPrivateTorrentSourceForTest
	allowPrivateTorrentSourceForTest = allow
	t.Cleanup(func() { allowPrivateTorrentSourceForTest = old })
}

func mapHostToServer(t *testing.T, host string, server *httptest.Server) {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("Parse server URL %q: %v", server.URL, err)
	}
	targetAddr := u.Host
	orig := http.DefaultTransport
	transport, ok := orig.(*http.Transport)
	if !ok {
		t.Fatalf("DefaultTransport not *http.Transport")
	}
	clone := transport.Clone()
	oldDial := clone.DialContext
	clone.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, host+":") || addr == host {
			return (&net.Dialer{}).DialContext(ctx, network, targetAddr)
		}
		if oldDial != nil {
			return oldDial(ctx, network, addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	http.DefaultTransport = clone
	t.Cleanup(func() { http.DefaultTransport = orig })
}

func TestDefaultTorrentSourceFetcherFetchesViaProxyAndSanitizes(t *testing.T) {
	withAllowPrivate(t, true)
	var upstreamHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		_, _ = w.Write(testTorrentPayload)
	}))
	defer upstream.Close()

	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if r.Header.Get("User-Agent") != torrentSourceUserAgent {
			t.Errorf("User-Agent = %q, want %q", r.Header.Get("User-Agent"), torrentSourceUserAgent)
		}
		if !strings.Contains(r.Header.Get("Accept"), "application/x-bittorrent") {
			t.Errorf("Accept = %q, want contains application/x-bittorrent", r.Header.Get("Accept"))
		}
		if r.URL.Host != strings.TrimPrefix(upstream.URL, "http://") {
			t.Errorf("proxy request host = %q, want upstream host %q", r.URL.Host, strings.TrimPrefix(upstream.URL, "http://"))
		}
		if r.URL.Path != "/file.torrent" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write(testTorrentPayload)
	}))
	defer proxy.Close()

	bytes, err := defaultTorrentSourceFetcher(context.Background(), upstream.URL+"/file.torrent", domain.NetworkProxySettings{Enabled: true, URL: proxy.URL})
	if err != nil {
		t.Fatalf("fetcher error = %v", err)
	}
	if string(bytes) != string(testTorrentPayload) {
		t.Fatalf("bytes = %q, want %q", bytes, testTorrentPayload)
	}
	if proxyHits.Load() != 1 {
		t.Fatalf("proxyHits=%d, want 1", proxyHits.Load())
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("upstream should not be hit directly when via proxy, hits=%d", upstreamHits.Load())
	}
}

func TestDefaultTorrentSourceFetcherDirectDoesNotUseEnvProxy(t *testing.T) {
	withAllowPrivate(t, true)
	var proxyHits atomic.Int32
	envProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
	}))
	defer envProxy.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(testTorrentPayload)
	}))
	defer target.Close()

	original := http.DefaultTransport
	http.DefaultTransport = &http.Transport{Proxy: http.ProxyURL(mustParseURLProxy(t, envProxy.URL))}
	t.Cleanup(func() { http.DefaultTransport = original })

	bytes, err := defaultTorrentSourceFetcher(context.Background(), target.URL+"/file.torrent", domain.NetworkProxySettings{Enabled: false})
	if err != nil {
		t.Fatalf("fetcher error = %v", err)
	}
	if string(bytes) != string(testTorrentPayload) {
		t.Fatalf("bytes mismatch")
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("env proxy was used despite disabled settings, hits=%d", proxyHits.Load())
	}
}

func mustParseURLProxy(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", raw, err)
	}
	return u
}

func TestDefaultTorrentSourceFetcherClassifiesHTTPStatuses(t *testing.T) {
	withAllowPrivate(t, true)
	tests := []struct {
		name          string
		status        int
		wantCode      string
		wantRetryable bool
	}{
		{name: "200 ok", status: 200, wantCode: "", wantRetryable: false},
		{name: "429 retryable", status: 429, wantCode: "torrent_source_unavailable", wantRetryable: true},
		{name: "500 retryable", status: 500, wantCode: "torrent_source_unavailable", wantRetryable: true},
		{name: "502 retryable", status: 502, wantCode: "torrent_source_unavailable", wantRetryable: true},
		{name: "404 permanent", status: 404, wantCode: "torrent_source_unavailable", wantRetryable: false},
		{name: "403 permanent", status: 403, wantCode: "torrent_source_unavailable", wantRetryable: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status == 200 {
					_, _ = w.Write(testTorrentPayload)
					return
				}
				http.Error(w, "error", tc.status)
			}))
			defer server.Close()
			bytes, err := defaultTorrentSourceFetcher(context.Background(), server.URL+"/file.torrent", domain.NetworkProxySettings{})
			if tc.status == 200 {
				if err != nil || string(bytes) != string(testTorrentPayload) {
					t.Fatalf("err=%v bytes=%q", err, bytes)
				}
				return
			}
			var failure *Failure
			if !errorIsFailure(err, &failure) || failure.Code != tc.wantCode || failure.Retryable != tc.wantRetryable {
				t.Fatalf("error = %#v, want %q retryable=%v", err, tc.wantCode, tc.wantRetryable)
			}
			if strings.Contains(failure.Message, server.URL) {
				t.Fatalf("failure leaks URL")
			}
		})
	}
}

func TestDefaultTorrentSourceFetcherRejectsPrivateRedirect(t *testing.T) {
	withAllowPrivate(t, false)
	privateHit := false
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		privateHit = true
		_, _ = w.Write(testTorrentPayload)
	}))
	defer private.Close()

	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL+"/file.torrent", http.StatusFound)
	}))
	defer redirecting.Close()

	// 将 redirecting 的 127.0.0.1 映射为非私有域名，避免初始请求被直接拒绝
	mapHostToServer(t, "redirect.example.test", redirecting)
	u, _ := url.Parse(redirecting.URL)
	redirectURL := "http://redirect.example.test:" + u.Port() + "/file.torrent"

	_, err := defaultTorrentSourceFetcher(context.Background(), redirectURL, domain.NetworkProxySettings{})
	var failure *Failure
	if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" || failure.Retryable {
		t.Fatalf("redirect to private should be permanent invalid, got %#v", err)
	}
	if privateHit {
		t.Fatalf("private server should not be hit after redirect validation")
	}
}

func TestDefaultTorrentSourceFetcherLimitsRedirects(t *testing.T) {
	withAllowPrivate(t, true)
	chain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.String(), http.StatusFound)
	}))
	defer chain.Close()
	_, err := defaultTorrentSourceFetcher(context.Background(), chain.URL+"/file.torrent", domain.NetworkProxySettings{})
	var failure *Failure
	if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" {
		t.Fatalf("too many redirects should be invalid, got %#v", err)
	}
}

func TestDefaultTorrentSourceFetcherValidatesBodyConstraints(t *testing.T) {
	withAllowPrivate(t, true)
	tests := []struct {
		name          string
		handler       func(w http.ResponseWriter, r *http.Request)
		wantCode      string
		wantRetryable bool
	}{
		{
			name: "too large via ContentLength",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "17000000")
				_, _ = w.Write([]byte("d8:announce4:test4:infod3:foo3:bareee"))
			},
			wantCode: "torrent_source_too_large", wantRetryable: false,
		},
		{
			name: "too large via body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				large := make([]byte, torrentSourceMaxBytes+1)
				for i := range large {
					large[i] = 'a'
				}
				large[0] = 'd'
				copy(large[1:], []byte("8:announce4:info"))
				_, _ = w.Write(large)
			},
			wantCode: "torrent_source_too_large", wantRetryable: false,
		},
		{
			name:     "empty body",
			handler:  func(w http.ResponseWriter, r *http.Request) {},
			wantCode: "torrent_source_not_torrent", wantRetryable: false,
		},
		{
			name: "non bencode",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("not a torrent"))
			},
			wantCode: "torrent_source_not_torrent", wantRetryable: false,
		},
		{
			name: "bencode but missing announce/info",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("d3:foo3:baree"))
			},
			wantCode: "torrent_source_not_torrent", wantRetryable: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tc.handler))
			defer server.Close()
			_, err := defaultTorrentSourceFetcher(context.Background(), server.URL+"/file.torrent", domain.NetworkProxySettings{})
			var failure *Failure
			if !errorIsFailure(err, &failure) || failure.Code != tc.wantCode || failure.Retryable != tc.wantRetryable {
				t.Fatalf("error = %#v, want %q retryable=%v", err, tc.wantCode, tc.wantRetryable)
			}
		})
	}
}

func TestDefaultTorrentSourceFetcherHandlesTimeoutAsRetryable(t *testing.T) {
	withAllowPrivate(t, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write(testTorrentPayload)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := defaultTorrentSourceFetcher(ctx, server.URL+"/file.torrent", domain.NetworkProxySettings{})
	var failure *Failure
	if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_unavailable" || !failure.Retryable {
		t.Fatalf("timeout should be retryable unavailable, got %#v", err)
	}
}

func TestIsTorrentBytesDetectsValidFixture(t *testing.T) {
	if !isTorrentBytes(testTorrentPayload) {
		t.Fatal("testTorrentPayload should be valid torrent bytes")
	}
	if isTorrentBytes([]byte("")) || isTorrentBytes([]byte("not torrent")) || isTorrentBytes([]byte("d3:foo3:baree")) {
		t.Fatal("invalid payloads should not be torrent")
	}
}
