package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const (
	configurationSecretsRevealPath   = "/api/v1/config/secrets/reveal"
	configurationSecretsCacheControl = "no-store, max-age=0"
)

// NewHandler wires generated routes to the strict server implementation.
func NewHandler(server StrictServerInterface) http.Handler {
	strictHandler := NewStrictHandlerWithOptions(server, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "the request is invalid")
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		},
	})

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(requestIDHeader)
	router.Use(configurationSecretsNoStore)
	router.Use(recoverer)
	if concreteServer, ok := server.(*Server); ok && concreteServer.auth != nil {
		router.Use(sessionAuthentication(concreteServer.auth))
	}
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "the requested resource was not found")
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "the request method is not allowed")
	})

	return HandlerFromMux(strictHandler, router)
}

func configurationSecretsNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == configurationSecretsRevealPath {
			w.Header().Set("Cache-Control", configurationSecretsCacheControl)
		}
		next.ServeHTTP(w, r)
	})
}

func requestIDHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(middleware.RequestIDHeader, middleware.GetReqID(r.Context()))
		next.ServeHTTP(w, r)
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		defer func() {
			recovered := recover()
			if recovered == nil || recovered == http.ErrAbortHandler {
				return
			}
			if wrapped.Status() == 0 {
				writeError(wrapped, r, http.StatusInternalServerError, "internal_error", "an internal error occurred")
			}
		}()
		next.ServeHTTP(wrapped, r)
	})
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeAPIError(w, status, ApiError{
		Code:      code,
		Message:   message,
		Details:   map[string]interface{}{},
		RequestId: middleware.GetReqID(r.Context()),
	})
}

func writeAPIError(w http.ResponseWriter, status int, apiError ApiError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError)
}

func badRequestError(ctx context.Context, message string) BadRequestJSONResponse {
	return BadRequestJSONResponse(ApiError{
		Code:      "invalid_request",
		Message:   message,
		Details:   map[string]any{},
		RequestId: middleware.GetReqID(ctx),
	})
}

func serviceUnavailableError(ctx context.Context, dependency string) ServiceUnavailableJSONResponse {
	return ServiceUnavailableJSONResponse(ApiError{
		Code:      "service_unavailable",
		Message:   "a required dependency is unavailable",
		Details:   map[string]any{"dependency": dependency},
		RequestId: middleware.GetReqID(ctx),
	})
}
