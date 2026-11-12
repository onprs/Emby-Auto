package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testLogger 丢弃日志，仅用于让恢复器具备日志依赖。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRecovererWritesStructuredErrorBeforeResponseStarts(t *testing.T) {
	handler := recoverer(testLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("fixture panic")
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/fixture", nil))

	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"internal_error"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestRecovererDoesNotRewriteStartedResponse(t *testing.T) {
	handler := recoverer(testLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		panic("late fixture panic")
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/fixture", nil))

	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestRecovererIgnoresAbortHandlerAfterStreamingStarts(t *testing.T) {
	handler := recoverer(testLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: ready\n\n"))
		panic(http.ErrAbortHandler)
	}))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/events", nil))

	if response.Code != http.StatusOK || response.Body.String() != "event: ready\n\n" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
}
