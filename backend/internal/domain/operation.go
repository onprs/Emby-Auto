package domain

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrOperationNotRunnable = errors.New("operation is not runnable")

type Operation struct {
	ID             uuid.UUID
	Kind           string
	ResourceType   string
	ResourceID     uuid.UUID
	IdempotencyKey string
	Status         string
	RiverJobID     int64
	MaxAttempts    int
	AttemptCount   int
	Timeout        time.Duration
	Payload        json.RawMessage
}
