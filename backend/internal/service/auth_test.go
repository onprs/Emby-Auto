package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

type authStoreStub struct {
	user             domain.AdminUser
	findUserErr      error
	createdTokenHash []byte
	createdExpiresAt time.Time
	session          domain.Session
	findSessionHash  []byte
	revokedHash      []byte
	touchedSessionID uuid.UUID
	userCount        int64
	createdUser      domain.AdminUser
}

func (store *authStoreStub) CountUsers(context.Context) (int64, error) { return store.userCount, nil }
func (store *authStoreStub) CreateUser(_ context.Context, user domain.AdminUser) error {
	store.createdUser = user
	return nil
}
func (store *authStoreStub) FindUserByUsername(context.Context, string) (domain.AdminUser, error) {
	return store.user, store.findUserErr
}
func (store *authStoreStub) CreateSession(_ context.Context, _ uuid.UUID, _ uuid.UUID, tokenHash []byte, expiresAt time.Time) error {
	store.createdTokenHash = append([]byte(nil), tokenHash...)
	store.createdExpiresAt = expiresAt
	return nil
}
func (store *authStoreStub) FindSessionByTokenHash(_ context.Context, tokenHash []byte) (domain.Session, error) {
	store.findSessionHash = append([]byte(nil), tokenHash...)
	return store.session, nil
}
func (store *authStoreStub) TouchSession(_ context.Context, sessionID uuid.UUID) error {
	store.touchedSessionID = sessionID
	return nil
}
func (store *authStoreStub) RevokeSession(_ context.Context, tokenHash []byte) error {
	store.revokedHash = append([]byte(nil), tokenHash...)
	return nil
}

type passwordStub struct {
	valid      bool
	verifyHash string
	hashValue  string
}

func (stub *passwordStub) Hash(string) (string, error) { return stub.hashValue, nil }
func (stub *passwordStub) Verify(_ string, encoded string) (bool, error) {
	stub.verifyHash = encoded
	return stub.valid, nil
}

func TestAuthenticationLoginStoresOnlyTokenHash(t *testing.T) {
	userID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	store := &authStoreStub{user: domain.AdminUser{ID: userID, Username: "admin", PasswordHash: "encoded-password"}}
	passwords := &passwordStub{valid: true}
	authentication := NewAuthentication(store, passwords, 12*time.Hour)
	authentication.now = func() time.Time { return time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC) }
	authentication.random = bytes.NewReader(bytes.Repeat([]byte{0x2a}, 32))

	result, err := authentication.Login(context.Background(), " admin ", "password123")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if result.Token != "KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio" {
		t.Fatalf("token = %q, want fixed base64url token", result.Token)
	}
	wantHash := sha256.Sum256([]byte(result.Token))
	if !bytes.Equal(store.createdTokenHash, wantHash[:]) {
		t.Fatalf("stored token hash = %x, want sha256(raw token)", store.createdTokenHash)
	}
	if bytes.Contains(store.createdTokenHash, []byte(result.Token)) {
		t.Fatal("session store received the raw token")
	}
	wantExpiry := time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC)
	if !store.createdExpiresAt.Equal(wantExpiry) || !result.Session.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("expiry = %v / %v, want %v", store.createdExpiresAt, result.Session.ExpiresAt, wantExpiry)
	}
	if passwords.verifyHash != "encoded-password" {
		t.Fatalf("verified hash = %q, want stored password hash", passwords.verifyHash)
	}
}

func TestAuthenticationDoesNotRevealUnknownUser(t *testing.T) {
	store := &authStoreStub{findUserErr: domain.ErrNotFound}
	passwords := &passwordStub{}
	authentication := NewAuthentication(store, passwords, time.Hour)

	_, err := authentication.Login(context.Background(), "missing", "password123")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
	}
	if passwords.verifyHash != dummyPasswordHash {
		t.Fatalf("verified hash = %q, want dummy hash", passwords.verifyHash)
	}
}

func TestAuthenticationAuthenticateAndLogoutHashCookieToken(t *testing.T) {
	sessionID := uuid.MustParse("10000000-0000-0000-0000-000000000002")
	store := &authStoreStub{session: domain.Session{ID: sessionID}}
	authentication := NewAuthentication(store, &passwordStub{}, time.Hour)

	if _, err := authentication.Authenticate(context.Background(), "cookie-token"); err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if err := authentication.Logout(context.Background(), "cookie-token"); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	wantHash := sha256.Sum256([]byte("cookie-token"))
	if !bytes.Equal(store.findSessionHash, wantHash[:]) || !bytes.Equal(store.revokedHash, wantHash[:]) {
		t.Fatalf("session hashes = %x / %x, want %x", store.findSessionHash, store.revokedHash, wantHash)
	}
	if store.touchedSessionID != sessionID {
		t.Fatalf("touched session = %s, want %s", store.touchedSessionID, sessionID)
	}
}

func TestBootstrapAdminCreatesOnlyWhenDatabaseIsEmpty(t *testing.T) {
	store := &authStoreStub{}
	passwords := &passwordStub{hashValue: "argon2id-hash"}
	authentication := NewAuthentication(store, passwords, time.Hour)

	if err := authentication.BootstrapAdmin(context.Background(), "admin", "password123"); err != nil {
		t.Fatalf("BootstrapAdmin() error = %v", err)
	}
	if store.createdUser.Username != "admin" || store.createdUser.PasswordHash != "argon2id-hash" || store.createdUser.ID == uuid.Nil {
		t.Fatalf("created user = %#v, want initialized administrator", store.createdUser)
	}

	store.userCount = 1
	store.createdUser = domain.AdminUser{}
	if err := authentication.BootstrapAdmin(context.Background(), "replacement", "password123"); err != nil {
		t.Fatalf("BootstrapAdmin(existing) error = %v", err)
	}
	if store.createdUser.ID != uuid.Nil {
		t.Fatalf("created replacement user = %#v, want none", store.createdUser)
	}
}
