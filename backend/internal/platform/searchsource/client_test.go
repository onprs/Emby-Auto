package searchsource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
