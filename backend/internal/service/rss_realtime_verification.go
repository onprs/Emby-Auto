package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

// RSSRealtimeTargetVerifier performs Emby HTTP I/O outside state-transition
// transactions and records a bounded check ID that transactions can validate.
type RSSRealtimeTargetVerifier interface {
	VerifySubscription(context.Context, uuid.UUID) (uuid.UUID, error)
	VerifyEntry(context.Context, uuid.UUID) (uuid.UUID, error)
	VerifyCoordinates(context.Context, uuid.UUID, []domain.EpisodeCoordinate) (uuid.UUID, error)
}

type RSSRealtimeVerificationError struct {
	Code      string
	Message   string
	Retryable bool
	Cause     error
}

func (err *RSSRealtimeVerificationError) Error() string {
	if err.Cause == nil {
		return err.Message
	}
	return fmt.Sprintf("%s: %v", err.Message, err.Cause)
}

func (err *RSSRealtimeVerificationError) Unwrap() error { return err.Cause }
