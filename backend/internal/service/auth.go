package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

const dummyPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$MDEyMzQ1Njc4OWFiY2RlZg$V/UM5TLb/3aE2ju6M32v0h9R2uT42Jx2BsCHQ1JNjXA"

type PasswordVerifier interface {
	Hash(string) (string, error)
	Verify(string, string) (bool, error)
}

type AuthStore interface {
	CountUsers(context.Context) (int64, error)
	CreateUser(context.Context, domain.AdminUser) error
	FindUserByUsername(context.Context, string) (domain.AdminUser, error)
	CreateSession(context.Context, uuid.UUID, uuid.UUID, []byte, time.Time) error
	FindSessionByTokenHash(context.Context, []byte) (domain.Session, error)
	TouchSession(context.Context, uuid.UUID) error
	RevokeSession(context.Context, []byte) error
}

type LoginResult struct {
	Session domain.Session
	Token   string
}

type Authentication struct {
	store      AuthStore
	passwords  PasswordVerifier
	sessionTTL time.Duration
	now        func() time.Time
	random     io.Reader
}

func NewAuthentication(store AuthStore, passwords PasswordVerifier, sessionTTL time.Duration) *Authentication {
	return &Authentication{
		store:      store,
		passwords:  passwords,
		sessionTTL: sessionTTL,
		now:        time.Now,
		random:     rand.Reader,
	}
}

func (authentication *Authentication) BootstrapAdmin(ctx context.Context, username, password string) error {
	if username == "" && password == "" {
		return nil
	}
	if strings.TrimSpace(username) == "" || len(password) < 8 {
		return fmt.Errorf("initial administrator requires a username and password of at least 8 characters")
	}

	count, err := authentication.store.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("check initial administrator: %w", err)
	}
	if count > 0 {
		return nil
	}

	passwordHash, err := authentication.passwords.Hash(password)
	if err != nil {
		return fmt.Errorf("hash initial administrator password: %w", err)
	}
	if err := authentication.store.CreateUser(ctx, domain.AdminUser{
		ID:           uuid.New(),
		Username:     strings.TrimSpace(username),
		PasswordHash: passwordHash,
	}); err != nil {
		return fmt.Errorf("bootstrap initial administrator: %w", err)
	}
	return nil
}

func (authentication *Authentication) Login(ctx context.Context, username, password string) (LoginResult, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 128 || len(password) < 8 || len(password) > 1024 {
		return LoginResult{}, ErrInvalidCredentials
	}

	user, err := authentication.store.FindUserByUsername(ctx, username)
	if errors.Is(err, domain.ErrNotFound) {
		_, _ = authentication.passwords.Verify(password, dummyPasswordHash)
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, fmt.Errorf("load administrator credentials: %w", err)
	}

	valid, err := authentication.passwords.Verify(password, user.PasswordHash)
	if err != nil {
		return LoginResult{}, fmt.Errorf("verify administrator credentials: %w", err)
	}
	if !valid || user.Disabled {
		return LoginResult{}, ErrInvalidCredentials
	}

	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(authentication.random, tokenBytes); err != nil {
		return LoginResult{}, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expiresAt := authentication.now().UTC().Add(authentication.sessionTTL)
	session := domain.Session{ID: uuid.New(), User: user, ExpiresAt: expiresAt}
	if err := authentication.store.CreateSession(ctx, session.ID, user.ID, hashSessionToken(token), expiresAt); err != nil {
		return LoginResult{}, fmt.Errorf("persist session: %w", err)
	}
	return LoginResult{Session: session, Token: token}, nil
}

func (authentication *Authentication) Authenticate(ctx context.Context, token string) (domain.Session, error) {
	if token == "" {
		return domain.Session{}, ErrUnauthenticated
	}
	session, err := authentication.store.FindSessionByTokenHash(ctx, hashSessionToken(token))
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Session{}, ErrUnauthenticated
	}
	if err != nil {
		return domain.Session{}, fmt.Errorf("authenticate session: %w", err)
	}
	if err := authentication.store.TouchSession(ctx, session.ID); err != nil {
		return domain.Session{}, fmt.Errorf("update session activity: %w", err)
	}
	return session, nil
}

func (authentication *Authentication) Logout(ctx context.Context, token string) error {
	if token == "" {
		return ErrUnauthenticated
	}
	if err := authentication.store.RevokeSession(ctx, hashSessionToken(token)); err != nil {
		return fmt.Errorf("logout session: %w", err)
	}
	return nil
}

func hashSessionToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}
