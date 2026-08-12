package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

const sessionCookieName = "emby_auto_session"

type authenticatedRequest struct {
	session domain.Session
	token   string
}

type authenticatedRequestKey struct{}

func (server *Server) Login(
	ctx context.Context,
	request LoginRequestObject,
) (LoginResponseObject, error) {
	if server.auth == nil {
		return Login503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "authentication")}, nil
	}
	if request.Body == nil {
		return Login400JSONResponse{BadRequestJSONResponse: badRequestError(ctx, "request body is required")}, nil
	}

	result, err := server.auth.Login(ctx, request.Body.Username, request.Body.Password)
	if errors.Is(err, service.ErrInvalidCredentials) {
		return Login401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "invalid username or password")}, nil
	}
	if err != nil {
		return Login503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}

	cookie := server.sessionCookie(result.Token, result.Session.ExpiresAt)
	cookieValue := cookie.String()
	return Login200JSONResponse{
		Body:    sessionResponse(result.Session),
		Headers: Login200ResponseHeaders{SetCookie: &cookieValue},
	}, nil
}

func (server *Server) Logout(
	ctx context.Context,
	_ LogoutRequestObject,
) (LogoutResponseObject, error) {
	authenticated, ok := authenticationFromContext(ctx)
	if !ok || server.auth == nil {
		return Logout401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	if err := server.auth.Logout(ctx, authenticated.token); err != nil {
		return Logout503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "postgresql")}, nil
	}

	cookie := server.expiredSessionCookie()
	cookieValue := cookie.String()
	return Logout204Response{Headers: Logout204ResponseHeaders{SetCookie: &cookieValue}}, nil
}

func (server *Server) GetSession(
	ctx context.Context,
	_ GetSessionRequestObject,
) (GetSessionResponseObject, error) {
	authenticated, ok := authenticationFromContext(ctx)
	if !ok {
		return GetSession401JSONResponse{UnauthorizedJSONResponse: unauthorizedError(ctx, "authentication is required")}, nil
	}
	return GetSession200JSONResponse(sessionResponse(authenticated.session)), nil
}

func (server *Server) sessionCookie(token string, expiresAt time.Time) *http.Cookie {
	maxAge := int(expiresAt.Sub(server.now().UTC()).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/api/v1",
		Expires:  expiresAt,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   server.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	}
}

func (server *Server) expiredSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Path:     "/api/v1",
		Expires:  time.Unix(1, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   server.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	}
}

func sessionResponse(session domain.Session) Session {
	return Session{
		User: AdminUser{
			Id:       session.User.ID,
			Username: session.User.Username,
		},
		ExpiresAt: session.ExpiresAt,
	}
}

func sessionAuthentication(authentication AuthenticationService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if isPublicRequest(request) {
				next.ServeHTTP(w, request)
				return
			}
			if authentication == nil {
				writeAPIError(w, http.StatusServiceUnavailable, ApiError(serviceUnavailableError(request.Context(), "authentication")))
				return
			}

			cookie, err := request.Cookie(sessionCookieName)
			if err != nil || cookie.Value == "" {
				writeAPIError(w, http.StatusUnauthorized, ApiError(unauthorizedError(request.Context(), "authentication is required")))
				return
			}
			session, err := authentication.Authenticate(request.Context(), cookie.Value)
			if errors.Is(err, service.ErrUnauthenticated) {
				writeAPIError(w, http.StatusUnauthorized, ApiError(unauthorizedError(request.Context(), "the session is invalid or expired")))
				return
			}
			if err != nil {
				writeAPIError(w, http.StatusServiceUnavailable, ApiError(serviceUnavailableError(request.Context(), "postgresql")))
				return
			}

			ctx := context.WithValue(request.Context(), authenticatedRequestKey{}, authenticatedRequest{
				session: session,
				token:   cookie.Value,
			})
			next.ServeHTTP(w, request.WithContext(ctx))
		})
	}
}

func authenticationFromContext(ctx context.Context) (authenticatedRequest, bool) {
	authenticated, ok := ctx.Value(authenticatedRequestKey{}).(authenticatedRequest)
	return authenticated, ok
}

func isPublicRequest(request *http.Request) bool {
	if request.Method == http.MethodPost && (request.URL.Path == "/api/v1/auth/login" || request.URL.Path == "/api/v1/setup/initialize") {
		return true
	}
	return request.Method == http.MethodGet && (request.URL.Path == "/api/v1/health/live" || request.URL.Path == "/api/v1/health/ready" || request.URL.Path == "/api/v1/setup/status")
}

func unauthorizedError(ctx context.Context, message string) UnauthorizedJSONResponse {
	return UnauthorizedJSONResponse(ApiError{
		Code:      "unauthorized",
		Message:   message,
		Details:   map[string]any{},
		RequestId: middleware.GetReqID(ctx),
	})
}
