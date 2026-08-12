package rssfeed

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

const (
	defaultMaxBytes = 4 << 20
	defaultTimeout  = 15 * time.Second
)

var magnetPattern = regexp.MustCompile(`(?i)magnet:\?[^<>"'[:space:]]+`)

type ClientOptions struct {
	HTTPClient *http.Client
	MaxBytes   int64
}

type Client struct {
	httpClient *http.Client
	maxBytes   int64
}

func NewClient(options ClientOptions) (*Client, error) {
	maxBytes := options.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	var httpClient *http.Client
	if options.HTTPClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	} else {
		clone := *options.HTTPClient
		if clone.Timeout <= 0 {
			clone.Timeout = defaultTimeout
		}
		httpClient = &clone
	}
	return &Client{httpClient: httpClient, maxBytes: maxBytes}, nil
}

func (client *Client) Fetch(ctx context.Context, feedURL string) (domain.RSSFeed, error) {
	parsed, err := url.Parse(strings.TrimSpace(feedURL))
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return domain.RSSFeed{}, fmt.Errorf("RSS feed URL must be HTTP(S) without embedded credentials")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return domain.RSSFeed{}, fmt.Errorf("create RSS request: %w", err)
	}
	request.Header.Set("Accept", "application/atom+xml, application/rss+xml, application/xml, text/xml;q=0.9")
	request.Header.Set("User-Agent", "Emby-Auto/0.1")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return domain.RSSFeed{}, fmt.Errorf("fetch RSS feed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return domain.RSSFeed{}, fmt.Errorf("fetch RSS feed: HTTP %d", response.StatusCode)
	}
	if response.ContentLength > client.maxBytes {
		return domain.RSSFeed{}, fmt.Errorf("RSS feed exceeds %d bytes", client.maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, client.maxBytes+1))
	if err != nil {
		return domain.RSSFeed{}, fmt.Errorf("read RSS feed: %w", err)
	}
	if int64(len(body)) > client.maxBytes {
		return domain.RSSFeed{}, fmt.Errorf("RSS feed exceeds %d bytes", client.maxBytes)
	}
	return parse(body)
}

func parse(body []byte) (domain.RSSFeed, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	for {
		token, err := decoder.Token()
		if err != nil {
			return domain.RSSFeed{}, fmt.Errorf("decode RSS document: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch strings.ToLower(start.Name.Local) {
		case "rss", "rdf":
			var document rssDocument
			if err := xml.Unmarshal(body, &document); err != nil {
				return domain.RSSFeed{}, fmt.Errorf("decode RSS feed: %w", err)
			}
			return normalizeRSS(document), nil
		case "feed":
			var document atomDocument
			if err := xml.Unmarshal(body, &document); err != nil {
				return domain.RSSFeed{}, fmt.Errorf("decode Atom feed: %w", err)
			}
			return normalizeAtom(document), nil
		default:
			return domain.RSSFeed{}, fmt.Errorf("unsupported feed root %q", start.Name.Local)
		}
	}
}

type rssDocument struct {
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title string    `xml:"title"`
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	GUID        string         `xml:"guid"`
	Title       string         `xml:"title"`
	Link        string         `xml:"link"`
	PubDate     string         `xml:"pubDate"`
	Published   string         `xml:"published"`
	Description string         `xml:"description"`
	Content     string         `xml:"encoded"`
	MagnetURI   string         `xml:"magnetURI"`
	Enclosures  []rssEnclosure `xml:"enclosure"`
	Torrent     rssTorrent     `xml:"torrent"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Type   string `xml:"type,attr"`
	Length string `xml:"length,attr"`
}

type rssTorrent struct {
	MagnetURI string `xml:"magnetURI"`
}

type atomDocument struct {
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Published string     `xml:"published"`
	Updated   string     `xml:"updated"`
	Summary   string     `xml:"summary"`
	Content   string     `xml:"content"`
	Links     []atomLink `xml:"link"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr"`
}

func normalizeRSS(document rssDocument) domain.RSSFeed {
	feed := domain.RSSFeed{Title: strings.TrimSpace(document.Channel.Title), Entries: make([]domain.RSSFeedEntry, 0, len(document.Channel.Items))}
	for _, item := range document.Channel.Items {
		link := strings.TrimSpace(item.Link)
		downloadURI := firstDownloadURI(enclosureURLs(item.Enclosures), []string{item.MagnetURI, item.Torrent.MagnetURI}, link, item.Description, item.Content, item.GUID)
		feed.Entries = append(feed.Entries, domain.RSSFeedEntry{
			GUID:        strings.TrimSpace(item.GUID),
			Title:       strings.TrimSpace(item.Title),
			URL:         link,
			DownloadURI: downloadURI,
			PublishedAt: parseTime(firstNonBlank(item.PubDate, item.Published)),
			UpstreamPayload: map[string]any{
				"description": strings.TrimSpace(item.Description),
				"content":     strings.TrimSpace(item.Content),
			},
		})
	}
	return feed
}

func normalizeAtom(document atomDocument) domain.RSSFeed {
	feed := domain.RSSFeed{Title: strings.TrimSpace(document.Title), Entries: make([]domain.RSSFeedEntry, 0, len(document.Entries))}
	for _, item := range document.Entries {
		link := ""
		enclosures := make([]string, 0, len(item.Links))
		for _, candidate := range item.Links {
			relation := strings.ToLower(strings.TrimSpace(candidate.Rel))
			if relation == "enclosure" {
				enclosures = append(enclosures, candidate.Href)
				continue
			}
			if link == "" && (relation == "" || relation == "alternate") {
				link = strings.TrimSpace(candidate.Href)
			}
		}
		feed.Entries = append(feed.Entries, domain.RSSFeedEntry{
			GUID:        strings.TrimSpace(item.ID),
			Title:       strings.TrimSpace(item.Title),
			URL:         link,
			DownloadURI: firstDownloadURI(enclosures, nil, link, item.Summary, item.Content),
			PublishedAt: parseTime(firstNonBlank(item.Published, item.Updated)),
			UpstreamPayload: map[string]any{
				"summary": strings.TrimSpace(item.Summary),
				"content": strings.TrimSpace(item.Content),
			},
		})
	}
	return feed
}

func enclosureURLs(enclosures []rssEnclosure) []string {
	result := make([]string, 0, len(enclosures))
	for _, enclosure := range enclosures {
		result = append(result, enclosure.URL)
	}
	return result
}

func firstDownloadURI(enclosures, explicit []string, link string, text ...string) string {
	for _, candidates := range [][]string{enclosures, explicit} {
		for _, candidate := range candidates {
			if isSupportedDownloadURI(candidate, true) {
				return strings.TrimSpace(candidate)
			}
		}
	}
	if isSupportedDownloadURI(link, false) {
		return strings.TrimSpace(link)
	}
	for _, value := range text {
		decoded := html.UnescapeString(value)
		if magnet := magnetPattern.FindString(decoded); magnet != "" {
			return magnet
		}
	}
	return ""
}

func isSupportedDownloadURI(raw string, enclosure bool) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User != nil {
		return false
	}
	if strings.EqualFold(parsed.Scheme, "magnet") {
		return parsed.RawQuery != ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return false
	}
	return enclosure || strings.EqualFold(path.Ext(parsed.Path), ".torrent")
}

func parseTime(value string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		time.RFC850,
		time.ANSIC,
	} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
