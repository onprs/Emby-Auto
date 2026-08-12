package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

const (
	defaultBaseURL       = "https://api.themoviedb.org/3"
	preferredLanguage    = "zh-CN"
	secondaryLanguage    = "zh-TW"
	maxResponseBodyBytes = 8 << 20
	maxErrorBodyBytes    = 64 << 10
)

type ClientOptions struct {
	BaseURL        string
	APIToken       string
	RequestTimeout time.Duration
	HTTPClient     *http.Client
}

type Client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (err *HTTPError) Error() string {
	if err.Body == "" {
		return fmt.Sprintf("TMDb HTTP %d", err.StatusCode)
	}
	return fmt.Sprintf("TMDb HTTP %d: %s", err.StatusCode, err.Body)
}

type tvDetails struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	OriginalName string `json:"original_name"`
	Seasons      []struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		SeasonNumber int    `json:"season_number"`
		EpisodeCount int    `json:"episode_count"`
	} `json:"seasons"`
}

type seasonDetails struct {
	ID           int64             `json:"id"`
	Name         string            `json:"name"`
	SeasonNumber int               `json:"season_number"`
	Episodes     []json.RawMessage `json:"episodes"`
}

type episodeDetails struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	EpisodeNumber int    `json:"episode_number"`
	AirDate       string `json:"air_date"`
}

type movieSearchHit struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	ReleaseDate   string `json:"release_date"`
	Overview      string `json:"overview"`
}

type movieSearchDocument struct {
	Results []movieSearchHit `json:"results"`
}

type tvSearchHit struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	OriginalName string `json:"original_name"`
	FirstAirDate string `json:"first_air_date"`
	Overview     string `json:"overview"`
}

type tvSearchDocument struct {
	Results []tvSearchHit `json:"results"`
}

type localizedEpisode struct {
	details episodeDetails
	payload json.RawMessage
}

func NewClient(options ClientOptions) (*Client, error) {
	baseURL := strings.TrimSpace(options.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return nil, fmt.Errorf("TMDb base URL must be an HTTP(S) URL without credentials")
	}
	if strings.TrimSpace(options.APIToken) == "" {
		return nil, fmt.Errorf("TMDb API token must not be blank")
	}
	if options.RequestTimeout <= 0 {
		return nil, fmt.Errorf("TMDb request timeout must be positive")
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	httpClient.Timeout = options.RequestTimeout
	return &Client{
		baseURL:    strings.TrimRight(parsed.String(), "/"),
		apiToken:   options.APIToken,
		httpClient: httpClient,
	}, nil
}

func (client *Client) Series(ctx context.Context, tmdbSeriesID int64) (domain.TMDbSeriesCatalog, error) {
	if tmdbSeriesID <= 0 {
		return domain.TMDbSeriesCatalog{}, fmt.Errorf("TMDb series ID must be positive")
	}
	seriesPath := "/tv/" + strconv.FormatInt(tmdbSeriesID, 10)
	payload, err := client.get(ctx, seriesPath, languageQuery(preferredLanguage))
	if err != nil {
		return domain.TMDbSeriesCatalog{}, err
	}
	var details tvDetails
	if err := json.Unmarshal(payload, &details); err != nil {
		return domain.TMDbSeriesCatalog{}, fmt.Errorf("decode TMDb series: %w", err)
	}
	if details.ID != tmdbSeriesID {
		return domain.TMDbSeriesCatalog{}, fmt.Errorf("TMDb series response does not match the requested ID")
	}
	traditional := client.optionalSeriesDetails(ctx, seriesPath, tmdbSeriesID)
	name := preferChineseName(details.Name, traditional.Name, details.OriginalName, traditional.OriginalName)
	if name == "" {
		return domain.TMDbSeriesCatalog{}, fmt.Errorf("TMDb series response is incomplete")
	}
	catalog := domain.TMDbSeriesCatalog{
		TMDbSeriesID: details.ID,
		Name:         name,
		OriginalName: firstText(details.OriginalName, traditional.OriginalName),
		Payload:      append(json.RawMessage(nil), payload...),
		Seasons:      make([]domain.TMDbSeasonCatalog, 0, len(details.Seasons)),
	}
	for _, summary := range details.Seasons {
		if summary.SeasonNumber < 0 || summary.ID <= 0 {
			return domain.TMDbSeriesCatalog{}, fmt.Errorf("TMDb series contains an invalid season")
		}
		seasonPath := seriesPath + "/season/" + strconv.Itoa(summary.SeasonNumber)
		seasonPayload, err := client.get(ctx, seasonPath, languageQuery(preferredLanguage))
		if err != nil {
			return domain.TMDbSeriesCatalog{}, err
		}
		var season seasonDetails
		if err := json.Unmarshal(seasonPayload, &season); err != nil {
			return domain.TMDbSeriesCatalog{}, fmt.Errorf("decode TMDb season %d: %w", summary.SeasonNumber, err)
		}
		if season.SeasonNumber != summary.SeasonNumber || season.ID != summary.ID {
			return domain.TMDbSeriesCatalog{}, fmt.Errorf("TMDb season %d response does not match its summary", summary.SeasonNumber)
		}
		traditionalSeason, err := client.optionalSeasonDetails(ctx, seasonPath, summary.ID, summary.SeasonNumber)
		if err != nil {
			return domain.TMDbSeriesCatalog{}, err
		}
		episodes, err := mergeLocalizedEpisodes(summary.SeasonNumber, season.Episodes, traditionalSeason.Episodes)
		if err != nil {
			return domain.TMDbSeriesCatalog{}, err
		}
		catalog.Seasons = append(catalog.Seasons, domain.TMDbSeasonCatalog{
			TMDbSeasonID: season.ID,
			SeasonNumber: season.SeasonNumber,
			Name:         preferChineseName(season.Name, traditionalSeason.Name),
			Payload:      append(json.RawMessage(nil), seasonPayload...),
			Episodes:     episodes,
		})
	}
	return catalog, nil
}

// SearchMovies queries TMDb for movies matching a name.
func (client *Client) SearchMovies(ctx context.Context, query string) ([]domain.TMDbMovieSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("TMDb search query must not be blank")
	}
	var preferred movieSearchDocument
	if err := client.getJSON(ctx, "/search/movie", searchQuery(query, preferredLanguage), &preferred); err != nil {
		return nil, err
	}
	var secondary movieSearchDocument
	_ = client.getJSON(ctx, "/search/movie", searchQuery(query, secondaryLanguage), &secondary)
	return mergeMovieSearchResults(preferred.Results, secondary.Results), nil
}

// Ping verifies the API token by reading the lightweight configuration resource.
func (client *Client) Ping(ctx context.Context) error {
	_, err := client.get(ctx, "/configuration", nil)
	return err
}

// SearchTV queries TMDb for TV series matching a name.
func (client *Client) SearchTV(ctx context.Context, query string) ([]domain.TMDbSeriesSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("TMDb search query must not be blank")
	}
	var preferred tvSearchDocument
	if err := client.getJSON(ctx, "/search/tv", searchQuery(query, preferredLanguage), &preferred); err != nil {
		return nil, err
	}
	var secondary tvSearchDocument
	_ = client.getJSON(ctx, "/search/tv", searchQuery(query, secondaryLanguage), &secondary)
	return mergeTVSearchResults(preferred.Results, secondary.Results), nil
}

func (client *Client) optionalSeriesDetails(ctx context.Context, path string, tmdbSeriesID int64) tvDetails {
	payload, err := client.get(ctx, path, languageQuery(secondaryLanguage))
	if err != nil {
		return tvDetails{}
	}
	var details tvDetails
	if json.Unmarshal(payload, &details) != nil || details.ID != tmdbSeriesID {
		return tvDetails{}
	}
	return details
}

func (client *Client) optionalSeasonDetails(
	ctx context.Context,
	path string,
	tmdbSeasonID int64,
	seasonNumber int,
) (seasonDetails, error) {
	payload, err := client.get(ctx, path, languageQuery(secondaryLanguage))
	if err != nil {
		return seasonDetails{}, nil
	}
	var season seasonDetails
	if err := json.Unmarshal(payload, &season); err != nil {
		return seasonDetails{}, fmt.Errorf("decode TMDb season %d secondary localization: %w", seasonNumber, err)
	}
	if season.ID != tmdbSeasonID || season.SeasonNumber != seasonNumber {
		return seasonDetails{}, fmt.Errorf("TMDb season %d secondary localization does not match its summary", seasonNumber)
	}
	return season, nil
}

func mergeLocalizedEpisodes(
	seasonNumber int,
	preferredPayloads []json.RawMessage,
	secondaryPayloads []json.RawMessage,
) ([]domain.TMDbEpisodeCatalog, error) {
	preferred, err := decodeSeasonEpisodes(seasonNumber, preferredPayloads)
	if err != nil {
		return nil, err
	}
	secondary, err := decodeSeasonEpisodes(seasonNumber, secondaryPayloads)
	if err != nil {
		return nil, err
	}
	numbers := make([]int, 0, len(preferred)+len(secondary))
	seen := make(map[int]struct{}, len(preferred)+len(secondary))
	for number := range preferred {
		seen[number] = struct{}{}
		numbers = append(numbers, number)
	}
	for number := range secondary {
		if _, exists := seen[number]; exists {
			continue
		}
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)

	episodes := make([]domain.TMDbEpisodeCatalog, 0, len(numbers))
	for _, number := range numbers {
		preferredEpisode, hasPreferred := preferred[number]
		secondaryEpisode, hasSecondary := secondary[number]
		selected := preferredEpisode
		if !hasPreferred {
			selected = secondaryEpisode
		}
		if hasPreferred && hasSecondary && preferredEpisode.details.ID != secondaryEpisode.details.ID {
			return nil, fmt.Errorf("TMDb season %d episode %d localizations do not match", seasonNumber, number)
		}
		secondaryName := ""
		if hasSecondary {
			secondaryName = secondaryEpisode.details.Name
		}
		name := preferChineseName(selected.details.Name, secondaryName)
		if selected.details.ID <= 0 || name == "" {
			return nil, fmt.Errorf("TMDb season %d contains an incomplete episode", seasonNumber)
		}
		airDateText := selected.details.AirDate
		if strings.TrimSpace(airDateText) == "" && hasSecondary {
			airDateText = secondaryEpisode.details.AirDate
		}
		var airDate *time.Time
		if strings.TrimSpace(airDateText) != "" {
			parsed, parseErr := time.Parse("2006-01-02", airDateText)
			if parseErr != nil {
				return nil, fmt.Errorf("parse TMDb episode air date: %w", parseErr)
			}
			airDate = &parsed
		}
		episodes = append(episodes, domain.TMDbEpisodeCatalog{
			TMDbEpisodeID: selected.details.ID,
			EpisodeNumber: number,
			Name:          name,
			AirDate:       airDate,
			Payload:       append(json.RawMessage(nil), selected.payload...),
		})
	}
	return episodes, nil
}

func decodeSeasonEpisodes(seasonNumber int, payloads []json.RawMessage) (map[int]localizedEpisode, error) {
	episodes := make(map[int]localizedEpisode, len(payloads))
	for _, payload := range payloads {
		var episode episodeDetails
		if err := json.Unmarshal(payload, &episode); err != nil {
			return nil, fmt.Errorf("decode TMDb season %d episode: %w", seasonNumber, err)
		}
		if episode.ID <= 0 || episode.EpisodeNumber <= 0 {
			return nil, fmt.Errorf("TMDb season %d contains an incomplete episode", seasonNumber)
		}
		if _, exists := episodes[episode.EpisodeNumber]; exists {
			return nil, fmt.Errorf("TMDb season %d contains duplicate episode %d", seasonNumber, episode.EpisodeNumber)
		}
		episodes[episode.EpisodeNumber] = localizedEpisode{details: episode, payload: payload}
	}
	return episodes, nil
}

func mergeMovieSearchResults(preferred, secondary []movieSearchHit) []domain.TMDbMovieSearchResult {
	secondaryByID := make(map[int64]movieSearchHit, len(secondary))
	for _, hit := range secondary {
		if hit.ID > 0 {
			secondaryByID[hit.ID] = hit
		}
	}
	results := make([]domain.TMDbMovieSearchResult, 0, len(preferred)+len(secondary))
	seen := make(map[int64]struct{}, len(preferred)+len(secondary))
	appendHit := func(hit, localized movieSearchHit) {
		if hit.ID <= 0 {
			return
		}
		title := preferChineseName(hit.Title, localized.Title, hit.OriginalTitle, localized.OriginalTitle)
		if title == "" {
			return
		}
		releaseDate := firstText(hit.ReleaseDate, localized.ReleaseDate)
		releaseYear := 0
		if parsed, parseErr := time.Parse("2006-01-02", releaseDate); parseErr == nil {
			releaseYear = parsed.Year()
		}
		results = append(results, domain.TMDbMovieSearchResult{
			TMDbMovieID: hit.ID, Title: title,
			OriginalTitle: firstText(hit.OriginalTitle, localized.OriginalTitle),
			ReleaseDate:   releaseDate, ReleaseYear: releaseYear,
			Overview: firstText(hit.Overview, localized.Overview),
		})
		seen[hit.ID] = struct{}{}
	}
	for _, hit := range preferred {
		if _, exists := seen[hit.ID]; exists {
			continue
		}
		appendHit(hit, secondaryByID[hit.ID])
	}
	for _, hit := range secondary {
		if _, exists := seen[hit.ID]; exists {
			continue
		}
		appendHit(hit, movieSearchHit{})
	}
	return results
}

func mergeTVSearchResults(preferred, secondary []tvSearchHit) []domain.TMDbSeriesSearchResult {
	secondaryByID := make(map[int64]tvSearchHit, len(secondary))
	for _, hit := range secondary {
		if hit.ID > 0 {
			secondaryByID[hit.ID] = hit
		}
	}
	results := make([]domain.TMDbSeriesSearchResult, 0, len(preferred)+len(secondary))
	seen := make(map[int64]struct{}, len(preferred)+len(secondary))
	appendHit := func(hit, localized tvSearchHit) {
		if hit.ID <= 0 {
			return
		}
		name := preferChineseName(hit.Name, localized.Name, hit.OriginalName, localized.OriginalName)
		if name == "" {
			return
		}
		results = append(results, domain.TMDbSeriesSearchResult{
			TMDbSeriesID: hit.ID, Name: name,
			OriginalName: firstText(hit.OriginalName, localized.OriginalName),
			FirstAirDate: firstText(hit.FirstAirDate, localized.FirstAirDate),
			Overview:     firstText(hit.Overview, localized.Overview),
		})
		seen[hit.ID] = struct{}{}
	}
	for _, hit := range preferred {
		if _, exists := seen[hit.ID]; exists {
			continue
		}
		appendHit(hit, secondaryByID[hit.ID])
	}
	for _, hit := range secondary {
		if _, exists := seen[hit.ID]; exists {
			continue
		}
		appendHit(hit, tvSearchHit{})
	}
	return results
}

func preferChineseName(preferred, secondary string, fallbacks ...string) string {
	candidates := append([]string{preferred, secondary}, fallbacks...)
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if looksChinese(candidate) {
			return candidate
		}
	}
	return firstText(candidates...)
}

func looksChinese(value string) bool {
	hasHan := false
	for _, character := range value {
		switch {
		case unicode.In(character, unicode.Hiragana, unicode.Katakana):
			return false
		case unicode.Is(unicode.Han, character):
			hasHan = true
		}
	}
	return hasHan
}

func firstText(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func languageQuery(language string) url.Values {
	return url.Values{"language": {language}}
}

func searchQuery(query, language string) url.Values {
	return url.Values{
		"include_adult": {"false"},
		"language":      {language},
		"query":         {query},
	}
}

func (client *Client) getJSON(ctx context.Context, path string, query url.Values, destination any) error {
	payload, err := client.get(ctx, path, query)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, destination); err != nil {
		return fmt.Errorf("decode TMDb response: %w", err)
	}
	return nil
}

func (client *Client) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	endpoint, err := url.Parse(client.baseURL + path)
	if err != nil {
		return nil, fmt.Errorf("build TMDb request URL: %w", err)
	}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create TMDb request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+client.apiToken)
	request.Header.Set("Accept", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request TMDb catalog: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		return nil, &HTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read TMDb response: %w", err)
	}
	if len(body) > maxResponseBodyBytes {
		return nil, fmt.Errorf("TMDb response exceeds %d bytes", maxResponseBodyBytes)
	}
	return body, nil
}
