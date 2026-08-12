package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type authQueries interface {
	CountAdminUsers(context.Context) (int64, error)
	CreateAdminUser(context.Context, db.CreateAdminUserParams) (db.AdminUser, error)
	GetAdminUserByUsername(context.Context, string) (db.AdminUser, error)
	CreateSession(context.Context, db.CreateSessionParams) (db.Session, error)
	GetActiveSessionByTokenHash(context.Context, []byte) (db.GetActiveSessionByTokenHashRow, error)
	TouchSession(context.Context, pgtype.UUID) (int64, error)
	RevokeSessionByTokenHash(context.Context, []byte) (int64, error)
}

type Auth struct {
	queries authQueries
}

func NewAuth(queries authQueries) *Auth {
	return &Auth{queries: queries}
}

func (repository *Auth) CountUsers(ctx context.Context) (int64, error) {
	count, err := repository.queries.CountAdminUsers(ctx)
	if err != nil {
		return 0, fmt.Errorf("count administrator users: %w", err)
	}
	return count, nil
}

func (repository *Auth) CreateUser(ctx context.Context, user domain.AdminUser) error {
	_, err := repository.queries.CreateAdminUser(ctx, db.CreateAdminUserParams{
		ID:           UUIDToPG(user.ID),
		Username:     user.Username,
		PasswordHash: user.PasswordHash,
	})
	if err != nil {
		return fmt.Errorf("create administrator user: %w", err)
	}
	return nil
}

func (repository *Auth) FindUserByUsername(ctx context.Context, username string) (domain.AdminUser, error) {
	user, err := repository.queries.GetAdminUserByUsername(ctx, username)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AdminUser{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.AdminUser{}, fmt.Errorf("find administrator user: %w", err)
	}
	return domain.AdminUser{
		ID:           UUIDFromPG(user.ID),
		Username:     user.Username,
		PasswordHash: user.PasswordHash,
		Disabled:     user.Disabled,
	}, nil
}

func (repository *Auth) CreateSession(
	ctx context.Context,
	sessionID uuid.UUID,
	userID uuid.UUID,
	tokenHash []byte,
	expiresAt time.Time,
) error {
	_, err := repository.queries.CreateSession(ctx, db.CreateSessionParams{
		ID:          UUIDToPG(sessionID),
		AdminUserID: UUIDToPG(userID),
		TokenHash:   tokenHash,
		ExpiresAt:   pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (repository *Auth) FindSessionByTokenHash(ctx context.Context, tokenHash []byte) (domain.Session, error) {
	row, err := repository.queries.GetActiveSessionByTokenHash(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Session{}, fmt.Errorf("find active session: %w", err)
	}
	return domain.Session{
		ID: UUIDFromPG(row.SessionID),
		User: domain.AdminUser{
			ID:       UUIDFromPG(row.AdminUserID),
			Username: row.Username,
		},
		ExpiresAt: row.ExpiresAt.Time,
	}, nil
}

func (repository *Auth) TouchSession(ctx context.Context, sessionID uuid.UUID) error {
	if _, err := repository.queries.TouchSession(ctx, UUIDToPG(sessionID)); err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

func (repository *Auth) RevokeSession(ctx context.Context, tokenHash []byte) error {
	if _, err := repository.queries.RevokeSessionByTokenHash(ctx, tokenHash); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func UUIDToPG(value uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte(value), Valid: true}
}

func UUIDFromPG(value pgtype.UUID) uuid.UUID {
	if !value.Valid {
		return uuid.Nil
	}
	return uuid.UUID(value.Bytes)
}
