package emby

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRefreshLibraryUsesTokenHeaderAndControlledEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/Library/Refresh" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("X-Emby-Token") != "secret-key" {
			t.Fatalf("token header = %q", request.Header.Get("X-Emby-Token"))
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret-key", RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.RefreshLibrary(context.Background()); err != nil {
		t.Fatalf("RefreshLibrary() error = %v", err)
	}
}

func TestRefreshLibraryReturnsStructuredHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "refresh unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret-key", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	err = client.RefreshLibrary(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusServiceUnavailable || httpErr.Body != "refresh unavailable" {
		t.Fatalf("RefreshLibrary() error = %#v", err)
	}
}

func TestCatalogEndpointsMapLibrariesAndItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Emby-Token") != "secret-key" {
			t.Fatalf("token header = %q", request.Header.Get("X-Emby-Token"))
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/Library/VirtualFolders":
			_, _ = response.Write([]byte(`[{"Name":"Anime","ItemId":"library-1","CollectionType":"tvshows","Locations":["/media/anime"],"Unknown":"kept"}]`))
		case "/Items":
			query := request.URL.Query()
			if query.Get("ParentId") != "library-1" || query.Get("Recursive") != "true" || query.Get("IncludeItemTypes") != "Series,Season,Episode,Movie" || query.Get("StartIndex") != "5" || query.Get("Limit") != "10" {
				t.Fatalf("items query = %s", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`{"Items":[{"Id":"episode-1","ParentId":"season-1","Type":"Episode","Name":"Pilot","Path":"/media/anime/Pilot.mkv","ProviderIds":{"Tmdb":"9001"},"IndexNumber":1,"ParentIndexNumber":2,"Unknown":"kept"}],"TotalRecordCount":6}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret-key", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	libraries, err := client.Libraries(context.Background())
	if err != nil {
		t.Fatalf("Libraries() error = %v", err)
	}
	if len(libraries) != 1 || libraries[0].EmbyID != "library-1" || libraries[0].Name != "Anime" || string(libraries[0].Payload) == "" {
		t.Fatalf("libraries = %#v", libraries)
	}
	page, err := client.LibraryItems(context.Background(), "library-1", 5, 10)
	if err != nil {
		t.Fatalf("LibraryItems() error = %v", err)
	}
	if page.TotalRecordCount != 6 || len(page.Items) != 1 {
		t.Fatalf("page = %#v", page)
	}
	item := page.Items[0]
	if item.ItemType != "Episode" || item.ProviderIDs["Tmdb"] != "9001" || item.SeasonNumber == nil || *item.SeasonNumber != 2 || item.EpisodeNumber == nil || *item.EpisodeNumber != 1 {
		t.Fatalf("item = %#v", item)
	}
}

func TestLibraryItemsAcceptsMovie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("IncludeItemTypes") != "Series,Season,Episode,Movie" {
			t.Fatalf("items query = %s", request.URL.RawQuery)
		}
		_, _ = response.Write([]byte(`{"Items":[{"Id":"movie-1","ParentId":"library-movies","Type":"Movie","Name":"Fixture Movie","Path":"/media/movies/Fixture Movie(2024)/Fixture Movie(2024).mp4","ProviderIds":{"Tmdb":"200"}}],"TotalRecordCount":1}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret-key", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.LibraryItems(context.Background(), "library-movies", 0, 10)
	if err != nil {
		t.Fatalf("LibraryItems(movie) error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ItemType != "Movie" || page.Items[0].Name != "Fixture Movie" || page.Items[0].ProviderIDs["Tmdb"] != "200" {
		t.Fatalf("movie page = %#v", page)
	}
}

func TestLibraryItemsRejectsInconsistentTotal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"Items":[{"Id":"episode-1","Type":"Episode","Name":"Pilot"}],"TotalRecordCount":0}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret-key", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.LibraryItems(context.Background(), "library-1", 0, 10); err == nil {
		t.Fatal("LibraryItems() error = nil")
	}
}

func TestSeriesEpisodesByTMDbUsesMappedTargetSeries(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/Items" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		query := request.URL.Query()
		switch requests {
		case 1:
			if query.Get("AnyProviderIdEquals") != "tmdb.500" || query.Get("IncludeItemTypes") != "Series" || query.Get("Recursive") != "true" {
				t.Fatalf("series query = %s", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`{"Items":[{"Id":"series-1","Type":"Series","Name":"Target Series","ProviderIds":{"Tmdb":"500"}}],"TotalRecordCount":1}`))
		case 2:
			if query.Get("ParentId") != "series-1" || query.Get("IncludeItemTypes") != "Episode" || query.Get("Recursive") != "true" {
				t.Fatalf("episode query = %s", request.URL.RawQuery)
			}
			_, _ = response.Write([]byte(`{"Items":[{"Id":"episode-1","ParentId":"season-2","Type":"Episode","Name":"Target Episode","Path":"/media/target.mkv","ProviderIds":{"Tmdb":"9001"},"IndexNumber":1,"ParentIndexNumber":2}],"TotalRecordCount":1}`))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret-key", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	episodes, err := client.SeriesEpisodesByTMDb(context.Background(), 500)
	if err != nil {
		t.Fatalf("SeriesEpisodesByTMDb() error = %v", err)
	}
	if len(episodes) != 1 || episodes[0].ProviderIDs["Tmdb"] != "9001" || episodes[0].SeasonNumber == nil || *episodes[0].SeasonNumber != 2 || episodes[0].EpisodeNumber == nil || *episodes[0].EpisodeNumber != 1 {
		t.Fatalf("episodes = %#v", episodes)
	}
}

func TestSeriesEpisodesByTMDbTreatsMissingSeriesAsEmpty(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = response.Write([]byte(`{"Items":[],"TotalRecordCount":0}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret-key", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	episodes, err := client.SeriesEpisodesByTMDb(context.Background(), 500)
	if err != nil {
		t.Fatalf("SeriesEpisodesByTMDb() error = %v", err)
	}
	if len(episodes) != 0 || requests != 1 {
		t.Fatalf("episodes = %#v, requests = %d", episodes, requests)
	}
}

func TestSeriesEpisodesByTMDbRejectsAmbiguousSeries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"Items":[{"Id":"series-1","Type":"Series","Name":"One"},{"Id":"series-2","Type":"Series","Name":"Two"}],"TotalRecordCount":2}`))
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret-key", RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SeriesEpisodesByTMDb(context.Background(), 500); err == nil {
		t.Fatal("SeriesEpisodesByTMDb() error = nil")
	}
}

func TestNewClientRejectsCredentialsInURL(t *testing.T) {
	if _, err := NewClient(ClientOptions{BaseURL: "http://user:pass@emby.test", APIKey: "key", RequestTimeout: time.Second}); err == nil {
		t.Fatal("NewClient(credentials URL) error = nil")
	}
}
