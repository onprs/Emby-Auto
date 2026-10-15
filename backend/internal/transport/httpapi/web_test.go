package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestWebHandler(t *testing.T, api http.Handler) *WebHandler {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"index.html":         "<!doctype html><title>Emby Auto</title>",
		"assets/app-hash.js": "console.log('ready')",
		".env":               "private fixture",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := NewWebHandler(api, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	return handler
}

func TestWebHandlerServesSetupPageAndSPARoutesWithoutDatabase(t *testing.T) {
	api := SetupOnly(NewHandler(NewServer(nil)))
	handler := newTestWebHandler(t, api)
	for _, target := range []string{"/", "/tasks", "/settings/", "/rss/subscriptions"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "<title>Emby Auto</title>") {
			t.Fatalf("%s: response = %d %s", target, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(response.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s: headers = %v", target, response.Header())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil))
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"not_found"`) {
		t.Fatalf("setup API boundary = %d %s", response.Code, response.Body.String())
	}
}

func TestWebHandlerPreservesAPIAuthenticationStreamingAndRangeHeaders(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: ready\n\n")
			w.(http.Flusher).Flush()
		case "/api/v1/tasks/fixture/artifacts/video":
			if r.Header.Get("Range") != "bytes=3-5" || r.Header.Get("If-Range") != "fixture-etag" {
				t.Error("网页处理器修改了 API Range 请求")
			}
			w.WriteHeader(http.StatusPartialContent)
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"unauthorized"}}`)
		}
	})
	handler := newTestWebHandler(t, api)
	for _, target := range []string{"/api", "/api/v1/tasks", "/api/missing.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "unauthorized") {
			t.Fatalf("%s: API response = %d %s", target, response.Code, response.Body.String())
		}
	}
	stream := httptest.NewRecorder()
	handler.ServeHTTP(stream, httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))
	if !stream.Flushed || stream.Body.String() != "event: ready\n\n" {
		t.Fatalf("SSE = %q flushed=%t", stream.Body.String(), stream.Flushed)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/fixture/artifacts/video", nil)
	request.Header.Set("Range", "bytes=3-5")
	request.Header.Set("If-Range", "fixture-etag")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent {
		t.Fatalf("API Range response = %d", response.Code)
	}
}

func TestWebHandlerRejectsTraversalDirectoriesHiddenFilesAndEscapingSymlinks(t *testing.T) {
	handler := newTestWebHandler(t, http.NotFoundHandler())
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside private content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := handler.root.Symlink(outside, "escape.txt"); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/.env", "/.git/config", "/../secret.txt", "/%2e%2e/secret.txt", "/assets/", "/escape.txt", "/assets/missing.js", "/assets/missing", "/missing.css"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s: response = %d %s", target, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "private") {
			t.Fatalf("%s: 泄露了网页目录以外的内容", target)
		}
	}
}

func TestWebHandlerSupportsAssetCachingHeadAndRange(t *testing.T) {
	handler := newTestWebHandler(t, http.NotFoundHandler())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodHead, "/assets/app-hash.js", nil))
	if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("HEAD = %d %s %v", response.Code, response.Body.String(), response.Header())
	}
	request := httptest.NewRequest(http.MethodGet, "/assets/app-hash.js", nil)
	request.Header.Set("Range", "bytes=0-6")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "console" {
		t.Fatalf("asset Range = %d %q", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST = %d %v", response.Code, response.Header())
	}
}

func TestWebHandlerRequiresAReadableIndexInsideTheWebRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := NewWebHandler(http.NotFoundHandler(), root); err == nil {
		t.Fatal("缺少首页时仍然启动了网页入口")
	}
	if err := os.Mkdir(filepath.Join(root, "index.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWebHandler(http.NotFoundHandler(), root); err == nil {
		t.Fatal("首页为目录时仍然启动了网页入口")
	}
}
