package worker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/proxyhttp"
)

var testTorrentPayload = []byte("d8:announce35:http://tracker.example.test:8080/announce4:infod6:lengthi123e4:name8:test.mkveee")

const sensitiveTorrentQueryFixture = "s3cr3t-t0k3n-xyz"

// 辅助：创建带局部 allowPrivate 的 fetcher，不修改全局状态。
func fetcherWithAllowPrivate(allowPrivate bool) torrentSourceFetcher {
	return func(ctx context.Context, rawURL string, settings domain.NetworkProxySettings) ([]byte, error) {
		return fetchTorrentSource(ctx, rawURL, settings, torrentSourceOptions{allowPrivate: allowPrivate})
	}
}

// 辅助：通过注入 newClient 实现 host->addr 的局部映射，避免修改 http.DefaultTransport 全局状态。
func fetcherWithMapping(mapping map[string]string, allowPrivate bool) torrentSourceFetcher {
	return func(ctx context.Context, rawURL string, settings domain.NetworkProxySettings) ([]byte, error) {
		opts := torrentSourceOptions{
			allowPrivate: allowPrivate,
			newClient: func(ps domain.NetworkProxySettings) (*http.Client, error) {
				base, _ := http.DefaultTransport.(*http.Transport)
				var tr *http.Transport
				if base != nil {
					tr = base.Clone()
				} else {
					tr = &http.Transport{}
				}
				tr.Proxy = nil
				if ps.Enabled {
					if strings.TrimSpace(ps.URL) == "" {
						return nil, fmt.Errorf("network proxy URL is required when enabled")
					}
					u, err := url.Parse(ps.URL)
					if err != nil {
						return nil, err
					}
					tr.Proxy = http.ProxyURL(u)
				}
				origDial := tr.DialContext
				tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					for host, target := range mapping {
						if strings.HasPrefix(addr, host+":") || addr == host {
							if origDial != nil {
								// 尝试用 origDial 拨目标，避免直接 net.Dial 丢失上下文
								// 若 origDial 失败则回退到 Dialer
								conn, err := origDial(ctx, network, target)
								if err == nil {
									return conn, nil
								}
							}
							return (&net.Dialer{}).DialContext(ctx, network, target)
						}
					}
					if origDial != nil {
						return origDial(ctx, network, addr)
					}
					return (&net.Dialer{}).DialContext(ctx, network, addr)
				}
				return &http.Client{Transport: tr}, nil
			},
		}
		return fetchTorrentSource(ctx, rawURL, settings, opts)
	}
}

// 模拟 Do 错误的 RoundTripper，错误中包含完整 URL，用于验证脱敏。
type failingRoundTripper struct {
	errWithURL error
}

func (f *failingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, f.errWithURL
}

func fetcherWithFailingTransport(errWithURL error) torrentSourceFetcher {
	return func(ctx context.Context, rawURL string, settings domain.NetworkProxySettings) ([]byte, error) {
		opts := torrentSourceOptions{
			allowPrivate: true,
			newClient: func(ps domain.NetworkProxySettings) (*http.Client, error) {
				return &http.Client{Transport: &failingRoundTripper{errWithURL: errWithURL}}, nil
			},
		}
		return fetchTorrentSource(ctx, rawURL, settings, opts)
	}
}

func TestValidateTorrentSourceURLRejectsPrivateAndMalformed(t *testing.T) {
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
			err := validateTorrentSourceURL(raw, torrentSourceOptions{allowPrivate: false})
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
			if err := validateTorrentSourceURL(raw, torrentSourceOptions{allowPrivate: false}); err != nil {
				t.Fatalf("validateTorrentSourceURL(%q) error = %v, want nil", raw, err)
			}
		})
	}
}

func TestValidateTorrentSourceURLRejectsLocalhostVariants(t *testing.T) {
	// 本轮仅做字面量归一化阻断，不做 DNS 解析；明确边界：DNS 重绑定不在本轮防护范围。
	invalid := []string{
		"http://localhost./file.torrent",
		"http://LOCALHOST./file.torrent",
		"http://localhost.:8080/file.torrent",
		"http://foo.localhost/file.torrent",
		"http://bar.foo.localhost/file.torrent",
		"http://example.localhost/file.torrent",
		"http://foo.localhost./file.torrent",
		"http://FOO.LOCALHOST/file.torrent",
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			err := validateTorrentSourceURL(raw, torrentSourceOptions{allowPrivate: false})
			var failure *Failure
			if err == nil {
				t.Fatalf("validateTorrentSourceURL(%q) = nil, want error", raw)
			}
			if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" {
				t.Fatalf("error = %#v, want torrent_source_invalid", err)
			}
		})
	}
	valid := []string{
		"http://localhost.example.test/file.torrent",
		"http://example.test/file.torrent",
	}
	for _, raw := range valid {
		t.Run("valid:"+raw, func(t *testing.T) {
			if err := validateTorrentSourceURL(raw, torrentSourceOptions{allowPrivate: false}); err != nil {
				t.Fatalf("validateTorrentSourceURL(%q) unexpected error %v", raw, err)
			}
		})
	}
}

func TestValidateTorrentSourceURLRejectsPrivateIPWithTrailingDot(t *testing.T) {
	invalid := []string{
		"http://127.0.0.1./file.torrent",
		"http://10.0.0.5./file.torrent",
		"http://192.168.1.1./file.torrent",
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			err := validateTorrentSourceURL(raw, torrentSourceOptions{allowPrivate: false})
			var failure *Failure
			if err == nil || !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" {
				t.Fatalf("validateTorrentSourceURL(%q) = %#v, want blocked", raw, err)
			}
		})
	}
}

func TestValidateTorrentSourceURLRejectsZoneScopedIPv6(t *testing.T) {
	// 验证带 zone 的 IPv6 字面量 link-local/loopback 仍被阻断，不依赖 net.ParseIP 字符串截断
	invalid := []string{
		"http://[fe80::1%25en0]/file.torrent",
		"http://[fe80::1%25zone]/file.torrent",
		"http://[fe80::1%25eth0]:8080/file.torrent",
		"http://[fe80::1234:5678:abcd:ef12%25en0]/file.torrent",
		"http://[::1%25lo0]/file.torrent",
		"http://[fe80::1%25en0]/file.torrent?token=" + sensitiveTorrentQueryFixture,
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			err := validateTorrentSourceURL(raw, torrentSourceOptions{allowPrivate: false})
			var failure *Failure
			if err == nil || !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" || failure.Retryable {
				t.Fatalf("validateTorrentSourceURL(%q) = %#v, want permanent torrent_source_invalid", raw, err)
			}
			if strings.Contains(failure.Message, sensitiveTorrentQueryFixture) || (failure.Cause != nil && strings.Contains(failure.Cause.Error(), sensitiveTorrentQueryFixture)) {
				t.Fatalf("error leaks sensitive fixture")
			}
		})
	}
}

func TestValidateTorrentSourceURLRejectsIPv4MappedIPv6(t *testing.T) {
	// IPv4-mapped IPv6 字面量底层仍按 IPv4 语义阻断，需先 Unmap 再判定
	invalid := []string{
		"http://[::ffff:127.0.0.1]/file.torrent",
		"http://[::ffff:127.0.0.1]:8080/file.torrent",
		"http://[::ffff:10.0.0.1]/file.torrent",
		"http://[::ffff:10.255.255.255]/file.torrent",
		"http://[::ffff:192.168.1.1]/file.torrent",
		"http://[::ffff:172.16.5.4]/file.torrent",
		"http://[::ffff:172.31.255.255]/file.torrent",
		"http://[::ffff:169.254.10.20]/file.torrent",
		"http://[::ffff:0.0.0.0]/file.torrent",
		"http://[::ffff:224.0.0.1]/file.torrent",
		"http://[::ffff:10.0.0.1]/file.torrent?token=" + sensitiveTorrentQueryFixture,
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			err := validateTorrentSourceURL(raw, torrentSourceOptions{allowPrivate: false})
			var failure *Failure
			if err == nil || !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" || failure.Retryable {
				t.Fatalf("validateTorrentSourceURL(%q) = %#v, want permanent torrent_source_invalid", raw, err)
			}
			if strings.Contains(failure.Message, sensitiveTorrentQueryFixture) || (failure.Cause != nil && strings.Contains(failure.Cause.Error(), sensitiveTorrentQueryFixture)) {
				t.Fatalf("错误泄露敏感信息")
			}
			if failure.Cause != nil && (strings.Contains(failure.Cause.Error(), raw) || strings.Contains(failure.Cause.Error(), "::ffff")) {
				t.Fatalf("错误泄露原始地址")
			}
		})
	}
}

func TestValidateTorrentSourceURLAllowsPublicIPv4MappedIPv6(t *testing.T) {
	// 公共 IPv4 映射不应被误拒绝，验证 Unmap 后仅阻断私有/保留段
	valid := []string{
		"http://[::ffff:8.8.8.8]/file.torrent",
		"https://[::ffff:1.1.1.1]/file.torrent",
		"http://[::ffff:8.8.4.4]:8080/file.torrent?token=abc",
		"http://[::ffff:9.9.9.9]/file.torrent",
		"http://[::ffff:1.0.0.1]/file.torrent",
	}
	for _, raw := range valid {
		t.Run(raw, func(t *testing.T) {
			if err := validateTorrentSourceURL(raw, torrentSourceOptions{allowPrivate: false}); err != nil {
				t.Fatalf("validateTorrentSourceURL(%q) unexpected error %v, want nil", raw, err)
			}
		})
	}
	// 普通公共 IPv6 仍放行，保持原有行为不变
	if err := validateTorrentSourceURL("http://[2001:4860:4860::8888]/file.torrent", torrentSourceOptions{allowPrivate: false}); err != nil {
		t.Fatalf("公共 IPv6 不应被拒绝, got %v", err)
	}
}

func TestDefaultTorrentSourceFetcherRejectsMappedLoopbackRedirect(t *testing.T) {
	// 重定向到 IPv4-mapped loopback 应在 CheckRedirect 阶段永久拒绝，且不实际拨号目标
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://[::ffff:127.0.0.1]/file.torrent", http.StatusFound)
	}))
	defer redirecting.Close()
	u, _ := url.Parse(redirecting.URL)
	mapping := map[string]string{"redirect.example.test": u.Host}
	fetcher := fetcherWithMapping(mapping, false)
	redirectURL := "http://redirect.example.test:" + u.Port() + "/file.torrent"
	_, err := fetcher(context.Background(), redirectURL, domain.NetworkProxySettings{})
	var failure *Failure
	if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" || failure.Retryable {
		t.Fatalf("重定向到 mapped loopback 应为永久拒绝, got %#v", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "::ffff") || (failure.Cause != nil && (strings.Contains(failure.Cause.Error(), "127.0.0.1") || strings.Contains(failure.Cause.Error(), "::ffff"))) {
		t.Fatalf("重定向错误泄露原始地址: %v", err)
	}
}

func TestDefaultTorrentSourceFetcherRejectsMappedPrivateRedirect(t *testing.T) {
	// 重定向到 IPv4-mapped 私网/链路本地应同样永久拒绝，验证 dial 前阻断
	cases := []string{
		"http://[::ffff:10.0.0.1]/file.torrent",
		"http://[::ffff:192.168.1.1]/file.torrent",
		"http://[::ffff:172.16.5.4]/file.torrent",
		"http://[::ffff:169.254.10.20]/file.torrent?token=" + sensitiveTorrentQueryFixture,
	}
	for _, target := range cases {
		t.Run(target, func(t *testing.T) {
			redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target, http.StatusFound)
			}))
			defer redirecting.Close()
			u, _ := url.Parse(redirecting.URL)
			mapping := map[string]string{"redirect.example.test": u.Host}
			fetcher := fetcherWithMapping(mapping, false)
			redirectURL := "http://redirect.example.test:" + u.Port() + "/file.torrent"
			_, err := fetcher(context.Background(), redirectURL, domain.NetworkProxySettings{})
			var failure *Failure
			if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" || failure.Retryable {
				t.Fatalf("重定向到 %q 应为永久拒绝, got %#v", target, err)
			}
			if strings.Contains(err.Error(), sensitiveTorrentQueryFixture) || (failure.Cause != nil && strings.Contains(failure.Cause.Error(), sensitiveTorrentQueryFixture)) {
				t.Fatalf("重定向错误泄露敏感信息: %v", err)
			}
			if strings.Contains(err.Error(), "::ffff") || (failure.Cause != nil && strings.Contains(failure.Cause.Error(), "::ffff")) {
				t.Fatalf("重定向错误泄露原始地址: %v", err)
			}
		})
	}
}

func TestDefaultTorrentSourceFetcherRejectsMappedInitial(t *testing.T) {
	// 初始 URL 为 IPv4-mapped 私网/回环，应在请求发出前永久拒绝且不泄露敏感信息
	invalid := "http://[::ffff:10.0.0.1]/file.torrent?token=" + sensitiveTorrentQueryFixture
	_, err := fetchTorrentSource(context.Background(), invalid, domain.NetworkProxySettings{}, torrentSourceOptions{allowPrivate: false})
	var failure *Failure
	if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" || failure.Retryable {
		t.Fatalf("mapped 初始 URL 应为永久拒绝, got %#v", err)
	}
	if strings.Contains(err.Error(), sensitiveTorrentQueryFixture) || (failure.Cause != nil && strings.Contains(failure.Cause.Error(), sensitiveTorrentQueryFixture)) {
		t.Fatalf("初始错误泄露敏感信息: %v", err)
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

func TestDefaultTorrentSourceFetcherFetchesViaProxyAndSanitizes(t *testing.T) {
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

	fetcher := fetcherWithAllowPrivate(true)
	bytes, err := fetcher(context.Background(), upstream.URL+"/file.torrent", domain.NetworkProxySettings{Enabled: true, URL: proxy.URL})
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
	// 对生产 proxyhttp.NewClient 禁用代理的 transport 做局部结构断言，不修改全局 transport/env
	client, err := proxyhttp.NewClient(domain.NetworkProxySettings{Enabled: false})
	if err != nil {
		t.Fatalf("proxyhttp.NewClient disabled error = %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client.Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		req := &http.Request{URL: mustParseURLProxy(t, "http://example.test/file.torrent")}
		if got, _ := transport.Proxy(req); got != nil {
			t.Fatalf("disabled transport proxy should be nil, got %v", got)
		}
	}
	// 本地直连验证：使用真实生产 client 路径（allowPrivate 仅为测试本地服务器直连，不依赖 env proxy）
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(testTorrentPayload)
	}))
	defer target.Close()
	fetcher := fetcherWithAllowPrivate(true)
	data, err := fetcher(context.Background(), target.URL+"/file.torrent", domain.NetworkProxySettings{Enabled: false})
	if err != nil {
		t.Fatalf("direct fetcher error = %v", err)
	}
	if string(data) != string(testTorrentPayload) {
		t.Fatalf("direct fetch bytes mismatch")
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
			fetcher := fetcherWithAllowPrivate(true)
			bytes, err := fetcher(context.Background(), server.URL+"/file.torrent", domain.NetworkProxySettings{})
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

	// 将 redirecting 的 127.0.0.1 映射为非私有域名，避免初始请求被直接拒绝；使用局部注入而非全局 DefaultTransport
	u, _ := url.Parse(redirecting.URL)
	mapping := map[string]string{"redirect.example.test": u.Host}
	fetcher := fetcherWithMapping(mapping, false)
	redirectURL := "http://redirect.example.test:" + u.Port() + "/file.torrent"

	_, err := fetcher(context.Background(), redirectURL, domain.NetworkProxySettings{})
	var failure *Failure
	if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" || failure.Retryable {
		t.Fatalf("redirect to private should be permanent invalid, got %#v", err)
	}
	if privateHit {
		t.Fatalf("private server should not be hit after redirect validation")
	}
}

func TestDefaultTorrentSourceFetcherRejectsLocalhostRedirect(t *testing.T) {
	// 验证重定向到 localhost. 变体也被拒绝
	privateHit := false
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		privateHit = true
		_, _ = w.Write(testTorrentPayload)
	}))
	defer private.Close()

	// redirect 目标使用 localhost. 形式
	uPrivate, _ := url.Parse(private.URL)
	localhostTarget := "http://localhost.:" + uPrivate.Port() + "/file.torrent"

	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, localhostTarget, http.StatusFound)
	}))
	defer redirecting.Close()

	uRedirect, _ := url.Parse(redirecting.URL)
	mapping := map[string]string{"redirect.example.test": uRedirect.Host}
	fetcher := fetcherWithMapping(mapping, false)
	redirectURL := "http://redirect.example.test:" + uRedirect.Port() + "/file.torrent"

	_, err := fetcher(context.Background(), redirectURL, domain.NetworkProxySettings{})
	var failure *Failure
	if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" {
		t.Fatalf("redirect to localhost. should be invalid, got %#v", err)
	}
	if privateHit {
		t.Fatalf("localhost redirect target should not be hit")
	}
}

func TestDefaultTorrentSourceFetcherRejectsZoneScopedInitial(t *testing.T) {
	// 初始 URL 为 zone-scoped link-local，应在发出请求前被永久拒绝，且不泄露敏感信息，不实际连接该地址
	invalid := "http://[fe80::1%25en0]/file.torrent?token=" + sensitiveTorrentQueryFixture
	_, err := fetchTorrentSource(context.Background(), invalid, domain.NetworkProxySettings{}, torrentSourceOptions{allowPrivate: false})
	var failure *Failure
	if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" || failure.Retryable {
		t.Fatalf("zone-scoped initial should be permanent invalid, got %#v", err)
	}
	if strings.Contains(err.Error(), sensitiveTorrentQueryFixture) || (failure.Cause != nil && strings.Contains(failure.Cause.Error(), sensitiveTorrentQueryFixture)) {
		t.Fatalf("zone initial error leaks fixture: %v", err)
	}
}

func TestDefaultTorrentSourceFetcherRejectsZoneScopedRedirect(t *testing.T) {
	// 重定向到 zone-scoped link-local，应在 CheckRedirect 中被拒绝，不实际连接目标
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://[fe80::1%25en0]/file.torrent", http.StatusFound)
	}))
	defer redirecting.Close()
	u, _ := url.Parse(redirecting.URL)
	mapping := map[string]string{"redirect.example.test": u.Host}
	fetcher := fetcherWithMapping(mapping, false)
	redirectURL := "http://redirect.example.test:" + u.Port() + "/file.torrent"
	_, err := fetcher(context.Background(), redirectURL, domain.NetworkProxySettings{})
	var failure *Failure
	if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" || failure.Retryable {
		t.Fatalf("zone redirect should be permanent invalid, got %#v", err)
	}
	if strings.Contains(err.Error(), "fe80") || (failure.Cause != nil && strings.Contains(failure.Cause.Error(), "fe80")) {
		t.Fatalf("zone redirect error leaks URL: %v", err)
	}
}

func TestDefaultTorrentSourceFetcherLimitsRedirects(t *testing.T) {
	chain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.String(), http.StatusFound)
	}))
	defer chain.Close()
	fetcher := fetcherWithAllowPrivate(true)
	_, err := fetcher(context.Background(), chain.URL+"/file.torrent", domain.NetworkProxySettings{})
	var failure *Failure
	if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_invalid" {
		t.Fatalf("too many redirects should be invalid, got %#v", err)
	}
}

func TestDefaultTorrentSourceFetcherValidatesBodyConstraints(t *testing.T) {
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
			fetcher := fetcherWithAllowPrivate(true)
			_, err := fetcher(context.Background(), server.URL+"/file.torrent", domain.NetworkProxySettings{})
			var failure *Failure
			if !errorIsFailure(err, &failure) || failure.Code != tc.wantCode || failure.Retryable != tc.wantRetryable {
				t.Fatalf("error = %#v, want %q retryable=%v", err, tc.wantCode, tc.wantRetryable)
			}
		})
	}
}

func TestDefaultTorrentSourceFetcherHandlesTimeoutAsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write(testTorrentPayload)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	fetcher := fetcherWithAllowPrivate(true)
	_, err := fetcher(ctx, server.URL+"/file.torrent", domain.NetworkProxySettings{})
	var failure *Failure
	if !errorIsFailure(err, &failure) || failure.Code != "torrent_source_unavailable" || !failure.Retryable {
		t.Fatalf("timeout should be retryable unavailable, got %#v", err)
	}
	// 超时错误不应泄露 URL
	if strings.Contains(err.Error(), server.URL) || (failure.Cause != nil && strings.Contains(failure.Cause.Error(), server.URL)) {
		t.Fatalf("timeout error leaks URL: %v cause=%v", err, failure.Cause)
	}
}

func TestTorrentSourceFetcherSanitizesLeakOnNetworkError(t *testing.T) {
	sensitiveURL := "https://" + "example.test" + "/file.torrent?token=" + sensitiveTorrentQueryFixture + "&user=me"
	// 构造包含敏感 URL 的原始错误
	rawErr := fmt.Errorf("Get %q: dial tcp 1.2.3.4:443: connect: connection refused", sensitiveURL)
	fetcher := fetcherWithFailingTransport(rawErr)
	_, err := fetcher(context.Background(), sensitiveURL, domain.NetworkProxySettings{})
	var failure *Failure
	if !errorIsFailure(err, &failure) {
		t.Fatalf("error = %v, want Failure", err)
	}
	combined := err.Error()
	if strings.Contains(combined, sensitiveTorrentQueryFixture) {
		t.Fatalf("err.Error() leaks sensitive fixture %q in %q", sensitiveTorrentQueryFixture, combined)
	}
	if strings.Contains(combined, "example.test") {
		t.Fatalf("err.Error() leaks host in %q", combined)
	}
	if failure.Cause != nil && strings.Contains(failure.Cause.Error(), sensitiveTorrentQueryFixture) {
		t.Fatalf("failure.Cause leaks sensitive fixture: %v", failure.Cause)
	}
	if failure.Message != "暂时无法获取种子文件" {
		t.Fatalf("unexpected message %q", failure.Message)
	}
}

func TestTorrentSourceFetcherSanitizesLeakOnTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write(testTorrentPayload)
	}))
	defer server.Close()
	urlWithSensitive := server.URL + "/file.torrent?token=" + sensitiveTorrentQueryFixture
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	fetcher := fetcherWithAllowPrivate(true)
	_, err := fetcher(ctx, urlWithSensitive, domain.NetworkProxySettings{})
	var failure *Failure
	if !errorIsFailure(err, &failure) {
		t.Fatalf("error = %#v, want Failure", err)
	}
	if strings.Contains(err.Error(), sensitiveTorrentQueryFixture) {
		t.Fatalf("timeout err leaks fixture: %v", err)
	}
	if failure.Cause != nil && strings.Contains(failure.Cause.Error(), sensitiveTorrentQueryFixture) {
		t.Fatalf("timeout cause leaks fixture: %v", failure.Cause)
	}
}

func TestTorrentSourceFetcherSanitizesLeakOnInvalidURL(t *testing.T) {
	invalidWithSensitive := "http://[::1" + "/file.torrent?token=" + sensitiveTorrentQueryFixture
	// 该 URL 解析失败，错误中可能包含原始字符串
	_, err := fetchTorrentSource(context.Background(), invalidWithSensitive, domain.NetworkProxySettings{}, torrentSourceOptions{allowPrivate: false})
	var failure *Failure
	if !errorIsFailure(err, &failure) {
		t.Fatalf("error = %#v, want Failure", err)
	}
	if strings.Contains(err.Error(), sensitiveTorrentQueryFixture) {
		t.Fatalf("invalid URL err leaks fixture: %v", err)
	}
	if failure.Cause != nil && strings.Contains(failure.Cause.Error(), sensitiveTorrentQueryFixture) {
		t.Fatalf("invalid URL cause leaks fixture: %v", failure.Cause)
	}
	if failure.Cause != nil && strings.Contains(failure.Cause.Error(), "[::1") {
		t.Fatalf("invalid URL cause leaks host: %v", failure.Cause)
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

func TestFailureErrorDoesNotLeakWhenCauseIsSanitized(t *testing.T) {
	// 直接构造 Failure，验证 Error() 拼接后不含敏感信息（即使 Cause 是稳定错误）
	failure := retryableFailure("torrent_source_unavailable", "暂时无法获取种子文件", errTorrentSourceRequestFailed)
	if strings.Contains(failure.Error(), sensitiveTorrentQueryFixture) {
		t.Fatalf("failure error leaks")
	}
	if failure.Cause != nil && failure.Cause.Error() != errTorrentSourceRequestFailed.Error() {
		t.Fatalf("unexpected cause")
	}
}
