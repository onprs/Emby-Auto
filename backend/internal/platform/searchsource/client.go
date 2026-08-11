package searchsource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"golang.org/x/net/html"
)

const (
	defaultMaxBytes       = 4 << 20
	defaultTimeout        = 30 * time.Second
	defaultMaxDetailPages = 20
)

type Provider struct {
	Name              string
	SearchURLTemplate string
}

type ClientOptions struct {
	HTTPClient     *http.Client
	Providers      []Provider
	MaxBytes       int64
	MaxDetailPages int
}

type Client struct {
	httpClient     *http.Client
	providers      []Provider
	maxBytes       int64
	maxDetailPages int
}

func DefaultProviders() []Provider {
	return []Provider{
		{Name: "dmhy", SearchURLTemplate: "https://share.dmhy.org/topics/list?keyword={query}"},
		{Name: "kisssub", SearchURLTemplate: "https://www.kisssub.org/search.php?keyword={query}"},
		{Name: "mikan", SearchURLTemplate: "https://mikanani.me/Home/Search?searchstr={query}"},
	}
}

func NewClient(options ClientOptions) (*Client, error) {
	providers := slices.Clone(options.Providers)
	if len(providers) == 0 {
		providers = DefaultProviders()
	}
	for _, provider := range providers {
		if strings.TrimSpace(provider.Name) == "" || !strings.Contains(provider.SearchURLTemplate, "{query}") {
			return nil, fmt.Errorf("search provider requires a name and {query} URL template")
		}
		parsed, err := url.Parse(provider.SearchURLTemplate)
		if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, fmt.Errorf("search provider %q URL must be HTTP(S) without embedded credentials", provider.Name)
		}
	}

	maxBytes := options.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	maxDetailPages := options.MaxDetailPages
	if maxDetailPages <= 0 {
		maxDetailPages = defaultMaxDetailPages
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
	return &Client{
		httpClient:     httpClient,
		providers:      providers,
		maxBytes:       maxBytes,
		maxDetailPages: maxDetailPages,
	}, nil
}

type providerOutcome struct {
	provider   string
	candidates []domain.ReleaseCandidate
	err        error
}

func (client *Client) Search(ctx context.Context, query string) (domain.SearchProviderResult, error) {
	query = strings.Join(strings.Fields(query), " ")
	if query == "" {
		return domain.SearchProviderResult{}, fmt.Errorf("search query must not be blank")
	}

	outcomes := make(chan providerOutcome, len(client.providers))
	var wait sync.WaitGroup
	for _, provider := range client.providers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			candidates, err := client.searchProvider(ctx, provider, query)
			outcomes <- providerOutcome{provider: provider.Name, candidates: candidates, err: err}
		}()
	}
	wait.Wait()
	close(outcomes)

	result := domain.SearchProviderResult{}
	seen := make(map[string]struct{})
	var providerErrors []error
	for outcome := range outcomes {
		if outcome.err != nil {
			providerErrors = append(providerErrors, outcome.err)
			result.Failures = append(result.Failures, domain.SearchProviderFailure{
				Provider: outcome.provider,
				Code:     "search_provider_failed",
				Message:  outcome.err.Error(),
			})
			continue
		}
		for _, candidate := range outcome.candidates {
			key := candidate.Provider + "\n" + candidate.IdentityKey
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result.Candidates = append(result.Candidates, candidate)
		}
	}
	slices.SortFunc(result.Candidates, func(left, right domain.ReleaseCandidate) int {
		if order := strings.Compare(left.Provider, right.Provider); order != 0 {
			return order
		}
		return strings.Compare(left.Title, right.Title)
	})
	slices.SortFunc(result.Failures, func(left, right domain.SearchProviderFailure) int {
		return strings.Compare(left.Provider, right.Provider)
	})
	if len(providerErrors) == len(client.providers) {
		return result, fmt.Errorf("all search providers failed: %w", errors.Join(providerErrors...))
	}
	return result, nil
}

func (client *Client) searchProvider(ctx context.Context, provider Provider, query string) ([]domain.ReleaseCandidate, error) {
	searchURL := strings.ReplaceAll(provider.SearchURLTemplate, "{query}", url.QueryEscape(query))
	body, err := client.fetchHTML(ctx, searchURL)
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", provider.Name, err)
	}
	anchors, err := parseAnchors(searchURL, body)
	if err != nil {
		return nil, fmt.Errorf("parse %s search results: %w", provider.Name, err)
	}

	candidates := make([]domain.ReleaseCandidate, 0, len(anchors))
	detailCount := 0
	for _, anchor := range anchors {
		downloadURI := ""
		detailURL := ""
		if isDirectDownload(anchor.URL) {
			downloadURI = anchor.URL
		} else if looksLikeDetail(provider.Name, anchor.URL) {
			if detailCount >= client.maxDetailPages {
				continue
			}
			detailCount++
			detailURL = anchor.URL
			detailBody, fetchErr := client.fetchHTML(ctx, detailURL)
			if fetchErr == nil {
				detailAnchors, parseErr := parseAnchors(detailURL, detailBody)
				if parseErr == nil {
					for _, detailAnchor := range detailAnchors {
						if isDirectDownload(detailAnchor.URL) {
							downloadURI = detailAnchor.URL
							break
						}
					}
				}
			}
		} else {
			continue
		}

		title := strings.Join(strings.Fields(anchor.Text), " ")
		if title == "" || (detailURL == "" && isGenericDownloadLabel(title)) {
			continue
		}
		identity, identityErr := domain.BuildReleaseCandidateIdentity(provider.Name, title, downloadURI, detailURL)
		if identityErr != nil {
			continue
		}
		candidates = append(candidates, domain.ReleaseCandidate{
			Provider:    strings.ToLower(strings.TrimSpace(provider.Name)),
			IdentityKey: identity,
			Title:       title,
			DownloadURI: downloadURI,
			UpstreamPayload: map[string]any{
				"detailUrl": detailURL,
				"searchUrl": searchURL,
			},
		})
	}
	return candidates, nil
}

func (client *Client) fetchHTML(ctx context.Context, rawURL string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9")
	request.Header.Set("User-Agent", "Emby-Auto/0.1")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch page: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("fetch page: HTTP %d", response.StatusCode)
	}
	if response.ContentLength > client.maxBytes {
		return nil, fmt.Errorf("page exceeds %d bytes", client.maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, client.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read page: %w", err)
	}
	if int64(len(body)) > client.maxBytes {
		return nil, fmt.Errorf("page exceeds %d bytes", client.maxBytes)
	}
	return body, nil
}

type anchor struct {
	URL  string
	Text string
}

func parseAnchors(baseURL string, body []byte) ([]anchor, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	document, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	result := make([]anchor, 0)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && strings.EqualFold(node.Data, "a") {
			href := ""
			for _, attribute := range node.Attr {
				if strings.EqualFold(attribute.Key, "href") {
					href = strings.TrimSpace(attribute.Val)
					break
				}
			}
			if href != "" {
				parsed, parseErr := url.Parse(href)
				if parseErr == nil {
					resolved := base.ResolveReference(parsed)
					if resolved.User == nil && (resolved.Scheme == "http" || resolved.Scheme == "https" || resolved.Scheme == "magnet") {
						result = append(result, anchor{URL: resolved.String(), Text: nodeText(node)})
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	return result, nil
}

func nodeText(node *html.Node) string {
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			builder.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(builder.String()), " ")
}

func isDirectDownload(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User != nil {
		return false
	}
	if strings.EqualFold(parsed.Scheme, "magnet") {
		return parsed.RawQuery != ""
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return false
	}
	decodedPath, err := url.PathUnescape(parsed.Path)
	if err != nil {
		decodedPath = parsed.Path
	}
	lowerPath := strings.ToLower(decodedPath)
	if strings.EqualFold(path.Ext(lowerPath), ".torrent") {
		return true
	}
	for _, segment := range strings.Split(strings.Trim(lowerPath, "/"), "/") {
		if segment == "download" || segment == "dl" || strings.HasPrefix(segment, "download.") {
			return true
		}
	}
	combined := lowerPath + "?" + strings.ToLower(parsed.RawQuery)
	return strings.Contains(combined, "torrent") && containsAny(combined, "download", "dl", "attach", "file", "get")
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func looksLikeDetail(provider, rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	value := strings.ToLower(parsed.Path)
	switch strings.ToLower(provider) {
	case "dmhy":
		return strings.Contains(value, "/topics/view/")
	case "mikan":
		return strings.Contains(value, "/home/episode/")
	case "kisssub":
		return strings.Contains(value, "show-") || strings.Contains(value, "/detail")
	default:
		return strings.Contains(value, "/detail") || strings.Contains(value, "/episode/") || strings.Contains(value, "/topics/view/")
	}
}

func isGenericDownloadLabel(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "magnet", "download", "torrent", "磁力", "下载", "下載":
		return true
	default:
		return false
	}
}
