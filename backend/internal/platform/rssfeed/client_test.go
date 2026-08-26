package rssfeed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientFetchNormalizesRSSDownloadSources(t *testing.T) {
	published := "Tue, 21 Jul 2026 10:00:00 GMT"
	feedXML := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Show Releases</title>
    <item>
      <guid>release-01</guid>
      <title>[Group] Show - 01 [1080p]</title>
      <link>https://example.test/releases/01</link>
      <pubDate>` + published + `</pubDate>
      <enclosure url="https://cdn.example.test/show-01.torrent" type="application/x-bittorrent" length="1234" />
    </item>
    <item>
      <guid>release-02</guid>
      <title>[Group] Show - 02 [1080p]</title>
      <link>https://example.test/releases/02</link>
      <description><![CDATA[Download magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01&dn=show-02]]></description>
    </item>
  </channel>
</rss>`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/rss+xml")
		_, _ = response.Write([]byte(feedXML))
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{HTTPClient: server.Client(), MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	feed, err := client.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if feed.Title != "Show Releases" || len(feed.Entries) != 2 {
		t.Fatalf("feed = %#v", feed)
	}
	first := feed.Entries[0]
	if first.GUID != "release-01" || first.URL != "https://example.test/releases/01" || first.DownloadURI != "https://cdn.example.test/show-01.torrent" || first.Title != "[Group] Show - 01 [1080p]" {
		t.Fatalf("first entry = %#v", first)
	}
	wantPublished := time.Date(2026, time.July, 21, 10, 0, 0, 0, time.UTC)
	if !first.PublishedAt.Equal(wantPublished) {
		t.Fatalf("publishedAt = %v, want %v", first.PublishedAt, wantPublished)
	}
	second := feed.Entries[1]
	if second.DownloadURI != "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01&dn=show-02" {
		t.Fatalf("second download URI = %q", second.DownloadURI)
	}
}

func TestClientFetchPrefersMagnetOverHTTPEnclosureForBTIHIdentity(t *testing.T) {
	feedXML := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Hybrid Releases</title>
    <item>
      <guid>shared-guid-01</guid>
      <title>[Group] Show - 01 [1080p]</title>
      <link>https://example.test/releases/01</link>
      <enclosure url="https://cdn.example.test/show-01.torrent" type="application/x-bittorrent" length="1234" />
      <torrent xmlns="https://xmlns.ezrss.it/0.1/">
        <magnetURI><![CDATA[magnet:?xt=urn:btih:1111111111111111111111111111111111111111&dn=show-01]]></magnetURI>
      </torrent>
    </item>
  </channel>
</rss>`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/rss+xml")
		_, _ = response.Write([]byte(feedXML))
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{HTTPClient: server.Client(), MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	feed, err := client.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(feed.Entries) != 1 {
		t.Fatalf("feed = %#v", feed)
	}
	entry := feed.Entries[0]
	wantMagnet := "magnet:?xt=urn:btih:1111111111111111111111111111111111111111&dn=show-01"
	if entry.DownloadURI != wantMagnet {
		t.Fatalf("downloadURI = %q, want magnet %q", entry.DownloadURI, wantMagnet)
	}
}

func TestClientFetchSupportsAtomEnclosures(t *testing.T) {
	feedXML := `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Releases</title>
  <entry>
    <id>atom-03</id>
    <title>Show S01E03</title>
    <updated>2026-07-21T11:00:00Z</updated>
    <link rel="alternate" href="https://example.test/releases/03" />
    <link rel="enclosure" type="application/x-bittorrent" href="https://cdn.example.test/show-03.torrent" />
  </entry>
</feed>`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(feedXML))
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{HTTPClient: server.Client(), MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	feed, err := client.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(feed.Entries) != 1 || feed.Entries[0].GUID != "atom-03" || feed.Entries[0].DownloadURI != "https://cdn.example.test/show-03.torrent" {
		t.Fatalf("atom feed = %#v", feed)
	}
}

func TestClientFetchSupportsRSS1RDFRootItems(t *testing.T) {
	feedXML := `<?xml version="1.0" encoding="utf-8"?>
<rdf:RDF
  xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"
  xmlns="http://purl.org/rss/1.0/"
  xmlns:dc="http://purl.org/dc/elements/1.1/"
  xmlns:enc="http://purl.oclc.org/net/rss_2.0/enc#">
  <channel rdf:about="https://example.test/feed">
    <title>RDF Releases</title>
    <link>https://example.test/</link>
  </channel>
  <item rdf:about="https://example.test/releases/04">
    <title>Show S01E04</title>
    <link>https://example.test/releases/04</link>
    <dc:date>2026-07-21T12:00:00Z</dc:date>
    <enc:enclosure rdf:resource="https://cdn.example.test/show-04.torrent" type="application/x-bittorrent" />
  </item>
</rdf:RDF>`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(feedXML))
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{HTTPClient: server.Client(), MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	feed, err := client.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if feed.Title != "RDF Releases" || len(feed.Entries) != 1 {
		t.Fatalf("RDF feed = %#v", feed)
	}
	if feed.Entries[0].GUID != "https://example.test/releases/04" ||
		feed.Entries[0].Title != "Show S01E04" ||
		feed.Entries[0].DownloadURI != "https://cdn.example.test/show-04.torrent" ||
		!feed.Entries[0].PublishedAt.Equal(time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("RDF entry = %#v", feed.Entries[0])
	}
}

func TestClientFetchRejectsOversizedOrInvalidResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "oversized", body: strings.Repeat("x", 65)},
		{name: "invalid XML", body: "not a feed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := NewClient(ClientOptions{HTTPClient: server.Client(), MaxBytes: 64})
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			if _, err := client.Fetch(context.Background(), server.URL); err == nil {
				t.Fatal("Fetch() error = nil")
			}
		})
	}
}
