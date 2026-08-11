package tmdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestSearchMoviesPrefersChineseTitleAndPreservesReleaseYear(t *testing.T) {
	languages := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if request.URL.Path != "/search/movie" || query.Get("query") != "流浪地球" || query.Get("include_adult") != "false" {
			t.Fatalf("request URL = %s", request.URL.String())
		}
		languages = append(languages, query.Get("language"))
		if query.Get("language") == secondaryLanguage {
			_, _ = response.Write([]byte(`{"results":[{"id":535167,"title":"流浪地球","original_title":"流浪地球","release_date":"2019-02-05","overview":"中文简介"}]}`))
			return
		}
		_, _ = response.Write([]byte(`{"results":[{"id":535167,"title":"The Wandering Earth","original_title":"流浪地球","release_date":"2019-02-05","overview":"overview"},{"id":0,"title":"ignored"}]}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIToken: "token", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	results, err := client.SearchMovies(context.Background(), "流浪地球")
	if err != nil {
		t.Fatalf("SearchMovies() error = %v", err)
	}
	if !reflect.DeepEqual(languages, []string{preferredLanguage, secondaryLanguage}) {
		t.Fatalf("languages = %#v", languages)
	}
	if len(results) != 1 || results[0].TMDbMovieID != 535167 || results[0].ReleaseYear != 2019 || results[0].Title != "流浪地球" {
		t.Fatalf("SearchMovies() = %#v", results)
	}
}

func TestSearchTVMergesLocalizationsByIDWithoutChangingPrimaryOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if request.URL.Path != "/search/tv" || query.Get("query") != "show" || query.Get("include_adult") != "false" {
			t.Fatalf("request URL = %s", request.URL.String())
		}
		if query.Get("language") == secondaryLanguage {
			_, _ = response.Write([]byte(`{"results":[{"id":1,"name":"繁中名稱","original_name":"Original One","first_air_date":"2024-01-01"},{"id":2,"name":"繁中第二部","original_name":"Original Two"},{"id":3,"name":"繁中獨有","original_name":"Original Three"}]}`))
			return
		}
		_, _ = response.Write([]byte(`{"results":[{"id":1,"name":"English Fallback","original_name":"Original One","first_air_date":"2024-01-01"},{"id":2,"name":"简中第二部","original_name":"Original Two"}]}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIToken: "token", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	results, err := client.SearchTV(context.Background(), "show")
	if err != nil {
		t.Fatalf("SearchTV() error = %v", err)
	}
	if len(results) != 3 || results[0].TMDbSeriesID != 1 || results[0].Name != "繁中名稱" || results[1].TMDbSeriesID != 2 || results[1].Name != "简中第二部" || results[2].TMDbSeriesID != 3 {
		t.Fatalf("SearchTV() = %#v", results)
	}
}

func TestSeriesPrefersChineseNamesAndPreservesPrimaryPayloads(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		language := request.URL.Query().Get("language")
		requests = append(requests, request.URL.Path+"?language="+language)
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path + "?" + language {
		case "/tv/42?zh-CN":
			_, _ = response.Write([]byte(`{"id":42,"name":"示例番剧","original_name":"Original Show","unknown":"series-value","seasons":[{"id":420,"name":"Specials","season_number":0,"episode_count":1},{"id":421,"name":"Season 1","season_number":1,"episode_count":2}]}`))
		case "/tv/42?zh-TW":
			_, _ = response.Write([]byte(`{"id":42,"name":"範例動畫","original_name":"Original Show","seasons":[{"id":420,"name":"特別篇","season_number":0,"episode_count":1},{"id":421,"name":"第 1 季","season_number":1,"episode_count":2}]}`))
		case "/tv/42/season/0?zh-CN":
			_, _ = response.Write([]byte(`{"id":420,"name":"特别篇","season_number":0,"episodes":[{"id":4201,"name":"特别内容","episode_number":1,"air_date":"","unknown":"episode-value"}]}`))
		case "/tv/42/season/0?zh-TW":
			_, _ = response.Write([]byte(`{"id":420,"name":"特別篇","season_number":0,"episodes":[{"id":4201,"name":"特別內容","episode_number":1,"air_date":""}]}`))
		case "/tv/42/season/1?zh-CN":
			_, _ = response.Write([]byte(`{"id":421,"name":"Season 1","season_number":1,"episodes":[{"id":4211,"name":"Pilot","episode_number":1,"air_date":"2026-01-02"},{"id":4212,"name":"大结局","episode_number":2,"air_date":null}]}`))
		case "/tv/42/season/1?zh-TW":
			_, _ = response.Write([]byte(`{"id":421,"name":"第 1 季","season_number":1,"episodes":[{"id":4211,"name":"首播集","episode_number":1,"air_date":"2026-01-02"},{"id":4212,"name":"大結局","episode_number":2,"air_date":null}]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIToken: "fixture-token", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := client.Series(context.Background(), 42)
	if err != nil {
		t.Fatalf("Series() error = %v", err)
	}
	wantRequests := []string{
		"/tv/42?language=zh-CN", "/tv/42?language=zh-TW",
		"/tv/42/season/0?language=zh-CN", "/tv/42/season/0?language=zh-TW",
		"/tv/42/season/1?language=zh-CN", "/tv/42/season/1?language=zh-TW",
	}
	if !reflect.DeepEqual(requests, wantRequests) {
		t.Fatalf("requests = %#v", requests)
	}
	if catalog.Name != "示例番剧" || catalog.OriginalName != "Original Show" || len(catalog.Seasons) != 2 {
		t.Fatalf("catalog = %#v", catalog)
	}
	if catalog.Seasons[0].SeasonNumber != 0 || catalog.Seasons[0].Name != "特别篇" || catalog.Seasons[0].Episodes[0].Name != "特别内容" {
		t.Fatalf("special season = %#v", catalog.Seasons[0])
	}
	if catalog.Seasons[1].Name != "第 1 季" || catalog.Seasons[1].Episodes[0].Name != "首播集" || catalog.Seasons[1].Episodes[1].Name != "大结局" {
		t.Fatalf("season 1 localization = %#v", catalog.Seasons[1])
	}
	if string(catalog.Seasons[0].Episodes[0].Payload) != `{"id":4201,"name":"特别内容","episode_number":1,"air_date":"","unknown":"episode-value"}` {
		t.Fatalf("episode payload = %s", catalog.Seasons[0].Episodes[0].Payload)
	}
	airDate := catalog.Seasons[1].Episodes[0].AirDate
	if airDate == nil || airDate.Format("2006-01-02") != "2026-01-02" {
		t.Fatalf("air date = %v", airDate)
	}
}

func TestPreferChineseNameDistinguishesChineseFromJapaneseAndFallbacks(t *testing.T) {
	tests := []struct {
		name      string
		preferred string
		secondary string
		fallback  string
		want      string
	}{
		{name: "traditional Chinese beats English fallback", preferred: "Re:ZERO -Starting Life in Another World-", secondary: "Re：從零開始的異世界生活", fallback: "Re:ゼロから始める異世界生活", want: "Re：從零開始的異世界生活"},
		{name: "Chinese original title beats localized English fallback", preferred: "The Wandering Earth", fallback: "流浪地球", want: "流浪地球"},
		{name: "Chinese beats Japanese with kanji and kana", preferred: "Re:ゼロから始める異世界生活", secondary: "Re：从零开始的异世界生活", want: "Re：从零开始的异世界生活"},
		{name: "simplified Chinese remains ahead of traditional Chinese", preferred: "葬送的芙莉莲", secondary: "葬送的芙莉蓮", want: "葬送的芙莉莲"},
		{name: "non Chinese primary remains the fallback", preferred: "BanG Dream!", secondary: "BanG Dream!", fallback: "バンドリ！", want: "BanG Dream!"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := preferChineseName(test.preferred, test.secondary, test.fallback); got != test.want {
				t.Fatalf("preferChineseName() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSeriesKeepsUsablePrimaryCatalogWhenSecondaryLocalizationIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("language") == secondaryLanguage {
			http.Error(response, "secondary unavailable", http.StatusServiceUnavailable)
			return
		}
		switch request.URL.Path {
		case "/tv/9":
			_, _ = response.Write([]byte(`{"id":9,"name":"Fallback Show","original_name":"Original Show","seasons":[{"id":91,"name":"Season 1","season_number":1,"episode_count":1}]}`))
		case "/tv/9/season/1":
			_, _ = response.Write([]byte(`{"id":91,"name":"Season 1","season_number":1,"episodes":[{"id":911,"name":"Fallback Episode","episode_number":1,"air_date":"2026-02-03"}]}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIToken: "token", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := client.Series(context.Background(), 9)
	if err != nil {
		t.Fatalf("Series() error = %v", err)
	}
	if catalog.Name != "Fallback Show" || len(catalog.Seasons) != 1 || catalog.Seasons[0].Episodes[0].Name != "Fallback Episode" {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestSeriesRejectsMismatchedSeasonResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/tv/7" {
			_, _ = response.Write([]byte(`{"id":7,"name":"Show","seasons":[{"id":70,"name":"Season 1","season_number":1,"episode_count":1}]}`))
			return
		}
		_, _ = response.Write([]byte(`{"id":71,"name":"Season 1","season_number":1,"episodes":[]}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIToken: "token", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Series(context.Background(), 7); err == nil {
		t.Fatal("Series() error = nil")
	}
}

func TestSeriesReturnsStructuredHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIToken: "token", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Series(context.Background(), 7)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests || httpErr.Body != "rate limited" {
		t.Fatalf("Series() error = %#v", err)
	}
}
