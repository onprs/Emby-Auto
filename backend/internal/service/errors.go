package service

import "errors"

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthenticated    = errors.New("authentication required")
	ErrStateConflict      = errors.New("resource state conflict")
	ErrInvalidInput       = errors.New("invalid input")
	ErrUnavailable        = errors.New("service unavailable")
)

// Error carries a stable API-safe code and details while retaining an internal cause.
type Error struct {
	Code    string
	Message string
	Details map[string]any
	Cause   error
}

func (err *Error) Error() string {
	return err.Message
}

func (err *Error) Unwrap() error {
	return err.Cause
}

func NewError(code, message string, cause error, details map[string]any) *Error {
	if details == nil {
		details = map[string]any{}
	}
	return &Error{Code: code, Message: message, Details: details, Cause: cause}
}
