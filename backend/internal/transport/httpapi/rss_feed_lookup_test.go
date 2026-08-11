package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type rssFeedLookupStub struct {
	lookup domain.RSSFeedLookup
	err    error
	gotURL string
}

func (stub *rssFeedLookupStub) Lookup(_ context.Context, feedURL string) (domain.RSSFeedLookup, error) {
	stub.gotURL = feedURL
	return stub.lookup, stub.err
}

func TestLookupRSSFeedReturnsSuggestion(t *testing.T) {
	stub := &rssFeedLookupStub{lookup: domain.RSSFeedLookup{
		FeedURL: "https://example.test/feed.xml", FeedTitle: "孤独摇滚", SuggestedQuery: "孤独摇滚",
		SuggestedQueries: []string{"孤独摇滚", "Bocchi the Rock"}, SampleTitles: []string{"第 01 集"},
		Candidates: []domain.TMDbSeriesSearchResult{{TMDbSeriesID: 119100, Name: "孤独摇滚！"}}, CatalogMatchSource: "deterministic",
	}}
	handler := NewHandler(NewServer(readinessStub{}, WithRSSFeedLookup(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/rss/feed-lookup", strings.NewReader(`{"feedUrl":"https://example.test/feed.xml"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if stub.gotURL != "https://example.test/feed.xml" {
		t.Fatalf("lookup URL = %q, want request feedUrl", stub.gotURL)
	}
	var body struct {
		FeedUrl          string   `json:"feedUrl"`
		FeedTitle        string   `json:"feedTitle"`
		SuggestedQuery   string   `json:"suggestedQuery"`
		SuggestedQueries []string `json:"suggestedQueries"`
		SampleTitles     []string `json:"sampleTitles"`
		Candidates       []struct {
			TMDbSeriesID int64 `json:"tmdbSeriesId"`
		} `json:"candidates"`
		CatalogMatchSource string `json:"catalogMatchSource"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.FeedTitle != "孤独摇滚" || body.SuggestedQuery != "孤独摇滚" || len(body.SuggestedQueries) != 2 || len(body.SampleTitles) != 1 || len(body.Candidates) != 1 || body.Candidates[0].TMDbSeriesID != 119100 || body.CatalogMatchSource != "deterministic" {
		t.Fatalf("unexpected response body: %+v", body)
	}
}

func TestLookupRSSFeedMapsInvalidInputTo400(t *testing.T) {
	stub := &rssFeedLookupStub{err: service.NewError("invalid_rss_feed_url", "the RSS feed URL is invalid", service.ErrInvalidInput, map[string]any{"field": "feedUrl"})}
	handler := NewHandler(NewServer(readinessStub{}, WithRSSFeedLookup(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/rss/feed-lookup", strings.NewReader(`{"feedUrl":"not-a-url"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	var body ApiError
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != "invalid_rss_feed_url" {
		t.Fatalf("error code = %q, want invalid_rss_feed_url", body.Code)
	}
}

func TestLookupRSSFeedMapsFetchFailureTo503(t *testing.T) {
	stub := &rssFeedLookupStub{err: service.NewError("rss_fetch_failed", "the RSS feed could not be fetched", errors.New("timeout"), nil)}
	handler := NewHandler(NewServer(readinessStub{}, WithRSSFeedLookup(stub)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/rss/feed-lookup", strings.NewReader(`{"feedUrl":"https://example.test/feed.xml"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", recorder.Code, recorder.Body.String())
	}
	var body ApiError
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != "rss_fetch_failed" {
		t.Fatalf("error code = %q, want rss_fetch_failed", body.Code)
	}
}
