package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type eventSourceStub struct {
	mutex      sync.Mutex
	events     []domain.Event
	stats      domain.EventStats
	err        error
	firstCall  chan struct{}
	signalOnce sync.Once
	cursors    []*uuid.UUID
}

func (stub *eventSourceStub) List(_ context.Context, cursor *uuid.UUID, _ int32) ([]domain.Event, error) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.cursors = append(stub.cursors, cloneUUID(cursor))
	stub.signalOnce.Do(func() {
		if stub.firstCall != nil {
			close(stub.firstCall)
		}
	})
	if stub.err != nil {
		return nil, stub.err
	}
	if len(stub.cursors) == 1 {
		return stub.events, nil
	}
	return nil, nil
}

func (stub *eventSourceStub) Stats(context.Context) (domain.EventStats, error) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	return stub.stats, stub.err
}

func TestEventStatsResponsePreservesNullableEarliestTimestamp(t *testing.T) {
	tests := []struct {
		name      string
		stats     domain.EventStats
		wantCount int64
		wantTime  *time.Time
	}{
		{
			name:      "empty events",
			stats:     domain.EventStats{},
			wantCount: 0,
		},
		{
			name: "non-empty events",
			stats: domain.EventStats{
				Count:              3,
				EarliestOccurredAt: timePointer(time.Date(2026, time.July, 21, 12, 30, 0, 0, time.UTC)),
			},
			wantCount: 3,
			wantTime:  timePointer(time.Date(2026, time.July, 21, 12, 30, 0, 0, time.UTC)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &eventSourceStub{stats: test.stats}
			handler := NewHandler(NewServer(readinessStub{}, WithEvents(source)))
			request := httptest.NewRequest(http.MethodGet, "/api/v1/events/stats", nil)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var body EventStats
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.EventCount != test.wantCount {
				t.Fatalf("event count = %d, want %d", body.EventCount, test.wantCount)
			}
			if test.wantTime == nil {
				if body.EarliestOccurredAt != nil {
					t.Fatalf("earliest timestamp = %v, want null", body.EarliestOccurredAt)
				}
			} else if body.EarliestOccurredAt == nil || !body.EarliestOccurredAt.Equal(*test.wantTime) {
				t.Fatalf("earliest timestamp = %v, want %v", body.EarliestOccurredAt, test.wantTime)
			}
		})
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func TestEventStreamWritesPublicUUIDCursorAndEnvelope(t *testing.T) {
	eventID := uuid.MustParse("10000000-0000-0000-0000-000000000010")
	source := &eventSourceStub{
		firstCall: make(chan struct{}),
		events: []domain.Event{{
			ID:         eventID,
			Topic:      "task.updated",
			Data:       json.RawMessage(`{"state":"processing"}`),
			OccurredAt: time.Date(2026, time.July, 21, 12, 30, 0, 0, time.UTC),
		}},
	}
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: uuid.New(), Username: "admin"}}}
	server := NewServer(readinessStub{}, WithAuthentication(authentication, false), WithEvents(source))
	server.eventPollInterval = time.Hour
	handler := NewHandler(server)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()

	<-source.firstCall
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not stop after request cancellation")
	}

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "text/event-stream" || response.Header().Get("Cache-Control") != "no-cache, no-transform" || response.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("SSE headers = %#v", response.Header())
	}
	want := "id: 10000000-0000-0000-0000-000000000010\n" +
		"event: task.updated\n" +
		"data: {\"id\":\"10000000-0000-0000-0000-000000000010\",\"topic\":\"task.updated\",\"data\":{\"state\":\"processing\"},\"occurredAt\":\"2026-07-21T12:30:00Z\"}\n\n"
	if response.Body.String() != want {
		t.Fatalf("SSE body = %q, want %q", response.Body.String(), want)
	}
}

func TestEventStreamRejectsUnknownLastEventID(t *testing.T) {
	cursor := "10000000-0000-0000-0000-000000000099"
	source := &eventSourceStub{err: domain.ErrNotFound}
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{ID: uuid.New(), Username: "admin"}}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithEvents(source)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	request.Header.Set("Last-Event-ID", cursor)
	request.Header.Set("X-Request-Id", "event-cursor-request")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(source.cursors) != 1 || source.cursors[0] == nil || source.cursors[0].String() != cursor {
		t.Fatalf("source cursors = %#v, want %s", source.cursors, cursor)
	}
	var body ApiError
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != "event_cursor_not_found" || body.Details["lastEventId"] != cursor || body.RequestId != "event-cursor-request" {
		t.Fatalf("error = %#v, want cursor conflict", body)
	}
}

func TestEventTopicCannotInjectSSEFields(t *testing.T) {
	var output strings.Builder
	event := domain.Event{
		ID:         uuid.MustParse("10000000-0000-0000-0000-000000000011"),
		Topic:      "task.updated\nid: injected",
		Data:       json.RawMessage(`{}`),
		OccurredAt: time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC),
	}
	if err := writeEvent(&output, event); err != nil {
		t.Fatalf("writeEvent() error = %v", err)
	}
	if strings.Contains(output.String(), "\nid: injected\n") {
		t.Fatalf("SSE output contains injected field: %q", output.String())
	}
	if !strings.Contains(output.String(), "event: task.updatedid: injected\n") {
		t.Fatalf("sanitized event name missing: %q", output.String())
	}
}
