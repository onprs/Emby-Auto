package qbittorrent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	torrentHashA = "0123456789abcdef0123456789abcdef01234567"
	torrentHashB = "abcdef0123456789abcdef0123456789abcdef01"
)

func TestExtractBTIHSupportsHexAndBase32Magnets(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "hex", source: "magnet:?xt=urn%3Abtih%3A0123456789ABCDEF0123456789ABCDEF01234567&dn=show", want: torrentHashA},
		{name: "base32", source: "magnet:?xt=urn:btih:AERUKZ4JVPG66AJDIVTYTK6N54ASGRLH", want: torrentHashA},
		{name: "http URL", source: "https://example.test/show.torrent", want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ExtractBTIH(test.source); got != test.want {
				t.Fatalf("ExtractBTIH(%q) = %q, want %q", test.source, got, test.want)
			}
		})
	}
}

func TestIsTorrentCompleteRecognizesProgressAndSeedingStates(t *testing.T) {
	tests := []struct {
		name    string
		torrent Torrent
		want    bool
	}{
		{name: "progress complete", torrent: Torrent{Progress: 1}, want: true},
		{name: "nothing left", torrent: Torrent{AmountLeft: 0, TotalSize: 100}, want: true},
		{name: "seeding state", torrent: Torrent{Progress: 0.99, State: "stalledUP"}, want: true},
		{name: "still downloading", torrent: Torrent{Progress: 0.75, AmountLeft: 25, TotalSize: 100, State: "downloading"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsTorrentComplete(test.torrent); got != test.want {
				t.Fatalf("IsTorrentComplete(%#v) = %t, want %t", test.torrent, got, test.want)
			}
		})
	}
}

func TestResolveAddedTorrentHashRequiresExpectedOrSingleNewHash(t *testing.T) {
	tests := []struct {
		name       string
		before     []Torrent
		after      []Torrent
		source     string
		wantHash   string
		wantReason HashResolutionReason
		wantOK     bool
	}{
		{
			name:       "new magnet",
			before:     nil,
			after:      []Torrent{{Hash: torrentHashA}},
			source:     "magnet:?xt=urn:btih:" + strings.ToUpper(torrentHashA),
			wantHash:   torrentHashA,
			wantReason: HashResolutionMagnet,
			wantOK:     true,
		},
		{
			name:       "duplicate magnet",
			before:     []Torrent{{Hash: torrentHashA}},
			after:      []Torrent{{Hash: torrentHashA}},
			source:     "magnet:?xt=urn:btih:" + torrentHashA,
			wantHash:   torrentHashA,
			wantReason: HashResolutionExisting,
			wantOK:     true,
		},
		{
			name:       "single new URL torrent",
			before:     []Torrent{{Hash: torrentHashA}},
			after:      []Torrent{{Hash: torrentHashA}, {Hash: torrentHashB}},
			source:     "https://example.test/show.torrent",
			wantHash:   torrentHashB,
			wantReason: HashResolutionNew,
			wantOK:     true,
		},
		{
			name:   "ambiguous concurrent URL adds",
			before: nil,
			after:  []Torrent{{Hash: torrentHashA}, {Hash: torrentHashB}},
			source: "https://example.test/show.torrent",
			wantOK: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ResolveAddedTorrentHash(test.before, test.after, test.source)
			if ok != test.wantOK || got.Hash != test.wantHash || got.Reason != test.wantReason {
				t.Fatalf("ResolveAddedTorrentHash() = %#v/%t, want hash %q reason %q ok %t", got, ok, test.wantHash, test.wantReason, test.wantOK)
			}
		})
	}
}

func TestClientAddAndConfirmStopsAfterMetadataInAuthenticatedCategoryScope(t *testing.T) {
	var mu sync.Mutex
	loginCalls := 0
	infoCategories := make([]string, 0, 2)
	var addForm url.Values

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/auth/login":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse login form: %v", err)
			}
			if request.Form.Get("username") != "admin" || request.Form.Get("password") != "secret" {
				t.Errorf("login form = %v", request.Form)
			}
			loginCalls++
			http.SetCookie(response, &http.Cookie{Name: "SID", Value: "session", Path: "/"})
			_, _ = response.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if cookie, err := request.Cookie("SID"); err != nil || cookie.Value != "session" {
				t.Errorf("missing authenticated cookie: %v", err)
			}
			mu.Lock()
			infoCategories = append(infoCategories, request.URL.Query().Get("category"))
			call := len(infoCategories)
			mu.Unlock()
			response.Header().Set("Content-Type", "application/json")
			if call == 1 {
				_ = json.NewEncoder(response).Encode([]Torrent{})
				return
			}
			_ = json.NewEncoder(response).Encode([]Torrent{{Hash: torrentHashB, Name: "Show"}})
		case "/api/v2/torrents/add":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse add form: %v", err)
			}
			addForm = request.Form
			_, _ = response.Write([]byte("Ok."))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{
		BaseURL:        server.URL,
		Username:       "admin",
		Password:       "secret",
		RequestTimeout: time.Second,
		PollInterval:   time.Millisecond,
		ConfirmTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	resolution, err := client.AddAndConfirm(context.Background(), AddRequest{
		Source:   "https://example.test/show.torrent",
		SavePath: "/downloads/acquisition",
		Category: "emby-auto-acquisition-42",
	})
	if err != nil {
		t.Fatalf("AddAndConfirm() error = %v", err)
	}
	if resolution.Hash != torrentHashB || resolution.Reason != HashResolutionNew {
		t.Fatalf("resolution = %#v, want %s/new", resolution, torrentHashB)
	}
	if loginCalls != 1 {
		t.Fatalf("login calls = %d, want 1", loginCalls)
	}
	if !slices.Equal(infoCategories, []string{"emby-auto-acquisition-42", "emby-auto-acquisition-42"}) {
		t.Fatalf("info categories = %v", infoCategories)
	}
	if addForm.Get("urls") != "https://example.test/show.torrent" ||
		addForm.Get("savepath") != "/downloads/acquisition" ||
		addForm.Get("category") != "emby-auto-acquisition-42" ||
		addForm.Get("stopped") != "false" ||
		addForm.Get("stopCondition") != "MetadataReceived" ||
		addForm.Has("paused") {
		t.Fatalf("add form = %v", addForm)
	}
}

func TestClientAddAndConfirmReusesSingleTorrentInUniqueCategoryOnRetry(t *testing.T) {
	addCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(response).Encode([]Torrent{{Hash: torrentHashB, Category: "emby-auto-download"}})
		case "/api/v2/torrents/add":
			addCalls++
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	resolution, err := client.AddAndConfirm(context.Background(), AddRequest{
		Source:   "https://example.test/show.torrent",
		Category: "emby-auto-download",
	})
	if err != nil {
		t.Fatalf("AddAndConfirm() error = %v", err)
	}
	if resolution.Hash != torrentHashB || resolution.Reason != HashResolutionExisting || addCalls != 0 {
		t.Fatalf("resolution/add calls = %#v/%d, want existing %s and no duplicate add", resolution, addCalls, torrentHashB)
	}
}

func TestClientEnsureCategoryCreatesMissingCategoryOnce(t *testing.T) {
	categories := map[string]map[string]string{}
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/torrents/categories":
			_ = json.NewEncoder(response).Encode(categories)
		case "/api/v2/torrents/createCategory":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse create category form: %v", err)
			}
			createCalls++
			category := request.Form.Get("category")
			if category != ManagedCategory || request.Form.Get("savePath") != "" {
				t.Errorf("create category form = %v", request.Form)
			}
			categories[category] = map[string]string{"name": category, "savePath": ""}
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.EnsureCategory(context.Background(), ManagedCategory); err != nil {
		t.Fatalf("EnsureCategory(first) error = %v", err)
	}
	if err := client.EnsureCategory(context.Background(), ManagedCategory); err != nil {
		t.Fatalf("EnsureCategory(second) error = %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("create category calls = %d, want 1", createCalls)
	}
}

func TestClientEnsureCategoryAcceptsConcurrentCreation(t *testing.T) {
	listCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/torrents/categories":
			listCalls++
			if listCalls == 1 {
				_, _ = response.Write([]byte(`{}`))
				return
			}
			_, _ = response.Write([]byte(`{"emby_auto":{"name":"emby_auto","savePath":""}}`))
		case "/api/v2/torrents/createCategory":
			http.Error(response, "Incorrect category name", http.StatusConflict)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.EnsureCategory(context.Background(), ManagedCategory); err != nil {
		t.Fatalf("EnsureCategory() error = %v", err)
	}
	if listCalls != 2 {
		t.Fatalf("category list calls = %d, want 2", listCalls)
	}
}

func TestClientSetsPerTorrentRateLimits(t *testing.T) {
	forms := map[string]url.Values{}
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/torrents/setDownloadLimit", "/api/v2/torrents/setUploadLimit":
			requestCount++
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse rate limit form: %v", err)
			}
			forms[request.URL.Path] = request.Form
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.SetTorrentRateLimits(context.Background(), torrentHashA, 2*1024*1024, 0); err != nil {
		t.Fatalf("SetTorrentRateLimits() error = %v", err)
	}
	download := forms["/api/v2/torrents/setDownloadLimit"]
	upload := forms["/api/v2/torrents/setUploadLimit"]
	if download.Get("hashes") != torrentHashA || download.Get("limit") != "2097152" {
		t.Fatalf("download limit form = %v", download)
	}
	if upload.Get("hashes") != torrentHashA || upload.Get("limit") != "0" {
		t.Fatalf("upload limit form = %v", upload)
	}

	if err := client.SetTorrentRateLimits(context.Background(), torrentHashA, 2147483646, 0); err != nil {
		t.Fatalf("SetTorrentRateLimits() rejected the largest effective limit: %v", err)
	}
	if got := forms["/api/v2/torrents/setDownloadLimit"].Get("limit"); got != "2147483646" {
		t.Fatalf("largest download limit form = %q, want 2147483646", got)
	}

	for _, test := range []struct {
		name     string
		download int64
		upload   int64
	}{
		{name: "negative", download: -1},
		{name: "qBittorrent unlimited sentinel", download: 2147483647},
		{name: "above signed int range", download: 2147483648},
		{name: "invalid upload", upload: 2147483647},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := client.SetTorrentRateLimits(context.Background(), torrentHashA, test.download, test.upload); err == nil {
				t.Fatalf("SetTorrentRateLimits() accepted download/upload limits %d/%d", test.download, test.upload)
			}
		})
	}
	if requestCount != 4 {
		t.Fatalf("rate limit request count = %d, want only four requests for valid calls", requestCount)
	}
}

func TestClientControlsTaggedTorrentBatches(t *testing.T) {
	forms := map[string][]url.Values{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/torrents/addTags", "/api/v2/torrents/removeTags", "/api/v2/torrents/start", "/api/v2/torrents/stop":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse torrent control form: %v", err)
			}
			forms[request.URL.Path] = append(forms[request.URL.Path], request.Form)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	hashes := []string{strings.ToUpper(torrentHashB), torrentHashA, torrentHashA}
	if err := client.AddTorrentTags(context.Background(), hashes, RuntimePausedTag); err != nil {
		t.Fatalf("AddTorrentTags() error = %v", err)
	}
	if err := client.StopTorrents(context.Background(), hashes); err != nil {
		t.Fatalf("StopTorrents() error = %v", err)
	}
	if err := client.ResumeTorrents(context.Background(), hashes); err != nil {
		t.Fatalf("ResumeTorrents() error = %v", err)
	}
	if err := client.RemoveTorrentTags(context.Background(), hashes, RuntimePausedTag); err != nil {
		t.Fatalf("RemoveTorrentTags() error = %v", err)
	}

	wantHashes := torrentHashA + "|" + torrentHashB
	for _, path := range []string{"/api/v2/torrents/addTags", "/api/v2/torrents/removeTags", "/api/v2/torrents/start", "/api/v2/torrents/stop"} {
		if len(forms[path]) != 1 || forms[path][0].Get("hashes") != wantHashes {
			t.Fatalf("%s forms = %v, want hashes %q", path, forms[path], wantHashes)
		}
	}
	if forms["/api/v2/torrents/addTags"][0].Get("tags") != RuntimePausedTag || forms["/api/v2/torrents/removeTags"][0].Get("tags") != RuntimePausedTag {
		t.Fatalf("tag forms = %v / %v", forms["/api/v2/torrents/addTags"], forms["/api/v2/torrents/removeTags"])
	}
}

func TestClientTorrentRuntimePauseClassification(t *testing.T) {
	if !TorrentHasTag(Torrent{Tags: "manual, " + RuntimePausedTag}, RuntimePausedTag) {
		t.Fatal("TorrentHasTag() did not find the runtime pause tag")
	}
	if TorrentHasTag(Torrent{Tags: "manual"}, RuntimePausedTag) {
		t.Fatal("TorrentHasTag() matched an absent tag")
	}
	for _, state := range []string{"pausedDL", "pausedUP", "stoppedDL", "stoppedUP"} {
		if !IsTorrentStopped(Torrent{State: state}) {
			t.Fatalf("IsTorrentStopped(%q) = false", state)
		}
	}
	if IsTorrentStopped(Torrent{State: "stalledDL"}) {
		t.Fatal("IsTorrentStopped(stalledDL) = true")
	}
}

func TestClientAddAndConfirmUsesMultipartForTorrentBytes(t *testing.T) {
	torrentBytes := []byte("d8:announce35:http://tracker.example.test:8080/announce4:infod6:lengthi999e4:name8:test.mkveee")
	var capturedFields map[string]string
	var capturedFilename, capturedContentType string
	var capturedBytes []byte
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/torrents/info":
			if strings.Contains(request.URL.RawQuery, "category=") && request.URL.Query().Get("category") != "emby-auto-test-category" {
				t.Errorf("unexpected category query %q", request.URL.RawQuery)
			}
			requestedCategory := request.URL.Query().Get("category")
			if capturedBytes == nil {
				_ = json.NewEncoder(response).Encode([]Torrent{})
				return
			}
			if requestedCategory == "emby-auto-test-category" {
				_ = json.NewEncoder(response).Encode([]Torrent{{Hash: torrentHashA}})
				return
			}
			_ = json.NewEncoder(response).Encode([]Torrent{})
		case "/api/v2/torrents/add":
			contentType := request.Header.Get("Content-Type")
			if !strings.HasPrefix(contentType, "multipart/form-data;") {
				t.Errorf("Content-Type = %q, want multipart/form-data", contentType)
			}
			_, params, err := mime.ParseMediaType(contentType)
			if err != nil {
				t.Fatalf("ParseMediaType() error = %v", err)
			}
			reader := multipart.NewReader(request.Body, params["boundary"])
			fields := map[string]string{}
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("multipart NextPart() error = %v", err)
				}
				data, err := io.ReadAll(part)
				if err != nil {
					t.Fatalf("ReadAll() error = %v", err)
				}
				formName := part.FormName()
				if formName == "torrents" {
					capturedFilename = part.FileName()
					capturedContentType = part.Header.Get("Content-Type")
					capturedBytes = append([]byte(nil), data...)
				} else {
					fields[formName] = string(data)
				}
			}
			capturedFields = fields
			if fields["savepath"] != "/downloads/test" || fields["category"] != "emby-auto-test-category" || fields["stopped"] != "false" || fields["stopCondition"] != "MetadataReceived" {
				t.Errorf("fields = %#v", fields)
			}
			if fields["urls"] != "" {
				t.Errorf("unexpected urls field %q for torrent upload", fields["urls"])
			}
			_, _ = response.Write([]byte("Ok."))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, RequestTimeout: time.Second, PollInterval: time.Millisecond, ConfirmTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	resolution, err := client.AddAndConfirm(context.Background(), AddRequest{
		Torrent:         torrentBytes,
		TorrentFilename: "source.torrent",
		SavePath:        "/downloads/test",
		Category:        "emby-auto-test-category",
	})
	if err != nil {
		t.Fatalf("AddAndConfirm() error = %v", err)
	}
	if resolution.Hash != torrentHashA || resolution.Reason != HashResolutionNew {
		t.Fatalf("resolution = %#v, want %s/new", resolution, torrentHashA)
	}
	if capturedFilename != "source.torrent" || capturedContentType != "application/x-bittorrent" || !bytes.Equal(capturedBytes, torrentBytes) {
		t.Fatalf("multipart torrents part = filename %q type %q bytes %v", capturedFilename, capturedContentType, capturedBytes)
	}
	if capturedFields["savepath"] != "/downloads/test" {
		t.Fatalf("savepath not preserved in multipart")
	}
}

func TestClientAddAndConfirmRejectsInvalidTorrentAndSurfaces415(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(response).Encode([]Torrent{})
		case "/api/v2/torrents/add":
			http.Error(response, "torrent file is invalid", http.StatusUnsupportedMediaType)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.AddAndConfirm(context.Background(), AddRequest{
		Torrent:         []byte("d8:announce31:http://tracker:8080/announce4:infod6:lengthi1e3:fooeee"),
		TorrentFilename: "source.torrent",
		SavePath:        "/downloads/test",
		Category:        "emby-auto-test-category",
	})
	var httpErr *HTTPError
	if !bytes.Contains([]byte(err.Error()), []byte("415")) || !errorsAsHTTPError(err, &httpErr) || httpErr.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("AddAndConfirm() error = %v, want HTTP 415", err)
	}
}

func errorsAsHTTPError(err error, target **HTTPError) bool {
	// helper to avoid importing errors at top for this file
	return httpErrorAs(err, target)
}

func httpErrorAs(err error, target **HTTPError) bool {
	for err != nil {
		if h, ok := err.(*HTTPError); ok {
			*target = h
			return true
		}
		err = unwrapErr(err)
	}
	return false
}

func TestClientAddAndConfirmReusesExistingTorrentForFileUpload(t *testing.T) {
	addCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(response).Encode([]Torrent{{Hash: torrentHashA, Category: "emby-auto-123"}})
		case "/api/v2/torrents/add":
			addCalls++
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	resolution, err := client.AddAndConfirm(context.Background(), AddRequest{
		Torrent:         []byte("d8:announce31:http://tracker:8080/announce4:infod6:lengthi1e3:fooeee"),
		TorrentFilename: "source.torrent",
		SavePath:        "/downloads/123",
		Category:        "emby-auto-123",
	})
	if err != nil || resolution.Hash != torrentHashA || resolution.Reason != HashResolutionExisting || addCalls != 0 {
		t.Fatalf("resolution/addCalls = %#v/%d, want existing %s without add", resolution, addCalls, torrentHashA)
	}
}

func unwrapErr(err error) error {
	type unwrapper interface{ Unwrap() error }
	if ue, ok := err.(unwrapper); ok {
		return ue.Unwrap()
	}
	return nil
}

func TestClientManagesFilesCategoriesAndExplicitDeletion(t *testing.T) {
	var priorityForms []url.Values
	var deleteForms []url.Values
	var categoryForm, removeCategoryForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/torrents/files":
			if request.URL.Query().Get("hash") != torrentHashA {
				t.Errorf("files hash = %q", request.URL.Query().Get("hash"))
			}
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode([]TorrentFile{
				{Index: 0, Name: "Show - 01.mkv", Size: 1000, Priority: 1},
				{Index: 1, Name: "NCOP.mkv", Size: 2000, Priority: 1},
			})
		case "/api/v2/torrents/filePrio":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse priority form: %v", err)
			}
			priorityForms = append(priorityForms, request.Form)
		case "/api/v2/torrents/setCategory":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse category form: %v", err)
			}
			categoryForm = request.Form
		case "/api/v2/torrents/removeCategories":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse remove category form: %v", err)
			}
			removeCategoryForm = request.Form
		case "/api/v2/torrents/delete":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse delete form: %v", err)
			}
			deleteForms = append(deleteForms, request.Form)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	files, err := client.TorrentFiles(context.Background(), torrentHashA)
	if err != nil {
		t.Fatalf("TorrentFiles() error = %v", err)
	}
	if len(files) != 2 || files[0].Name != "Show - 01.mkv" || files[1].Index != 1 {
		t.Fatalf("files = %#v", files)
	}
	if err := client.SetFilePriority(context.Background(), torrentHashA, []int{1}, 0); err != nil {
		t.Fatalf("SetFilePriority(unwanted) error = %v", err)
	}
	if err := client.SetFilePriority(context.Background(), torrentHashA, []int{0}, 1); err != nil {
		t.Fatalf("SetFilePriority(wanted) error = %v", err)
	}
	if err := client.SetTorrentCategory(context.Background(), torrentHashA, ManagedCategory); err != nil {
		t.Fatalf("SetTorrentCategory() error = %v", err)
	}
	if err := client.DeleteCategory(context.Background(), "emby-auto-download-id"); err != nil {
		t.Fatalf("DeleteCategory() error = %v", err)
	}
	if err := client.DeleteTorrent(context.Background(), torrentHashA, false); err != nil {
		t.Fatalf("DeleteTorrent(keep files) error = %v", err)
	}
	if err := client.DeleteTorrent(context.Background(), torrentHashA, true); err != nil {
		t.Fatalf("DeleteTorrent(delete files) error = %v", err)
	}

	if len(priorityForms) != 2 || priorityForms[0].Get("id") != "1" || priorityForms[0].Get("priority") != "0" || priorityForms[1].Get("id") != "0" || priorityForms[1].Get("priority") != "1" {
		t.Fatalf("priority forms = %v", priorityForms)
	}
	if categoryForm.Get("hashes") != torrentHashA || categoryForm.Get("category") != ManagedCategory || removeCategoryForm.Get("categories") != "emby-auto-download-id" {
		t.Fatalf("category forms = %v/%v", categoryForm, removeCategoryForm)
	}
	if len(deleteForms) != 2 || deleteForms[0].Get("hashes") != torrentHashA || deleteForms[0].Get("deleteFiles") != "false" || deleteForms[1].Get("deleteFiles") != "true" {
		t.Fatalf("delete forms = %v", deleteForms)
	}
}

func TestNewIsolatedQBTransportRemovesProxy(t *testing.T) {
	proxyURL := mustParseURL(t, "http://proxy.example.test:8080")
	base := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	isolated, err := newIsolatedQBTransport(base)
	if err != nil {
		t.Fatalf("newIsolatedQBTransport() error = %v", err)
	}
	if isolated == base {
		t.Fatalf("isolated transport should be a clone")
	}
	if isolated.Proxy != nil {
		req := &http.Request{URL: mustParseURL(t, "http://example.test/file.torrent")}
		if got, _ := isolated.Proxy(req); got != nil {
			t.Fatalf("隔离后的 qB 传输不应使用代理， got %v", got)
		}
	}
	// 验证未修改原始 base 的代理行为
	if base.Proxy == nil {
		t.Fatalf("base transport proxy should remain")
	}
	req := &http.Request{URL: mustParseURL(t, "http://example.test/file.torrent")}
	if got, _ := base.Proxy(req); got == nil || got.String() != proxyURL.String() {
		t.Fatalf("base proxy should remain %v, got %v", proxyURL, got)
	}
}

func TestNewClientPreservesExplicitTransportProxy(t *testing.T) {
	proxyURL := mustParseURL(t, "http://proxy.example.test:8080")
	explicit := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	explicitClient := &http.Client{Transport: explicit}
	client, err := NewClient(ClientOptions{BaseURL: "http://qb:8080", RequestTimeout: time.Second, HTTPClient: explicitClient})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || transport != explicit {
		t.Fatalf("显式传输应被保留， got %T %v", client.httpClient.Transport, client.httpClient.Transport)
	}
	req := &http.Request{URL: mustParseURL(t, "http://example.test/file.torrent")}
	if got, _ := transport.Proxy(req); got == nil || got.String() != proxyURL.String() {
		t.Fatalf("显式代理应保留 %v, got %v", proxyURL, got)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", raw, err)
	}
	return parsed
}

type fakeDefaultRoundTripper struct{}

func (f *fakeDefaultRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, nil }

func TestNewIsolatedQBTransportRejectsUnexpectedType(t *testing.T) {
	_, err := newIsolatedQBTransport(&fakeDefaultRoundTripper{})
	if err == nil || !strings.Contains(err.Error(), "*http.Transport") {
		t.Fatalf("newIsolatedQBTransport() error = %v, want configuration error about *http.Transport", err)
	}
}

func TestResolveTorrentBySavePathRequiresExactNormalizedMatch(t *testing.T) {
	savePath := "/downloads/30000000-0000-0000-0000-000000000001"
	tests := []struct {
		name     string
		torrents []Torrent
		savePath string
		wantHash string
		wantOK   bool
		wantErr  bool
	}{
		{
			name:     "exact match in temporary category",
			torrents: []Torrent{{Hash: torrentHashA, SavePath: savePath, Category: "emby-auto-30000000-0000-0000-0000-000000000001"}},
			savePath: savePath,
			wantHash: torrentHashA,
			wantOK:   true,
		},
		{
			name:     "exact match in managed category",
			torrents: []Torrent{{Hash: torrentHashA, SavePath: savePath, Category: ManagedCategory}},
			savePath: savePath,
			wantHash: torrentHashA,
			wantOK:   true,
		},
		{
			name:     "trailing separator is normalized",
			torrents: []Torrent{{Hash: torrentHashA, SavePath: savePath + "/", Category: ManagedCategory}},
			savePath: savePath,
			wantHash: torrentHashA,
			wantOK:   true,
		},
		{
			name:     "case sensitive mismatch",
			torrents: []Torrent{{Hash: torrentHashA, SavePath: strings.ToUpper(savePath), Category: ManagedCategory}},
			savePath: savePath,
			wantOK:   false,
		},
		{
			name:     "separator style preserved not cleaned",
			torrents: []Torrent{{Hash: torrentHashA, SavePath: strings.ReplaceAll(savePath, "/", "\\"), Category: ManagedCategory}},
			savePath: savePath,
			wantOK:   false,
		},
		{
			name:     "no match",
			torrents: []Torrent{{Hash: torrentHashA, SavePath: "/downloads/other", Category: ManagedCategory}},
			savePath: savePath,
			wantOK:   false,
		},
		{
			name:     "ambiguous multiple hashes",
			torrents: []Torrent{{Hash: torrentHashA, SavePath: savePath}, {Hash: torrentHashB, SavePath: savePath}},
			savePath: savePath,
			wantErr:  true,
		},
		{
			name:     "invalid hash is ambiguous",
			torrents: []Torrent{{Hash: "invalid", SavePath: savePath}},
			savePath: savePath,
			wantErr:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok, err := ResolveTorrentBySavePath(test.torrents, test.savePath)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ResolveTorrentBySavePath() error = nil, want ambiguous")
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveTorrentBySavePath() error = %v", err)
			}
			if ok != test.wantOK || got.Hash != test.wantHash {
				t.Fatalf("ResolveTorrentBySavePath() = %#v/%t, want hash %q ok %t", got, ok, test.wantHash, test.wantOK)
			}
		})
	}
}
