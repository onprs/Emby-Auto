package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("resource not found")

type AdminUser struct {
	ID           uuid.UUID
	Username     string
	PasswordHash string
	Disabled     bool
}

type Session struct {
	ID        uuid.UUID
	User      AdminUser
	ExpiresAt time.Time
}
