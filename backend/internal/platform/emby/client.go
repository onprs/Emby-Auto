package emby

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

const (
	maxErrorBodyBytes    = 64 << 10
	maxResponseBodyBytes = 16 << 20
)

type ClientOptions struct {
	BaseURL        string
	APIKey         string
	RequestTimeout time.Duration
	HTTPClient     *http.Client
}

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (err *HTTPError) Error() string {
	if err.Body == "" {
		return fmt.Sprintf("Emby HTTP %d", err.StatusCode)
	}
	return fmt.Sprintf("Emby HTTP %d: %s", err.StatusCode, err.Body)
}

func NewClient(options ClientOptions) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(options.BaseURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return nil, fmt.Errorf("emby base URL must be an HTTP(S) URL without credentials")
	}
	if strings.TrimSpace(options.APIKey) == "" {
		return nil, fmt.Errorf("emby API key must not be blank")
	}
	if options.RequestTimeout <= 0 {
		return nil, fmt.Errorf("emby request timeout must be positive")
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{}
	} else {
		clone := *client
		client = &clone
	}
	client.Timeout = options.RequestTimeout
	return &Client{
		baseURL:    strings.TrimRight(parsed.String(), "/"),
		apiKey:     options.APIKey,
		httpClient: client,
	}, nil
}

func (client *Client) RefreshLibrary(ctx context.Context) error {
	_, err := client.request(ctx, http.MethodPost, "/Library/Refresh", nil)
	return err
}

func (client *Client) Libraries(ctx context.Context) ([]domain.EmbyLibraryCatalog, error) {
	payload, err := client.request(ctx, http.MethodGet, "/Library/VirtualFolders", nil)
	if err != nil {
		return nil, err
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(payload, &entries); err != nil {
		return nil, fmt.Errorf("decode Emby libraries: %w", err)
	}
	libraries := make([]domain.EmbyLibraryCatalog, 0, len(entries))
	for _, entry := range entries {
		var decoded struct {
			Name           string   `json:"Name"`
			ItemID         string   `json:"ItemId"`
			CollectionType string   `json:"CollectionType"`
			Locations      []string `json:"Locations"`
		}
		if err := json.Unmarshal(entry, &decoded); err != nil {
			return nil, fmt.Errorf("decode Emby library: %w", err)
		}
		if strings.TrimSpace(decoded.ItemID) == "" || strings.TrimSpace(decoded.Name) == "" {
			return nil, fmt.Errorf("emby library response contains an incomplete entry")
		}
		libraries = append(libraries, domain.EmbyLibraryCatalog{
			EmbyID:         strings.TrimSpace(decoded.ItemID),
			Name:           strings.TrimSpace(decoded.Name),
			CollectionType: strings.TrimSpace(decoded.CollectionType),
			Locations:      append([]string(nil), decoded.Locations...),
			Payload:        append(json.RawMessage(nil), entry...),
		})
	}
	return libraries, nil
}

type ItemPage struct {
	Items            []domain.EmbyLibraryItemCatalog
	TotalRecordCount int
}

func (client *Client) LibraryItems(
	ctx context.Context,
	libraryEmbyID string,
	startIndex int,
	limit int,
) (ItemPage, error) {
	if strings.TrimSpace(libraryEmbyID) == "" {
		return ItemPage{}, fmt.Errorf("emby library ID must not be blank")
	}
	if startIndex < 0 || limit <= 0 || limit > 1000 {
		return ItemPage{}, fmt.Errorf("emby item pagination is invalid")
	}
	query := url.Values{}
	query.Set("ParentId", libraryEmbyID)
	query.Set("Recursive", "true")
	query.Set("IncludeItemTypes", "Series,Season,Episode,Movie")
	query.Set("Fields", "Path,ParentId,ProviderIds,IndexNumber,ParentIndexNumber")
	query.Set("StartIndex", strconv.Itoa(startIndex))
	query.Set("Limit", strconv.Itoa(limit))
	page, err := client.queryItems(ctx, query)
	if err != nil {
		return ItemPage{}, err
	}
	if page.TotalRecordCount < startIndex+len(page.Items) {
		return ItemPage{}, fmt.Errorf("emby item count is inconsistent")
	}
	return page, nil
}

// SeriesEpisodesByTMDb resolves one Emby Series by its TMDb provider ID and
// returns its current Episode children. Callers match target TMDb episode IDs
// first and target season/episode coordinates second.
func (client *Client) SeriesEpisodesByTMDb(ctx context.Context, tmdbSeriesID int64) ([]domain.EmbyLibraryItemCatalog, error) {
	if tmdbSeriesID <= 0 {
		return nil, fmt.Errorf("TMDb series ID must be positive")
	}
	seriesQuery := url.Values{}
	seriesQuery.Set("AnyProviderIdEquals", "tmdb."+strconv.FormatInt(tmdbSeriesID, 10))
	seriesQuery.Set("Recursive", "true")
	seriesQuery.Set("IncludeItemTypes", "Series")
	seriesQuery.Set("Fields", "ProviderIds")
	seriesQuery.Set("Limit", "10")
	seriesPage, err := client.queryItems(ctx, seriesQuery)
	if err != nil {
		return nil, fmt.Errorf("query Emby series by TMDb ID: %w", err)
	}
	if seriesPage.TotalRecordCount == 0 {
		return []domain.EmbyLibraryItemCatalog{}, nil
	}
	if seriesPage.TotalRecordCount != 1 || len(seriesPage.Items) != 1 || seriesPage.Items[0].ItemType != "Series" {
		return nil, fmt.Errorf("emby TMDb series lookup is ambiguous")
	}

	const pageSize = 1000
	episodes := make([]domain.EmbyLibraryItemCatalog, 0)
	for startIndex, expectedTotal := 0, -1; ; {
		episodeQuery := url.Values{}
		episodeQuery.Set("ParentId", seriesPage.Items[0].EmbyID)
		episodeQuery.Set("Recursive", "true")
		episodeQuery.Set("IncludeItemTypes", "Episode")
		episodeQuery.Set("Fields", "Path,ParentId,ProviderIds,IndexNumber,ParentIndexNumber")
		episodeQuery.Set("StartIndex", strconv.Itoa(startIndex))
		episodeQuery.Set("Limit", strconv.Itoa(pageSize))
		page, err := client.queryItems(ctx, episodeQuery)
		if err != nil {
			return nil, fmt.Errorf("query Emby series episodes: %w", err)
		}
		if expectedTotal < 0 {
			expectedTotal = page.TotalRecordCount
		} else if page.TotalRecordCount != expectedTotal {
			return nil, fmt.Errorf("emby series episode catalog changed during verification")
		}
		if startIndex+len(page.Items) > expectedTotal {
			return nil, fmt.Errorf("emby series episode count is inconsistent")
		}
		episodes = append(episodes, page.Items...)
		startIndex += len(page.Items)
		if startIndex >= expectedTotal {
			break
		}
		if len(page.Items) == 0 {
			return nil, fmt.Errorf("emby returned an empty episode page before the catalog ended")
		}
	}
	return episodes, nil
}

func (client *Client) queryItems(ctx context.Context, query url.Values) (ItemPage, error) {
	payload, err := client.request(ctx, http.MethodGet, "/Items", query)
	if err != nil {
		return ItemPage{}, err
	}
	var response struct {
		Items            []json.RawMessage `json:"Items"`
		TotalRecordCount int               `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return ItemPage{}, fmt.Errorf("decode Emby items: %w", err)
	}
	if response.TotalRecordCount < 0 {
		return ItemPage{}, fmt.Errorf("emby item count is inconsistent")
	}
	page := ItemPage{Items: make([]domain.EmbyLibraryItemCatalog, 0, len(response.Items)), TotalRecordCount: response.TotalRecordCount}
	for _, entry := range response.Items {
		var decoded struct {
			ID                string            `json:"Id"`
			ParentID          string            `json:"ParentId"`
			Type              string            `json:"Type"`
			Name              string            `json:"Name"`
			Path              string            `json:"Path"`
			ProviderIDs       map[string]string `json:"ProviderIds"`
			IndexNumber       *int              `json:"IndexNumber"`
			ParentIndexNumber *int              `json:"ParentIndexNumber"`
		}
		if err := json.Unmarshal(entry, &decoded); err != nil {
			return ItemPage{}, fmt.Errorf("decode Emby item: %w", err)
		}
		if strings.TrimSpace(decoded.ID) == "" || strings.TrimSpace(decoded.Name) == "" ||
			(decoded.Type != "Series" && decoded.Type != "Season" && decoded.Type != "Episode" && decoded.Type != "Movie") {
			return ItemPage{}, fmt.Errorf("emby item response contains an incomplete entry")
		}
		item := domain.EmbyLibraryItemCatalog{
			EmbyID:       strings.TrimSpace(decoded.ID),
			ParentEmbyID: strings.TrimSpace(decoded.ParentID),
			ItemType:     decoded.Type,
			Name:         strings.TrimSpace(decoded.Name),
			Path:         strings.TrimSpace(decoded.Path),
			ProviderIDs:  decoded.ProviderIDs,
			Payload:      append(json.RawMessage(nil), entry...),
		}
		switch decoded.Type {
		case "Season":
			item.SeasonNumber = decoded.IndexNumber
		case "Episode":
			item.SeasonNumber = decoded.ParentIndexNumber
			item.EpisodeNumber = decoded.IndexNumber
		}
		if item.ProviderIDs == nil {
			item.ProviderIDs = map[string]string{}
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

func (client *Client) request(ctx context.Context, method, path string, query url.Values) ([]byte, error) {
	requestURL := client.baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create Emby request: %w", err)
	}
	request.Header.Set("X-Emby-Token", client.apiKey)
	request.Header.Set("Accept", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request Emby API: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		return nil, &HTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Emby response: %w", err)
	}
	if len(body) > maxResponseBodyBytes {
		return nil, fmt.Errorf("emby response exceeds %d bytes", maxResponseBodyBytes)
	}
	return body, nil
}
