package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type agentResolutionRunnerStub struct {
	err error
}

func (stub agentResolutionRunnerStub) Run(context.Context, domain.Operation) error {
	return stub.err
}

func TestAgentResolveHandlerPreservesRetryableSubmissionExhaustion(t *testing.T) {
	handler := NewAgentResolveHandler(agentResolutionRunnerStub{err: &service.AgentRunError{
		Code: "agent_submission_exhausted", Message: "agent_submission_exhausted", Retryable: true,
	}})
	err := handler.Handle(context.Background(), domain.Operation{ID: uuid.New(), Kind: "agent.resolve"})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "agent_submission_exhausted" || !failure.Retryable {
		t.Fatalf("Handle() error = %#v, want retryable submission exhaustion", err)
	}
}
