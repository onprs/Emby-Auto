package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type EventStats struct {
	Count              int64
	EarliestOccurredAt *time.Time
}

type Event struct {
	ID           uuid.UUID
	Topic        string
	ResourceType string
	ResourceID   uuid.UUID
	OperationID  uuid.UUID
	Data         json.RawMessage
	OccurredAt   time.Time
}
