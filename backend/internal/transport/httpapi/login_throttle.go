package httpapi

import (
	"container/list"
	"context"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// 登录失败限制参数。按来源的连续失败会触发指数退避；全局失败上限约束
// 分布式来源的总体尝试速率；并发槽位约束 argon2 校验的峰值内存。
const (
	loginSourceFailureLimit  = 5
	loginSourceFailureWindow = 10 * time.Minute
	loginSourceBaseLockout   = 30 * time.Second
	loginSourceMaxLockout    = 15 * time.Minute

	loginGlobalFailureLimit  = 30
	loginGlobalFailureWindow = 10 * time.Minute
	loginGlobalLockout       = time.Minute

	loginVerificationSlots = 2
	loginVerificationWait  = 2 * time.Second

	// loginTrackedSources 约束按来源记账的内存上限，超出后淘汰最久未使用的来源。
	loginTrackedSources = 1024
)

// loginAttemptState 记录一个来源或全局窗口内的连续失败与锁定截止时间。
type loginAttemptState struct {
	key        string
	failures   int
	windowFrom time.Time
	lockedTill time.Time
}

// loginLimiter 以有界内存记录登录失败并按来源退避，同时限制校验并发。
// 状态只保存在进程内，重启后重置；这是有意的取舍，避免攻击者写入持久存储。
type loginLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	sources  map[string]*list.Element
	order    *list.List
	global   loginAttemptState
	slots    chan struct{}
	waitTime time.Duration
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	if now == nil {
		now = time.Now
	}
	return &loginLimiter{
		now:      now,
		sources:  make(map[string]*list.Element),
		order:    list.New(),
		slots:    make(chan struct{}, loginVerificationSlots),
		waitTime: loginVerificationWait,
	}
}

// allow 返回需要等待的重试间隔；ok 为 false 时表示当前来源或全局仍在退避期内。
func (limiter *loginLimiter) allow(source string) (retryAfter time.Duration, ok bool) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	retryAfter = 0
	if now.Before(limiter.global.lockedTill) {
		retryAfter = limiter.global.lockedTill.Sub(now)
	}
	if state := limiter.sourceState(source); state != nil && now.Before(state.lockedTill) {
		if wait := state.lockedTill.Sub(now); wait > retryAfter {
			retryAfter = wait
		}
	}
	return retryAfter, retryAfter == 0
}

// recordFailure 记一次凭据校验失败，并按窗口与阈值更新锁定时间。
func (limiter *loginLimiter) recordFailure(source string) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()

	state := limiter.trackSource(source, now)
	registerLoginFailure(state, now, loginSourceFailureLimit, loginSourceFailureWindow, loginSourceBaseLockout, loginSourceMaxLockout)

	registerLoginFailure(&limiter.global, now, loginGlobalFailureLimit, loginGlobalFailureWindow, loginGlobalLockout, loginGlobalLockout)
}

// recordSuccess 清除该来源的连续失败记录。全局失败计数保留，避免用成功登录
// 重置对分布式攻击的总体约束。
func (limiter *loginLimiter) recordSuccess(source string) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	element, ok := limiter.sources[source]
	if !ok {
		return
	}
	limiter.order.Remove(element)
	delete(limiter.sources, source)
}

// acquire 获取一个校验槽位；等待超过 waitTime 时返回 false。
func (limiter *loginLimiter) acquire(ctx context.Context) (release func(), ok bool) {
	release = func() { <-limiter.slots }
	select {
	case limiter.slots <- struct{}{}:
		return release, true
	case <-ctx.Done():
		return nil, false
	default:
	}
	timer := time.NewTimer(limiter.waitTime)
	defer timer.Stop()
	select {
	case limiter.slots <- struct{}{}:
		return release, true
	case <-ctx.Done():
		return nil, false
	case <-timer.C:
		return nil, false
	}
}

func (limiter *loginLimiter) sourceState(source string) *loginAttemptState {
	element, ok := limiter.sources[source]
	if !ok {
		return nil
	}
	return element.Value.(*loginAttemptState)
}

// trackSource 返回来源状态，并把来源刷新为最近使用；内存满时淘汰最久未使用的来源。
func (limiter *loginLimiter) trackSource(source string, now time.Time) *loginAttemptState {
	if element, ok := limiter.sources[source]; ok {
		limiter.order.MoveToFront(element)
		return element.Value.(*loginAttemptState)
	}
	for limiter.order.Len() >= loginTrackedSources {
		oldest := limiter.order.Back()
		if oldest == nil {
			break
		}
		limiter.order.Remove(oldest)
		delete(limiter.sources, oldest.Value.(*loginAttemptState).key)
	}
	state := &loginAttemptState{key: source, windowFrom: now}
	limiter.sources[source] = limiter.order.PushFront(state)
	return state
}

func registerLoginFailure(
	state *loginAttemptState,
	now time.Time,
	limit int,
	window time.Duration,
	baseLockout time.Duration,
	maxLockout time.Duration,
) {
	if now.Sub(state.windowFrom) > window {
		state.failures = 0
		state.windowFrom = now
	}
	state.failures++
	if state.failures < limit {
		return
	}
	backoff := baseLockout
	for step := 0; step < state.failures-limit && backoff < maxLockout; step++ {
		backoff *= 2
	}
	if backoff > maxLockout {
		backoff = maxLockout
	}
	state.lockedTill = now.Add(backoff)
}

// loginAttemptLimit 对登录请求应用失败退避与校验并发上限。
// 被拒绝时返回与账号是否存在无关的固定响应，不触发凭据校验与 argon2 计算。
func loginAttemptLimit(limiter *loginLimiter, trustedProxies []*net.IPNet, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isLoginAttempt(r) {
				next.ServeHTTP(w, r)
				return
			}

			source := clientAddress(r, trustedProxies)
			if retryAfter, ok := limiter.allow(source); !ok {
				writeLoginThrottled(w, r, retryAfter)
				logLoginThrottled(logger, r, retryAfter)
				return
			}
			release, ok := limiter.acquire(r.Context())
			if !ok {
				writeLoginThrottled(w, r, loginVerificationWait)
				logLoginThrottled(logger, r, loginVerificationWait)
				return
			}
			defer release()

			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)
			switch recorder.status {
			case http.StatusUnauthorized:
				limiter.recordFailure(source)
				logLoginFailure(logger, r)
			case http.StatusOK:
				limiter.recordSuccess(source)
			}
		})
	}
}

func isLoginAttempt(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login"
}

func writeLoginThrottled(w http.ResponseWriter, r *http.Request, retryAfter time.Duration) {
	// 向上取整，保证客户端至少等待剩余的退避时间。
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many login attempts, retry later")
}

func logLoginFailure(logger *slog.Logger, r *http.Request) {
	// 审计条目只包含事件、结果与请求 ID；不记录密码、用户名与来源地址。
	logger.Warn("administrator login failed",
		"event", "auth.login_failed",
		"request_id", middleware.GetReqID(r.Context()),
		"reason", "invalid_credentials",
	)
}

func logLoginThrottled(logger *slog.Logger, r *http.Request, retryAfter time.Duration) {
	logger.Warn("administrator login throttled",
		"event", "auth.login_throttled",
		"request_id", middleware.GetReqID(r.Context()),
		"retry_after_seconds", int(retryAfter.Seconds()),
	)
}

// statusRecorder 只记录首次写出的状态码，不缓冲响应体，保持流式写入行为。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (recorder *statusRecorder) WriteHeader(status int) {
	if !recorder.wroteHeader {
		recorder.status = status
		recorder.wroteHeader = true
	}
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Write(payload []byte) (int, error) {
	if !recorder.wroteHeader {
		recorder.wroteHeader = true
	}
	return recorder.ResponseWriter.Write(payload)
}

// clientAddress 返回用于限流的来源标识。只有 TCP 对端位于信任网段时才读取
// X-Forwarded-For，并从右向左跳过受信代理，避免采信客户端自行伪造的左侧条目。
func clientAddress(r *http.Request, trustedProxies []*net.IPNet) string {
	peer := remoteIP(r.RemoteAddr)
	if !ipInNetworks(peer, trustedProxies) {
		return remoteAddressKey(r.RemoteAddr, peer)
	}
	forwarded := r.Header.Values("X-Forwarded-For")
	entries := make([]string, 0, 4)
	for _, header := range forwarded {
		for _, entry := range strings.Split(header, ",") {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				entries = append(entries, trimmed)
			}
		}
	}
	for index := len(entries) - 1; index >= 0; index-- {
		candidate := net.ParseIP(entries[index])
		if candidate == nil || candidate.IsUnspecified() {
			continue
		}
		if ipInNetworks(candidate, trustedProxies) {
			continue
		}
		return candidate.String()
	}
	return remoteAddressKey(r.RemoteAddr, peer)
}

func remoteAddressKey(raw string, ip net.IP) string {
	if ip != nil {
		return ip.String()
	}
	if raw == "" {
		return "unknown"
	}
	return raw
}

func remoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return net.ParseIP(strings.TrimSpace(host))
}

func ipInNetworks(ip net.IP, networks []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, network := range networks {
		if network != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}
