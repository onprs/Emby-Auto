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

func (server *Server) GetEventStats(
	ctx context.Context,
	_ GetEventStatsRequestObject,
) (GetEventStatsResponseObject, error) {
	if server.events == nil {
		return GetEventStats503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}
	stats, err := server.events.Stats(ctx)
	if err != nil {
		return GetEventStats503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}
	response := EventStats{EventCount: stats.Count}
	if stats.EarliestOccurredAt != nil {
		earliest := stats.EarliestOccurredAt.UTC()
		response.EarliestOccurredAt = &earliest
	}
	return GetEventStats200JSONResponse(response), nil
}

func (server *Server) StreamEvents(
	ctx context.Context,
	request StreamEventsRequestObject,
) (StreamEventsResponseObject, error) {
	if server.events == nil {
		return StreamEvents503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}

	var cursor *uuid.UUID
	var initial []domain.Event
	// 没有游标时以“当前可见最新事件”为边界，先发一个 stream.started 帧；客户端收到边界后
	// 全量重读，避免重放整个保留期历史。head 是当前可见最大 event_sequence，不是事务
	// settled 水位：更早取得 sequence、但在边界之后才提交的事务不会被该游标重放。
	started := request.Params.LastEventID == nil
	if started {
		head, err := server.events.Head(ctx)
		if err != nil {
			return StreamEvents503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
		}
		cursor = head
	} else {
		parsed := uuid.UUID(*request.Params.LastEventID)
		cursor = &parsed
		var err error
		initial, err = server.events.List(ctx, cursor, eventBatchSize)
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
	}

	cacheControl := "no-cache, no-transform"
	buffering := "no"
	return StreamEvents200TexteventStreamResponse{
		Body: server.eventReader(ctx, cursor, initial, started),
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
	started bool,
) io.Reader {
	reader, writer := io.Pipe()
	go func() {
		stopCancellation := context.AfterFunc(ctx, func() { _ = writer.Close() })
		defer stopCancellation()
		defer func() {
			_ = writer.Close()
		}()
		// 立即发送保活注释，使空闲连接也能在客户端超时前收到响应头。
		if _, err := io.WriteString(writer, ": connected\n\n"); err != nil {
			return
		}
		cursor := cloneUUID(initialCursor)
		if started && !writeStreamStarted(writer, cursor) {
			return
		}
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

// writeStreamStarted 写入订阅边界帧。id 是当前最新事件的公开游标，
// 客户端据此确认从哪个水位开始接收后续事件。
func writeStreamStarted(writer io.Writer, cursor *uuid.UUID) bool {
	payload, err := json.Marshal(struct {
		Cursor *uuid.UUID `json:"cursor,omitempty"`
	}{Cursor: cursor})
	if err != nil {
		return false
	}
	if cursor == nil {
		_, err = fmt.Fprintf(writer, "event: stream.started\ndata: %s\n\n", payload)
		return err == nil
	}
	_, err = fmt.Fprintf(writer, "id: %s\nevent: stream.started\ndata: %s\n\n", cursor.String(), payload)
	return err == nil
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
