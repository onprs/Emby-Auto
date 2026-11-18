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

	SubtitleAttempt *SubtitleAttemptHandle `json:"-"`
}

// SubtitleAttemptHandle 是 StartAttempt 捕获的字幕尝试身份；只在内部传递，不序列化到 API。
type SubtitleAttemptHandle struct {
	OperationID           uuid.UUID
	JobAttempt            int
	AttemptID             uuid.UUID
	OperationAttemptCount int
	ResourceType          string
	ResourceID            uuid.UUID
}

// Valid 判断捕获的尝试身份是否完整且属于 episode_task。
func (handle SubtitleAttemptHandle) Valid() bool {
	return handle.OperationID != uuid.Nil &&
		handle.JobAttempt > 0 &&
		handle.AttemptID != uuid.Nil &&
		handle.OperationAttemptCount > 0 &&
		handle.ResourceType == "episode_task" &&
		handle.ResourceID != uuid.Nil
}
