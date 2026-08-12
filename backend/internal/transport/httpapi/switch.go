package httpapi

import (
	"net/http"
	"sync"
)

type SwitchingHandler struct {
	mu      sync.RWMutex
	handler http.Handler
}

func NewSwitchingHandler(handler http.Handler) *SwitchingHandler {
	return &SwitchingHandler{handler: handler}
}

func (handler *SwitchingHandler) Swap(next http.Handler) {
	handler.mu.Lock()
	handler.handler = next
	handler.mu.Unlock()
}

func (handler *SwitchingHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	handler.mu.RLock()
	current := handler.handler
	handler.mu.RUnlock()
	if current == nil {
		http.Error(writer, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	current.ServeHTTP(writer, request)
}

func SetupOnly(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if setupOnlyPath(request) {
			handler.ServeHTTP(writer, request)
			return
		}
		writeError(writer, request, http.StatusNotFound, "not_found", "the requested resource was not found")
	})
}

func setupOnlyPath(request *http.Request) bool {
	switch request.URL.Path {
	case "/api/v1/health/live", "/api/v1/health/ready", "/api/v1/setup/status":
		return request.Method == http.MethodGet
	case "/api/v1/setup/initialize":
		return request.Method == http.MethodPost
	default:
		return false
	}
}
