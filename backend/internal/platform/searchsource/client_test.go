package searchsource

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

type staticResolver map[string][]netip.Addr

func (resolver staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	addresses, ok := resolver[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return addresses, nil
}

func testDialer(routes map[string]string) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		target, ok := routes[address]
		if !ok {
			return nil, fmt.Errorf("unexpected dial target %s", address)
		}
		return dialer.DialContext(ctx, network, target)
	}
}

func TestSearchResolvesDetailsDeduplicatesCandidatesAndKeepsPartialFailures(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/dmhy/search", func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("q") != "Canonical Show" {
			t.Fatalf("query = %q", request.URL.Query().Get("q"))
		}
		_, _ = response.Write([]byte(`<html><body>
			<a href="/topics/view/42">Canonical Show 01 1080p</a>
			<a href="/topics/view/42">Canonical Show 01 1080p</a>
			<a href="/files/direct.torrent">Canonical Show 02 1080p</a>
		</body></html>`))
	})
	mux.HandleFunc("/topics/view/42", func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<a href="magnet:?xt=urn:btih:` + hash + `&amp;dn=Canonical">Magnet</a>`))
	})
	mux.HandleFunc("/broken/search", func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "upstream unavailable", http.StatusBadGateway)
	})

	client, err := NewClient(ClientOptions{
		HTTPClient: server.Client(),
		Providers: []Provider{
			{Name: "dmhy", SearchURLTemplate: server.URL + "/dmhy/search?q={query}"},
			{Name: "broken", SearchURLTemplate: server.URL + "/broken/search?q={query}"},
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	result, err := client.Search(context.Background(), "  Canonical   Show ")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("candidates = %#v, want 2", result.Candidates)
	}
	if len(result.Failures) != 1 || result.Failures[0].Provider != "broken" || result.Failures[0].Code != "search_provider_failed" {
		t.Fatalf("failures = %#v", result.Failures)
	}

	var detailFound, torrentFound bool
	for _, candidate := range result.Candidates {
		switch candidate.Title {
		case "Canonical Show 01 1080p":
			detailFound = candidate.DownloadURI == "magnet:?xt=urn:btih:"+hash+"&dn=Canonical" && candidate.IdentityKey == "btih:"+hash
		case "Canonical Show 02 1080p":
			torrentFound = candidate.DownloadURI == server.URL+"/files/direct.torrent" && strings.HasPrefix(candidate.IdentityKey, "url:")
		}
	}
	if !detailFound || !torrentFound {
		t.Fatalf("resolved candidates = %#v", result.Candidates)
	}
}

func TestSearchFailsWhenEveryProviderFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "no", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{
		HTTPClient: server.Client(),
		Providers:  []Provider{{Name: "only", SearchURLTemplate: server.URL + "/search?q={query}"}},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	result, err := client.Search(context.Background(), "Show")
	if err == nil || !strings.Contains(err.Error(), "all search providers failed") {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Failures) != 1 || len(result.Candidates) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestDirectDownloadRecognizesLegacyProviderRoutes(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{url: "https://www.kisssub.org/download.php?id=42", want: true},
		{url: "https://mikanani.me/Download/abc123", want: true},
		{url: "https://example.test/attachment?file=show.torrent", want: true},
		{url: "https://example.test/topics/view/42", want: false},
	}
	for _, test := range tests {
		if got := isDirectDownload(test.url); got != test.want {
			t.Fatalf("isDirectDownload(%q) = %t, want %t", test.url, got, test.want)
		}
	}
}

func TestNewClientRejectsUnsafeProviderURL(t *testing.T) {
	_, err := NewClient(ClientOptions{Providers: []Provider{{Name: "bad", SearchURLTemplate: "https://user:pass@example.test/search?q={query}"}}})
	if err == nil {
		t.Fatal("NewClient() error = nil")
	}
}

func TestSearchFetchesCrossHostDetailWhenItResolvesToPublicAddress(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	providerServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<a href="http://cdn.example.test/topics/view/7">Canonical Show 07 1080p</a>`))
	}))
	defer providerServer.Close()
	detailServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<a href="magnet:?xt=urn:btih:` + hash + `&amp;dn=Canonical">Magnet</a>`))
	}))
	defer detailServer.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{DialContext: testDialer(map[string]string{
			"provider.example.test:80": strings.TrimPrefix(providerServer.URL, "http://"),
			// 详情页必须拨向校验阶段固定下来的地址，而不是再次解析域名。
			"203.0.113.20:80": strings.TrimPrefix(detailServer.URL, "http://"),
		})}},
		Providers: []Provider{{Name: "dmhy", SearchURLTemplate: "http://provider.example.test/search?q={query}"}},
		Resolver: staticResolver{
			"provider.example.test": {netip.MustParseAddr("203.0.113.10")},
			"cdn.example.test":      {netip.MustParseAddr("203.0.113.20")},
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.Search(context.Background(), "Canonical Show")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].DownloadURI != "magnet:?xt=urn:btih:"+hash+"&dn=Canonical" {
		t.Fatalf("candidates = %#v", result.Candidates)
	}
}

func TestSearchTrustsSameHostDetailOnInternalProvider(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	detailServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<a href="magnet:?xt=urn:btih:` + hash + `&amp;dn=Internal">Magnet</a>`))
	}))
	defer detailServer.Close()
	providerServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<a href="` + detailServer.URL + `/topics/view/9">Internal Show 09 1080p</a>`))
	}))
	defer providerServer.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: providerServer.Client(),
		Providers:  []Provider{{Name: "dmhy", SearchURLTemplate: providerServer.URL + "/search?q={query}"}},
		// 管理员显式配置的内部搜索源与详情页同主机（端口可以不同），不应被内网地址策略阻断。
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.Search(context.Background(), "Internal Show")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].DownloadURI != "magnet:?xt=urn:btih:"+hash+"&dn=Internal" {
		t.Fatalf("candidates = %#v", result.Candidates)
	}
}

func TestSearchDoesNotFetchDetailResolvingToPrivateAddress(t *testing.T) {
	tests := []struct {
		name        string
		detailHost  string
		resolver    staticResolver
		wantBlocked bool
	}{
		{
			name:        "private IPv4",
			detailHost:  "internal.example.test",
			resolver:    staticResolver{"internal.example.test": {netip.MustParseAddr("10.1.2.3")}},
			wantBlocked: true,
		},
		{
			name:        "loopback IPv6",
			detailHost:  "loopback.example.test",
			resolver:    staticResolver{"loopback.example.test": {netip.MustParseAddr("::1")}},
			wantBlocked: true,
		},
		{
			name:        "mixed public and private",
			detailHost:  "mixed.example.test",
			resolver:    staticResolver{"mixed.example.test": {netip.MustParseAddr("203.0.113.30"), netip.MustParseAddr("192.168.1.5")}},
			wantBlocked: true,
		},
		{
			name:        "unresolvable",
			detailHost:  "unresolvable.example.test",
			resolver:    staticResolver{},
			wantBlocked: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detailHits := 0
			providerServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				_, _ = response.Write([]byte(`<a href="http://` + test.detailHost + `/topics/view/8">Canonical Show 08 1080p</a>`))
			}))
			defer providerServer.Close()
			client, err := NewClient(ClientOptions{
				HTTPClient: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					if address == "provider.example.test:80" {
						return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(providerServer.URL, "http://"))
					}
					detailHits++
					return nil, fmt.Errorf("detail fetch must not be attempted")
				}}},
				Providers: []Provider{{Name: "dmhy", SearchURLTemplate: "http://provider.example.test/search?q={query}"}},
				Resolver:  test.resolver,
			})
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			result, err := client.Search(context.Background(), "Canonical Show")
			if err != nil {
				t.Fatalf("Search() error = %v", err)
			}
			if test.wantBlocked && detailHits != 0 {
				t.Fatalf("detail fetch attempts = %d, want 0", detailHits)
			}
			if len(result.Candidates) != 1 || result.Candidates[0].DownloadURI != "" {
				t.Fatalf("candidates = %#v", result.Candidates)
			}
		})
	}
}

func TestSearchAllowsProxyResolvedCrossHostDetailWhenLocalDNSFails(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	providerServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<a href="http://cdn.example.test/topics/view/11">Proxy Show 11 1080p</a>`))
	}))
	defer providerServer.Close()
	detailServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`<a href="magnet:?xt=urn:btih:` + hash + `&amp;dn=Proxy">Magnet</a>`))
	}))
	defer detailServer.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{DialContext: testDialer(map[string]string{
			"provider.example.test:80": strings.TrimPrefix(providerServer.URL, "http://"),
			"cdn.example.test:80":      strings.TrimPrefix(detailServer.URL, "http://"),
		})}},
		Providers: []Provider{{Name: "dmhy", SearchURLTemplate: "http://provider.example.test/search?q={query}"}},
		// 启用代理后由代理解析目标域名，本地 DNS 失败不应跳过详情页。
		ProxyEnabled: true,
		Resolver:     staticResolver{},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.Search(context.Background(), "Proxy Show")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].DownloadURI != "magnet:?xt=urn:btih:"+hash+"&dn=Proxy" {
		t.Fatalf("candidates = %#v", result.Candidates)
	}
}

func TestSearchRejectsRedirectToPrivateAddressWithoutLeakingTarget(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "http://internal.example.test/detail", http.StatusFound)
	}))
	defer providerServer.Close()

	client, err := NewClient(ClientOptions{
		HTTPClient: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address == "provider.example.test:80" {
				return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(providerServer.URL, "http://"))
			}
			return nil, fmt.Errorf("redirect target must not be dialed")
		}}},
		Providers: []Provider{{Name: "dmhy", SearchURLTemplate: "http://provider.example.test/search?q={query}"}},
		Resolver:  staticResolver{"internal.example.test": {netip.MustParseAddr("10.1.2.3")}},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.Search(context.Background(), "Canonical Show")
	if err == nil {
		t.Fatal("Search() error = nil, want redirect rejection")
	}
	if !strings.Contains(err.Error(), "all search providers failed") {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("failures = %#v", result.Failures)
	}
	for _, leaked := range []string{"internal.example.test", "10.1.2.3", "/detail"} {
		if strings.Contains(err.Error(), leaked) || strings.Contains(result.Failures[0].Message, leaked) {
			t.Fatalf("failure message = %q, must not contain %q", result.Failures[0].Message, leaked)
		}
	}
}

func TestSearchCapsCandidatesPerProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`
			<a href="/files/01.torrent">Show 01</a>
			<a href="/files/02.torrent">Show 02</a>
			<a href="/files/03.torrent">Show 03</a>`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{
		HTTPClient:               server.Client(),
		Providers:                []Provider{{Name: "dmhy", SearchURLTemplate: server.URL + "/search?q={query}"}},
		MaxCandidatesPerProvider: 2,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.Search(context.Background(), "Show")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(result.Candidates))
	}
}

func TestBlockedSearchAddressCoversNonPublicRanges(t *testing.T) {
	tests := []struct {
		address string
		blocked bool
	}{
		{address: "127.0.0.1", blocked: true},
		{address: "::1", blocked: true},
		{address: "10.0.0.1", blocked: true},
		{address: "172.16.0.1", blocked: true},
		{address: "192.168.1.1", blocked: true},
		{address: "169.254.169.254", blocked: true},
		{address: "fd00::1", blocked: true},
		{address: "0.0.0.0", blocked: true},
		{address: "224.0.0.1", blocked: true},
		{address: "203.0.113.10", blocked: false},
		{address: "2001:4860:4860::8888", blocked: false},
	}
	for _, test := range tests {
		if got := blockedSearchAddress(netip.MustParseAddr(test.address)); got != test.blocked {
			t.Fatalf("blockedSearchAddress(%s) = %t, want %t", test.address, got, test.blocked)
		}
	}
}
