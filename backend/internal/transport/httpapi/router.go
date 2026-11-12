package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const (
	configurationSecretsRevealPath   = "/api/v1/config/secrets/reveal"
	configurationSecretsCacheControl = "no-store, max-age=0"

	// maxRequestBodyBytes 限制单个 API 请求体。所有请求体都是 JSON，
	// 最大的合法载荷（运行配置含 4 个 4 KiB 密钥）也远小于该上限。
	maxRequestBodyBytes int64 = 1 << 20

	// requestIDBytes 决定服务端生成的请求 ID 长度（32 位十六进制）。
	requestIDBytes = 16
)

// clientRequestIDPattern 约束客户端可传入的 X-Request-Id：只允许 ASCII 字母、
// 数字与 ._-，长度 8-64；不匹配时由服务端重新生成，避免回显任意客户端内容。
var clientRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,63}$`)

// requestFieldError 表示请求体缺少契约声明为 required 的字段。
// 字段名只能来自代码内的静态清单，不会包含客户端原始输入。
type requestFieldError struct {
	field string
}

func (err *requestFieldError) Error() string {
	return "required request field is missing: " + err.field
}

// HandlerOption 调整 NewHandler 组装的通用 HTTP 行为。
type HandlerOption func(*handlerOptions)

type handlerOptions struct {
	logger         *slog.Logger
	trustedProxies []*net.IPNet
}

// WithHandlerLogger 注入处理器日志，用于记录被恢复的 panic 与登录失败审计事件。
func WithHandlerLogger(logger *slog.Logger) HandlerOption {
	return func(options *handlerOptions) {
		if logger != nil {
			options.logger = logger
		}
	}
}

// WithTrustedProxies 声明可代表客户端提供 X-Forwarded-For 的代理网段。
// 空列表表示不信任任何代理头，登录限流只使用 TCP 对端地址。
func WithTrustedProxies(networks []*net.IPNet) HandlerOption {
	return func(options *handlerOptions) {
		options.trustedProxies = networks
	}
}

// NewHandler wires generated routes to the strict server implementation.
func NewHandler(server StrictServerInterface, options ...HandlerOption) http.Handler {
	settings := handlerOptions{logger: slog.Default()}
	for _, option := range options {
		option(&settings)
	}

	strictHandler := NewStrictHandlerWithOptions(server, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeRequestError(w, r, err)
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		},
	})

	router := chi.NewRouter()
	router.Use(requestID)
	router.Use(configurationSecretsNoStore)
	router.Use(requestBodyLimit(maxRequestBodyBytes))
	router.Use(recoverer(settings.logger))
	router.Use(loginAttemptLimit(newLoginLimiter(time.Now), settings.trustedProxies, settings.logger))
	if concreteServer, ok := server.(*Server); ok && concreteServer.auth != nil {
		router.Use(sessionAuthentication(concreteServer.auth))
	}
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "the requested resource was not found")
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "the request method is not allowed")
	})

	return HandlerWithOptions(strictHandler, ChiServerOptions{
		BaseRouter: router,
		// 查询字符串与路径参数绑定失败也使用统一错误模型，不回显客户端输入与解析细节。
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "the request is invalid")
		},
	})
}

func configurationSecretsNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == configurationSecretsRevealPath {
			w.Header().Set("Cache-Control", configurationSecretsCacheControl)
		}
		next.ServeHTTP(w, r)
	})
}

// requestID 为每个请求生成不含主机名与进程信息的随机 ID。
// 合法的客户端 X-Request-Id 会被沿用，便于跨代理关联日志。
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		if candidate := r.Header.Get(middleware.RequestIDHeader); clientRequestIDPattern.MatchString(candidate) {
			id = candidate
		}
		w.Header().Set(middleware.RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), middleware.RequestIDKey, id)))
	})
}

func newRequestID() string {
	raw := make([]byte, requestIDBytes)
	if _, err := rand.Read(raw); err != nil {
		// 随机源不可用时保留唯一性，仍然不暴露主机信息。
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw)
}

// requestBodyLimit 在解码前限制请求体大小，避免未认证请求把整个请求体读入内存。
func requestBodyLimit(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				writeError(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "the request body exceeds the allowed size")
				return
			}
			if r.Body != nil {
				// 分块传输或声明长度不实的请求在解码阶段被截断为 413。
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				recovered := recover()
				if recovered == nil || recovered == http.ErrAbortHandler {
					return
				}
				// 只记录请求标识、方法与路径；请求头（Cookie、Authorization）与请求体不进日志。
				logger.Error("recovered HTTP handler panic",
					"request_id", middleware.GetReqID(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(recovered),
					"stack", string(debug.Stack()),
				)
				if wrapped.Status() == 0 {
					writeError(wrapped, r, http.StatusInternalServerError, "internal_error", "an internal error occurred")
				}
			}()
			next.ServeHTTP(wrapped, r)
		})
	}
}

// writeRequestError 把请求解码错误转换成统一错误模型。
func writeRequestError(w http.ResponseWriter, r *http.Request, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "the request body exceeds the allowed size")
		return
	}
	var fieldError *requestFieldError
	if errors.As(err, &fieldError) {
		writeAPIError(w, http.StatusBadRequest, ApiError{
			Code:      "invalid_request",
			Message:   "a required request field is missing",
			Details:   map[string]any{"field": fieldError.field},
			RequestId: middleware.GetReqID(r.Context()),
		})
		return
	}
	writeError(w, r, http.StatusBadRequest, "invalid_request", "the request is invalid")
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
