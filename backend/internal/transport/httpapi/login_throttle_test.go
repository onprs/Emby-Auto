package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/service"
)

const validLoginBody = `{"username":"admin","password":"fixture-password"}`

func loginRequest(t *testing.T, remoteAddr string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(validLoginBody))
	request.Header.Set("Content-Type", "application/json")
	if remoteAddr != "" {
		request.RemoteAddr = remoteAddr
	}
	return request
}

func decodeAPIError(t *testing.T, response *httptest.ResponseRecorder) ApiError {
	t.Helper()
	var apiError ApiError
	if err := json.Unmarshal(response.Body.Bytes(), &apiError); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return apiError
}

func TestLoginThrottleBlocksRepeatedFailures(t *testing.T) {
	authentication := &authenticationStub{loginErr: service.ErrInvalidCredentials}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false)), WithHandlerLogger(testLogger()))

	for attempt := 1; attempt <= loginSourceFailureLimit; attempt++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, loginRequest(t, "192.0.2.10:4000"))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", attempt, response.Code)
		}
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, loginRequest(t, "192.0.2.10:4000"))
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("throttled status = %d, want 429, body = %s", response.Code, response.Body.String())
	}
	if retryAfter := response.Header().Get("Retry-After"); retryAfter != "30" {
		t.Fatalf("Retry-After = %q, want 30", retryAfter)
	}
	apiError := decodeAPIError(t, response)
	if apiError.Code != "rate_limited" {
		t.Fatalf("error code = %q, want rate_limited", apiError.Code)
	}
	if authentication.loginCalls != loginSourceFailureLimit {
		t.Fatalf("credential verifications = %d, want %d (throttled requests must not verify)", authentication.loginCalls, loginSourceFailureLimit)
	}
}

func TestLoginThrottleIsUniformForUnknownAndExistingAccounts(t *testing.T) {
	authentication := &authenticationStub{loginErr: service.ErrInvalidCredentials}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false)), WithHandlerLogger(testLogger()))

	bodies := map[string]string{}
	for _, username := range []string{"admin", "does-not-exist"} {
		for attempt := 0; attempt < loginSourceFailureLimit; attempt++ {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"`+username+`","password":"fixture-password"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Request-Id", "uniform-login-1")
			request.RemoteAddr = "192.0.2.11:4000"
			handler.ServeHTTP(httptest.NewRecorder(), request)
		}
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"`+username+`","password":"fixture-password"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Request-Id", "uniform-login-1")
		request.RemoteAddr = "192.0.2.11:4000"
		handler.ServeHTTP(response, request)
		bodies[username] = response.Body.String()
	}

	if bodies["admin"] != bodies["does-not-exist"] {
		t.Fatalf("throttled bodies differ by account: %q vs %q", bodies["admin"], bodies["does-not-exist"])
	}
}

func TestLoginThrottleResetsAfterSuccessfulLogin(t *testing.T) {
	authentication := &authenticationStub{loginErr: service.ErrInvalidCredentials}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false)), WithHandlerLogger(testLogger()))
	remoteAddr := "192.0.2.12:4000"

	for attempt := 0; attempt < loginSourceFailureLimit-1; attempt++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, loginRequest(t, remoteAddr))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", attempt, response.Code)
		}
	}

	authentication.loginErr = nil
	authentication.loginResult = service.LoginResult{Token: "fixture-token"}
	success := httptest.NewRecorder()
	handler.ServeHTTP(success, loginRequest(t, remoteAddr))
	if success.Code != http.StatusOK {
		t.Fatalf("successful login status = %d, want 200, body = %s", success.Code, success.Body.String())
	}

	authentication.loginErr = service.ErrInvalidCredentials
	for attempt := 0; attempt < loginSourceFailureLimit-1; attempt++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, loginRequest(t, remoteAddr))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("post-success attempt %d status = %d, want 401 (counter must reset)", attempt, response.Code)
		}
	}
}

func TestLoginThrottleIgnoresCredentialStoreFailures(t *testing.T) {
	authentication := &authenticationStub{loginErr: errors.New("postgresql unavailable")}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false)), WithHandlerLogger(testLogger()))

	for attempt := 0; attempt <= loginSourceFailureLimit; attempt++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, loginRequest(t, "192.0.2.13:4000"))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("attempt %d status = %d, want 503", attempt, response.Code)
		}
	}
}

func TestLoginThrottleLogsFailureWithoutRequestSecrets(t *testing.T) {
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	limiter := newLoginLimiter(time.Now)
	handler := requestID(loginAttemptLimit(limiter, nil, logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})))
	request := loginRequest(t, "192.0.2.14:4000")
	request.Header.Set("X-Request-Id", "login-audit-1")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	entry := logged.String()
	for _, want := range []string{`"event":"auth.login_failed"`, `"request_id":"login-audit-1"`, `"reason":"invalid_credentials"`} {
		if !strings.Contains(entry, want) {
			t.Fatalf("audit entry %s does not contain %s", entry, want)
		}
	}
	for _, secret := range []string{"fixture-password", "192.0.2.14"} {
		if strings.Contains(entry, secret) {
			t.Fatalf("audit entry leaks request data %q: %s", secret, entry)
		}
	}
}

func TestLoginLimiterBackoffGrowsWithFailures(t *testing.T) {
	current := time.Date(2026, time.November, 12, 10, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter(func() time.Time { return current })
	source := "192.0.2.20"

	for attempt := 0; attempt < loginSourceFailureLimit; attempt++ {
		limiter.recordFailure(source)
	}
	if _, ok := limiter.allow(source); ok {
		t.Fatal("allow() = true after reaching failure limit, want locked")
	}

	current = current.Add(loginSourceBaseLockout + time.Second)
	if _, ok := limiter.allow(source); !ok {
		t.Fatal("allow() = false after first lockout expired")
	}
	limiter.recordFailure(source)
	if _, ok := limiter.allow(source); ok {
		t.Fatal("allow() = true after repeated failure, want longer lockout")
	}
	retryAfter, _ := limiter.allow(source)
	if retryAfter <= loginSourceBaseLockout {
		t.Fatalf("retry after = %s, want longer than %s", retryAfter, loginSourceBaseLockout)
	}
}

func TestLoginLimiterGlobalBudgetLocksDistributedSources(t *testing.T) {
	current := time.Date(2026, time.November, 12, 10, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter(func() time.Time { return current })
	for index := 0; index < loginGlobalFailureLimit; index++ {
		limiter.recordFailure("198.51.100." + strconv.Itoa(index%250+1))
	}
	if _, ok := limiter.allow("203.0.113.200"); ok {
		t.Fatal("allow() = true for untouched source after global budget exhausted")
	}
	current = current.Add(loginGlobalLockout + time.Second)
	if _, ok := limiter.allow("203.0.113.200"); !ok {
		t.Fatal("allow() = false for untouched source after global lockout expired")
	}
}

func TestLoginLimiterMemoryIsBounded(t *testing.T) {
	current := time.Date(2026, time.November, 12, 10, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter(func() time.Time { return current })
	for index := 0; index < loginTrackedSources*3; index++ {
		limiter.recordFailure("source-" + strconv.Itoa(index))
	}
	if len(limiter.sources) > loginTrackedSources {
		t.Fatalf("tracked sources = %d, want <= %d", len(limiter.sources), loginTrackedSources)
	}
	if limiter.order.Len() != len(limiter.sources) {
		t.Fatalf("tracked lists = %d/%d, want equal length", limiter.order.Len(), len(limiter.sources))
	}
}

func TestLoginLimiterVerificationSlotsAreBounded(t *testing.T) {
	limiter := newLoginLimiter(time.Now)
	limiter.waitTime = 20 * time.Millisecond
	first, ok := limiter.acquire(context.Background())
	if !ok {
		t.Fatal("first acquire failed")
	}
	second, ok := limiter.acquire(context.Background())
	if !ok {
		first()
		t.Fatal("second acquire failed")
	}
	if _, ok := limiter.acquire(context.Background()); ok {
		t.Fatal("third acquire succeeded, want bounded concurrency")
	}
	first()
	third, ok := limiter.acquire(context.Background())
	if !ok {
		t.Fatal("acquire after release failed")
	}
	third()
	second()
}

func TestLoginThrottleBoundsConcurrentVerifications(t *testing.T) {
	limiter := newLoginLimiter(time.Now)
	limiter.waitTime = 50 * time.Millisecond
	var inFlight int32
	var maxInFlight int32
	release := make(chan struct{})
	handler := loginAttemptLimit(limiter, nil, testLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := atomic.AddInt32(&inFlight, 1)
		for {
			observed := atomic.LoadInt32(&maxInFlight)
			if observed >= current || atomic.CompareAndSwapInt32(&maxInFlight, observed, current) {
				break
			}
		}
		<-release
		atomic.AddInt32(&inFlight, -1)
		w.WriteHeader(http.StatusUnauthorized)
	}))

	var wait sync.WaitGroup
	for index := 0; index < loginVerificationSlots+3; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			request := loginRequest(t, "198.51.100."+strconv.Itoa(index+1)+":4000")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized && response.Code != http.StatusTooManyRequests {
				t.Errorf("status = %d, want 401 or 429", response.Code)
			}
		}(index)
	}

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&inFlight) < loginVerificationSlots && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := atomic.LoadInt32(&inFlight); got > int32(loginVerificationSlots) {
		t.Fatalf("in-flight verifications = %d, want <= %d", got, loginVerificationSlots)
	}
	close(release)
	wait.Wait()

	if got := atomic.LoadInt32(&maxInFlight); got > int32(loginVerificationSlots) {
		t.Fatalf("max in-flight verifications = %d, want <= %d", got, loginVerificationSlots)
	}
	if got := atomic.LoadInt32(&inFlight); got != 0 {
		t.Fatalf("in-flight verifications after release = %d, want 0", got)
	}
}

func TestClientAddressHonorsOnlyTrustedProxyHeaders(t *testing.T) {
	loopback := mustCIDR(t, "127.0.0.0/8")
	proxy := mustCIDR(t, "10.0.0.0/8")
	for _, test := range []struct {
		name       string
		remoteAddr string
		forwarded  string
		trusted    []*net.IPNet
		want       string
	}{
		{name: "no trusted proxies ignores forwarded header", remoteAddr: "203.0.113.5:5000", forwarded: "198.51.100.1", want: "203.0.113.5"},
		{name: "trusted peer uses rightmost forwarded entry", remoteAddr: "127.0.0.1:5000", forwarded: "198.51.100.9, 203.0.113.44", trusted: []*net.IPNet{loopback}, want: "203.0.113.44"},
		{name: "spoofed left entries are ignored", remoteAddr: "127.0.0.1:5000", forwarded: "203.0.113.99, 198.51.100.9", trusted: []*net.IPNet{loopback}, want: "198.51.100.9"},
		{name: "chained trusted proxies are skipped", remoteAddr: "127.0.0.1:5000", forwarded: "203.0.113.44, 10.1.2.3", trusted: []*net.IPNet{loopback, proxy}, want: "203.0.113.44"},
		{name: "invalid entries are skipped", remoteAddr: "127.0.0.1:5000", forwarded: "not-an-ip, 203.0.113.44", trusted: []*net.IPNet{loopback}, want: "203.0.113.44"},
		{name: "untrusted peer keeps forwarded header ignored", remoteAddr: "203.0.113.5:5000", forwarded: "198.51.100.1, 198.51.100.2", trusted: []*net.IPNet{loopback}, want: "203.0.113.5"},
		{name: "no forwarded header falls back to peer", remoteAddr: "127.0.0.1:5000", trusted: []*net.IPNet{loopback}, want: "127.0.0.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := loginRequest(t, test.remoteAddr)
			if test.forwarded != "" {
				request.Header.Set("X-Forwarded-For", test.forwarded)
			}
			if got := clientAddress(request, test.trusted); got != test.want {
				t.Fatalf("clientAddress() = %q, want %q", got, test.want)
			}
		})
	}
}

func mustCIDR(t *testing.T, value string) *net.IPNet {
	t.Helper()
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		t.Fatalf("ParseCIDR(%q) error = %v", value, err)
	}
	return network
}
