package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Event struct {
	ID           uuid.UUID
	Topic        string
	ResourceType string
	ResourceID   uuid.UUID
	OperationID  uuid.UUID
	Data         json.RawMessage
	OccurredAt   time.Time
}
