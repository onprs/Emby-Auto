package worker

import (
	"context"
	"errors"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type AgentResolutionRunner interface {
	Run(context.Context, domain.Operation) error
}

type AgentResolveHandler struct {
	resolutions AgentResolutionRunner
}

func NewAgentResolveHandler(resolutions AgentResolutionRunner) *AgentResolveHandler {
	return &AgentResolveHandler{resolutions: resolutions}
}

func (handler *AgentResolveHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if handler.resolutions == nil {
		return &Failure{Code: "agent_not_configured", Message: "Agent resolution service is unavailable", Retryable: false}
	}
	if err := handler.resolutions.Run(ctx, operation); err != nil {
		var runErr *service.AgentRunError
		if errors.As(err, &runErr) {
			return &Failure{Code: runErr.Code, Message: runErr.Message, Retryable: runErr.Retryable, Cause: runErr}
		}
		return err
	}
	return nil
}
