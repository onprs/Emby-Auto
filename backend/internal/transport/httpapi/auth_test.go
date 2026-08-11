package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type authenticationStub struct {
	loginResult        service.LoginResult
	loginErr           error
	authenticated      domain.Session
	authenticateErr    error
	authenticatedToken string
	logoutToken        string
}

func (stub *authenticationStub) Login(context.Context, string, string) (service.LoginResult, error) {
	return stub.loginResult, stub.loginErr
}
func (stub *authenticationStub) Authenticate(_ context.Context, token string) (domain.Session, error) {
	stub.authenticatedToken = token
	return stub.authenticated, stub.authenticateErr
}
func (stub *authenticationStub) Logout(_ context.Context, token string) error {
	stub.logoutToken = token
	return nil
}

func TestLoginSetsHttpOnlySessionCookie(t *testing.T) {
	expiresAt := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	authentication := &authenticationStub{loginResult: service.LoginResult{
		Token: "raw-session-token",
		Session: domain.Session{
			User: domain.AdminUser{
				ID:       uuid.MustParse("10000000-0000-0000-0000-000000000001"),
				Username: "admin",
			},
			ExpiresAt: expiresAt,
		},
	}}
	server := NewServer(readinessStub{}, WithAuthentication(authentication, true))
	server.now = func() time.Time { return time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC) }
	handler := NewHandler(server)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"password123"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-Id", "login-request")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v, want one session cookie", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != sessionCookieName || cookie.Value != "raw-session-token" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/api/v1" {
		t.Fatalf("cookie = %#v, want secure HttpOnly strict session cookie", cookie)
	}
	if !cookie.Expires.Equal(expiresAt) || cookie.MaxAge != 86400 {
		t.Fatalf("cookie expiry = %v max-age=%d, want %v and 86400", cookie.Expires, cookie.MaxAge, expiresAt)
	}

	var body Session
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.User.Username != "admin" || body.User.Id.String() != "10000000-0000-0000-0000-000000000001" {
		t.Fatalf("session body = %#v, want authenticated admin", body)
	}
}

func TestLoginReturnsSameErrorForInvalidCredentials(t *testing.T) {
	authentication := &authenticationStub{loginErr: service.ErrInvalidCredentials}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"missing","password":"password123"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-Id", "invalid-login")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
	var body ApiError
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != "unauthorized" || body.Message != "invalid username or password" || body.RequestId != "invalid-login" {
		t.Fatalf("error = %#v, want stable credential error", body)
	}
}

func TestProtectedRoutesRequireValidSessionCookie(t *testing.T) {
	authentication := &authenticationStub{authenticateErr: service.ErrUnauthenticated}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false)))

	for _, test := range []struct {
		name      string
		addCookie bool
	}{
		{name: "missing cookie"},
		{name: "expired cookie", addCookie: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
			request.Header.Set("X-Request-Id", "protected-request")
			if test.addCookie {
				request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "expired-token"})
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.Code)
			}
			var body ApiError
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Code != "unauthorized" || body.RequestId != "protected-request" {
				t.Fatalf("error = %#v, want structured unauthorized", body)
			}
		})
	}
}

func TestSessionAndLogoutUseAuthenticatedCookie(t *testing.T) {
	session := domain.Session{
		User:      domain.AdminUser{ID: uuid.MustParse("10000000-0000-0000-0000-000000000002"), Username: "operator"},
		ExpiresAt: time.Date(2026, time.July, 23, 0, 0, 0, 0, time.UTC),
	}
	authentication := &authenticationStub{authenticated: session}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false)))

	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	sessionRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	sessionResponse := httptest.NewRecorder()
	handler.ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusOK || authentication.authenticatedToken != "valid-token" {
		t.Fatalf("session status/token = %d/%q, want 200/valid-token", sessionResponse.Code, authentication.authenticatedToken)
	}

	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusNoContent || authentication.logoutToken != "valid-token" {
		t.Fatalf("logout status/token = %d/%q, want 204/valid-token", logoutResponse.Code, authentication.logoutToken)
	}
	cookies := logoutResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge != -1 || !cookies[0].HttpOnly {
		t.Fatalf("logout cookies = %#v, want expired HttpOnly cookie", cookies)
	}
}
