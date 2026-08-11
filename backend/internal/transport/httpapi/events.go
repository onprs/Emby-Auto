package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

const eventBatchSize int32 = 100

func (server *Server) StreamEvents(
	ctx context.Context,
	request StreamEventsRequestObject,
) (StreamEventsResponseObject, error) {
	if server.events == nil {
		return StreamEvents503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}

	var cursor *uuid.UUID
	if request.Params.LastEventID != nil {
		parsed := uuid.UUID(*request.Params.LastEventID)
		cursor = &parsed
	}
	initial, err := server.events.List(ctx, cursor, eventBatchSize)
	if errors.Is(err, domain.ErrNotFound) {
		return StreamEvents409JSONResponse{ConflictJSONResponse: ConflictJSONResponse(ApiError{
			Code:      "event_cursor_not_found",
			Message:   "the event cursor is no longer available",
			Details:   map[string]any{"lastEventId": cursor.String()},
			RequestId: middleware.GetReqID(ctx),
		})}, nil
	}
	if err != nil {
		return StreamEvents503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}

	cacheControl := "no-cache, no-transform"
	buffering := "no"
	return StreamEvents200TexteventStreamResponse{
		Body: server.eventReader(ctx, cursor, initial),
		Headers: StreamEvents200ResponseHeaders{
			CacheControl:    &cacheControl,
			XAccelBuffering: &buffering,
		},
	}, nil
}

func (server *Server) eventReader(
	ctx context.Context,
	initialCursor *uuid.UUID,
	initial []domain.Event,
) io.Reader {
	reader, writer := io.Pipe()
	go func() {
		defer func() {
			_ = writer.Close()
		}()
		cursor := cloneUUID(initialCursor)
		if !writeEventBatch(writer, initial, &cursor) {
			return
		}

		pollInterval := server.eventPollInterval
		if pollInterval <= 0 {
			pollInterval = time.Second
		}
		poll := time.NewTicker(pollInterval)
		heartbeat := time.NewTicker(15 * time.Second)
		defer poll.Stop()
		defer heartbeat.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				if _, err := io.WriteString(writer, ": keep-alive\n\n"); err != nil {
					return
				}
			case <-poll.C:
				events, err := server.events.List(ctx, cursor, eventBatchSize)
				if err != nil {
					_, _ = io.WriteString(writer, "event: stream.error\ndata: {\"code\":\"stream_unavailable\"}\n\n")
					return
				}
				if !writeEventBatch(writer, events, &cursor) {
					return
				}
			}
		}
	}()
	return reader
}

func writeEventBatch(writer io.Writer, events []domain.Event, cursor **uuid.UUID) bool {
	for _, event := range events {
		if err := writeEvent(writer, event); err != nil {
			return false
		}
		id := event.ID
		*cursor = &id
	}
	return true
}

func writeEvent(writer io.Writer, event domain.Event) error {
	type eventEnvelope struct {
		ID           uuid.UUID       `json:"id"`
		Topic        string          `json:"topic"`
		ResourceType string          `json:"resourceType,omitempty"`
		ResourceID   *uuid.UUID      `json:"resourceId,omitempty"`
		OperationID  *uuid.UUID      `json:"operationId,omitempty"`
		Data         json.RawMessage `json:"data"`
		OccurredAt   time.Time       `json:"occurredAt"`
	}

	envelope := eventEnvelope{
		ID:           event.ID,
		Topic:        event.Topic,
		ResourceType: event.ResourceType,
		Data:         event.Data,
		OccurredAt:   event.OccurredAt,
	}
	if event.ResourceID != uuid.Nil {
		envelope.ResourceID = &event.ResourceID
	}
	if event.OperationID != uuid.Nil {
		envelope.OperationID = &event.OperationID
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode event payload: %w", err)
	}

	topic := strings.NewReplacer("\r", "", "\n", "").Replace(event.Topic)
	if _, err := fmt.Fprintf(writer, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, topic, payload); err != nil {
		return fmt.Errorf("write event payload: %w", err)
	}
	return nil
}

func cloneUUID(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
