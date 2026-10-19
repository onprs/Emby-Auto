package agentapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"testing/iotest"
)

func TestCompleteClassifiesNetworkFailuresByCause(t *testing.T) {
	for _, test := range []struct {
		name      string
		cause     error
		code      string
		retryable bool
	}{
		{name: "cancelled", cause: context.Canceled, code: "agent_request_cancelled"},
		{name: "deadline", cause: context.DeadlineExceeded, code: "agent_request_timeout", retryable: true},
		{name: "dns_timeout", cause: &net.DNSError{Err: "timeout", Name: "provider.example", IsTimeout: true}, code: "agent_request_timeout", retryable: true},
		{name: "connection_refused", cause: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, code: "agent_request_failed", retryable: true},
		{name: "transport_failure", cause: errors.New("transport unavailable"), code: "agent_request_failed", retryable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := agentTestClient(t, "")
			client.http.Transport = agentRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, test.cause })
			_, err := client.complete(context.Background(), chatRequest{})
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != test.code || apiErr.Retryable != test.retryable || !errors.Is(err, test.cause) {
				t.Fatalf("complete() = %#v, want code %s retryable %t with original cause", err, test.code, test.retryable)
			}
		})
	}
}

func TestCompletePreservesCancellationWhileReadingErrorResponse(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(fmt.Sprintf("status_%d/%s", status, cause), func(t *testing.T) {
				client := agentTestClient(t, "")
				client.http.Transport = agentRoundTripFunc(func(*http.Request) (*http.Response, error) {
					response := agentResponse("")
					response.StatusCode = status
					response.Body = io.NopCloser(iotest.ErrReader(cause))
					return response, nil
				})
				_, err := client.complete(context.Background(), chatRequest{})
				code := "agent_request_cancelled"
				if errors.Is(cause, context.DeadlineExceeded) {
					code = "agent_request_timeout"
				}
				var apiErr *Error
				if !errors.As(err, &apiErr) || apiErr.Code != code || apiErr.Retryable != errors.Is(cause, context.DeadlineExceeded) || !errors.Is(err, cause) {
					t.Fatalf("complete() = %#v, want %s preserving body read cause", err, code)
				}
			})
		}
	}
}
