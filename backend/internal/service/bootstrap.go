package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	platformconfig "github.com/onprs/emby-auto/backend/internal/platform/config"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

const setupAdvisoryLock int64 = 0x454d42594155544f

type BootstrapConfigurationStore interface {
	Load() (platformconfig.BootstrapData, bool, error)
	WriteConfigured(platformconfig.BootstrapData) error
	MarkCompleted() error
}

type BootstrapMigrator interface {
	Migrate(context.Context, string) error
}

type RuntimeActivator func(context.Context, domain.RuntimeBootstrap) error

type BootstrapOptions struct {
	DatabaseURL               string
	ConfigEncryptionKey       []byte
	DatabaseManagedExternally bool
	Store                     BootstrapConfigurationStore
	Migrator                  BootstrapMigrator
	Passwords                 PasswordVerifier
	Activate                  RuntimeActivator
}

type Bootstrap struct {
	databaseURL               string
	configEncryptionKey       []byte
	databaseManagedExternally bool
	store                     BootstrapConfigurationStore
	migrator                  BootstrapMigrator
	passwords                 PasswordVerifier
	activate                  RuntimeActivator
	random                    io.Reader

	mu           sync.Mutex
	initializing bool
}

func NewBootstrap(options BootstrapOptions) *Bootstrap {
	return &Bootstrap{
		databaseURL:               options.DatabaseURL,
		configEncryptionKey:       append([]byte(nil), options.ConfigEncryptionKey...),
		databaseManagedExternally: options.DatabaseManagedExternally,
		store:                     options.Store,
		migrator:                  options.Migrator,
		passwords:                 options.Passwords,
		activate:                  options.Activate,
		random:                    rand.Reader,
	}
}

func (bootstrap *Bootstrap) Status(ctx context.Context) (domain.SetupStatus, error) {
	bootstrap.mu.Lock()
	initializing := bootstrap.initializing
	bootstrap.mu.Unlock()
	data, completed, err := bootstrap.loadStored()
	if err != nil {
		return domain.SetupStatus{}, setupUnavailable("bootstrap_file", err)
	}
	databaseURL := bootstrap.effectiveDatabaseURL(data)
	status := domain.SetupStatus{
		State:                     domain.SetupRequired,
		DatabaseConfigured:        databaseURL != "",
		DatabaseManagedExternally: bootstrap.databaseManagedExternally,
	}
	if initializing {
		status.State = domain.SetupInitializing
		return status, nil
	}
	if completed {
		status.State = domain.SetupCompleted
		status.AdministratorConfigured = true
		if databaseURL == "" {
			return domain.SetupStatus{}, setupUnavailable("postgresql", errors.New("completed installation has no database configuration"))
		}
		runtimeKey, _, keyErr := bootstrap.resolveEncryptionKey(data)
		if keyErr != nil {
			return domain.SetupStatus{}, setupUnavailable("bootstrap", keyErr)
		}
		if bootstrap.activate != nil {
			if err := bootstrap.activate(ctx, domain.RuntimeBootstrap{DatabaseURL: databaseURL, ConfigEncryptionKey: runtimeKey, AdminID: data.AdminID}); err != nil {
				return domain.SetupStatus{}, setupUnavailable("runtime", err)
			}
		}
		return status, nil
	}
	if databaseURL == "" {
		return status, nil
	}
	administratorConfigured, installation, err := bootstrap.databaseInstallationState(ctx, databaseURL)
	if err != nil {
		return domain.SetupStatus{}, setupUnavailable("postgresql", err)
	}
	status.AdministratorConfigured = administratorConfigured
	if administratorConfigured {
		status.State = domain.SetupCompleted
		if data.AdminID != uuid.Nil && installation != uuid.Nil && installation == data.AdminID {
			if err := bootstrap.store.MarkCompleted(); err != nil {
				return domain.SetupStatus{}, setupUnavailable("bootstrap_file", err)
			}
		}
	}
	return status, nil
}

func (bootstrap *Bootstrap) Initialize(
	ctx context.Context,
	input domain.InitializeSetup,
) (domain.SetupStatus, error) {
	bootstrap.mu.Lock()
	if bootstrap.initializing {
		bootstrap.mu.Unlock()
		return domain.SetupStatus{}, NewError("setup_in_progress", "installation is already in progress", ErrStateConflict, nil)
	}
	bootstrap.initializing = true
	bootstrap.mu.Unlock()
	defer func() {
		bootstrap.mu.Lock()
		bootstrap.initializing = false
		bootstrap.mu.Unlock()
	}()

	data, completed, err := bootstrap.loadStored()
	if err != nil {
		return domain.SetupStatus{}, setupUnavailable("bootstrap_file", err)
	}
	if completed {
		return domain.SetupStatus{}, setupAlreadyCompleted()
	}
	databaseURL, err := bootstrap.resolveInitializationDatabase(input.Database, data)
	if err != nil {
		return domain.SetupStatus{}, err
	}
	username := strings.TrimSpace(input.AdministratorUsername)
	if username == "" || len(username) > 128 {
		return domain.SetupStatus{}, invalidSetup("administrator.username", "must contain between 1 and 128 characters")
	}
	if len(input.AdministratorPassword) < 8 || len(input.AdministratorPassword) > 1024 {
		return domain.SetupStatus{}, invalidSetup("administrator.password", "must contain between 8 and 1024 characters")
	}
	if bootstrap.migrator == nil || bootstrap.store == nil || bootstrap.passwords == nil {
		return domain.SetupStatus{}, setupUnavailable("bootstrap", errors.New("bootstrap dependencies are unavailable"))
	}
	if err := bootstrap.migrator.Migrate(ctx, databaseURL); err != nil {
		return domain.SetupStatus{}, setupUnavailable("postgresql", fmt.Errorf("migrate database: %w", err))
	}
	administratorConfigured, installationID, err := bootstrap.databaseInstallationState(ctx, databaseURL)
	if err != nil {
		return domain.SetupStatus{}, setupUnavailable("postgresql", err)
	}
	if administratorConfigured {
		if data.AdminID != uuid.Nil && installationID == data.AdminID {
			if err := bootstrap.store.MarkCompleted(); err != nil {
				return domain.SetupStatus{}, setupUnavailable("bootstrap_file", err)
			}
			runtimeKey, _, keyErr := bootstrap.resolveEncryptionKey(data)
			if keyErr != nil {
				return domain.SetupStatus{}, setupUnavailable("bootstrap", keyErr)
			}
			if bootstrap.activate != nil {
				if err := bootstrap.activate(ctx, domain.RuntimeBootstrap{DatabaseURL: databaseURL, ConfigEncryptionKey: runtimeKey, AdminID: data.AdminID}); err != nil {
					return domain.SetupStatus{}, setupUnavailable("runtime", err)
				}
			}
			return domain.SetupStatus{State: domain.SetupCompleted, DatabaseConfigured: true, DatabaseManagedExternally: bootstrap.databaseManagedExternally, AdministratorConfigured: true}, nil
		}
		return domain.SetupStatus{}, setupAlreadyCompleted()
	}

	runtimeKey, encodedKey, err := bootstrap.resolveEncryptionKey(data)
	if err != nil {
		return domain.SetupStatus{}, setupUnavailable("bootstrap", err)
	}
	if data.AdminID == uuid.Nil {
		passwordHash, err := bootstrap.passwords.Hash(input.AdministratorPassword)
		if err != nil {
			return domain.SetupStatus{}, setupUnavailable("password_hasher", err)
		}
		data = platformconfig.BootstrapData{
			DatabaseURL:       bootstrap.persistedDatabaseURL(databaseURL),
			EncryptionKey:     encodedKey,
			AdminID:           uuid.New(),
			AdminUsername:     username,
			AdminPasswordHash: passwordHash,
		}
	} else {
		valid, err := bootstrap.passwords.Verify(input.AdministratorPassword, data.AdminPasswordHash)
		if err != nil {
			return domain.SetupStatus{}, setupUnavailable("password_hasher", err)
		}
		if data.AdminUsername != username || !valid {
			return domain.SetupStatus{}, NewError("setup_request_changed", "the pending installation was started with different administrator credentials", ErrStateConflict, nil)
		}
	}
	cipher, err := NewSecretCipher(runtimeKey)
	if err != nil {
		return domain.SetupStatus{}, setupUnavailable("secret_cipher", err)
	}
	initialConfiguration, err := NewConfigurationService(nil, cipher).PrepareInitial(input.Settings, input.Secrets, data.AdminID)
	if err != nil {
		if errors.Is(err, ErrInvalidInput) {
			return domain.SetupStatus{}, err
		}
		return domain.SetupStatus{}, setupUnavailable("secret_cipher", err)
	}
	if err := bootstrap.store.WriteConfigured(data); err != nil {
		return domain.SetupStatus{}, setupUnavailable("bootstrap_file", err)
	}
	if err := bootstrap.createInstallation(ctx, databaseURL, data, initialConfiguration); err != nil {
		return domain.SetupStatus{}, err
	}
	if err := bootstrap.store.MarkCompleted(); err != nil {
		return domain.SetupStatus{}, setupUnavailable("bootstrap_file", err)
	}
	if bootstrap.activate != nil {
		if err := bootstrap.activate(ctx, domain.RuntimeBootstrap{DatabaseURL: databaseURL, ConfigEncryptionKey: runtimeKey, AdminID: data.AdminID}); err != nil {
			return domain.SetupStatus{}, setupUnavailable("runtime", err)
		}
	}
	return domain.SetupStatus{
		State:                     domain.SetupCompleted,
		DatabaseConfigured:        true,
		DatabaseManagedExternally: bootstrap.databaseManagedExternally,
		AdministratorConfigured:   true,
	}, nil
}

// ActivateExisting migrates and activates an installation already represented by
// environment configuration or a completed bootstrap file.
func (bootstrap *Bootstrap) ActivateExisting(ctx context.Context) (bool, error) {
	bootstrap.mu.Lock()
	defer bootstrap.mu.Unlock()
	if bootstrap.initializing {
		return false, nil
	}
	data, completed, err := bootstrap.loadStored()
	if err != nil {
		return false, err
	}
	databaseURL := bootstrap.effectiveDatabaseURL(data)
	if databaseURL == "" {
		return false, nil
	}
	if bootstrap.migrator == nil {
		return false, fmt.Errorf("bootstrap migrator is unavailable")
	}
	if err := bootstrap.migrator.Migrate(ctx, databaseURL); err != nil {
		return false, err
	}
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		return false, err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return false, err
	}
	queries := db.New(pool)
	firstAdmin, err := queries.GetFirstAdminUser(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load existing administrator: %w", err)
	}
	runtimeKey, encodedKey, err := bootstrap.resolveEncryptionKey(data)
	if err != nil {
		return false, err
	}
	adminID := repository.UUIDFromPG(firstAdmin.ID)
	if data.AdminID != uuid.Nil && data.AdminID != adminID {
		installation, installErr := queries.GetInstallationState(ctx)
		if installErr != nil || repository.UUIDFromPG(installation.CompletedBy) != data.AdminID {
			return false, fmt.Errorf("bootstrap administrator does not match the database installation")
		}
		adminID = data.AdminID
	}
	if _, err := queries.GetInstallationState(ctx); errors.Is(err, pgx.ErrNoRows) {
		if _, completeErr := queries.CompleteInstallation(ctx, repository.UUIDToPG(adminID)); completeErr != nil {
			return false, fmt.Errorf("record existing installation: %w", completeErr)
		}
	} else if err != nil {
		return false, fmt.Errorf("load installation state: %w", err)
	}
	if !completed {
		if data.AdminID == uuid.Nil {
			data = platformconfig.BootstrapData{
				DatabaseURL:       bootstrap.persistedDatabaseURL(databaseURL),
				EncryptionKey:     encodedKey,
				AdminID:           adminID,
				AdminUsername:     firstAdmin.Username,
				AdminPasswordHash: firstAdmin.PasswordHash,
			}
			if err := bootstrap.store.WriteConfigured(data); err != nil {
				return false, err
			}
		}
		if err := bootstrap.store.MarkCompleted(); err != nil {
			return false, err
		}
	}
	if bootstrap.activate != nil {
		if err := bootstrap.activate(ctx, domain.RuntimeBootstrap{DatabaseURL: databaseURL, ConfigEncryptionKey: runtimeKey, AdminID: adminID}); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (bootstrap *Bootstrap) createInstallation(
	ctx context.Context,
	databaseURL string,
	data platformconfig.BootstrapData,
	configuration domain.SaveConfiguration,
) error {
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		return setupUnavailable("postgresql", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return setupUnavailable("postgresql", err)
	}
	transactor := database.NewTransactor(pool)
	return transactor.WithinTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(scope database.TxScope) error {
		if _, err := scope.Tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", setupAdvisoryLock); err != nil {
			return setupUnavailable("postgresql", fmt.Errorf("lock installation: %w", err))
		}
		count, err := scope.Queries.CountAdminUsers(ctx)
		if err != nil {
			return setupUnavailable("postgresql", fmt.Errorf("count administrators: %w", err))
		}
		if count > 0 {
			installation, installErr := scope.Queries.GetInstallationState(ctx)
			if installErr == nil && repository.UUIDFromPG(installation.CompletedBy) == data.AdminID {
				return nil
			}
			return setupAlreadyCompleted()
		}
		if _, err := scope.Queries.CreateAdminUser(ctx, db.CreateAdminUserParams{
			ID:           repository.UUIDToPG(data.AdminID),
			Username:     data.AdminUsername,
			PasswordHash: data.AdminPasswordHash,
		}); err != nil {
			return setupUnavailable("postgresql", fmt.Errorf("create administrator: %w", err))
		}
		if _, err := repository.SaveConfigurationInTx(ctx, scope, configuration); err != nil {
			return setupUnavailable("postgresql", fmt.Errorf("create runtime configuration: %w", err))
		}
		if _, err := scope.Queries.CompleteInstallation(ctx, repository.UUIDToPG(data.AdminID)); err != nil {
			return setupUnavailable("postgresql", fmt.Errorf("record installation completion: %w", err))
		}
		return nil
	})
}

func (bootstrap *Bootstrap) databaseInstallationState(
	ctx context.Context,
	databaseURL string,
) (bool, uuid.UUID, error) {
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		return false, uuid.Nil, err
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return false, uuid.Nil, err
	}
	queries := db.New(pool)
	count, err := queries.CountAdminUsers(ctx)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return false, uuid.Nil, nil
		}
		return false, uuid.Nil, err
	}
	installation, err := queries.GetInstallationState(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return count > 0, uuid.Nil, nil
	}
	if err != nil {
		return false, uuid.Nil, err
	}
	return count > 0, repository.UUIDFromPG(installation.CompletedBy), nil
}

func (bootstrap *Bootstrap) resolveInitializationDatabase(
	requested *domain.SetupDatabase,
	stored platformconfig.BootstrapData,
) (string, error) {
	if bootstrap.databaseManagedExternally {
		if requested != nil {
			return "", invalidSetup("database", "must be omitted because DATABASE_URL is managed by the deployment")
		}
		if strings.TrimSpace(bootstrap.databaseURL) == "" {
			return "", invalidSetup("database", "DATABASE_URL is empty")
		}
		return bootstrap.databaseURL, nil
	}
	if stored.DatabaseURL != "" {
		if requested != nil {
			resolved, err := databaseURL(*requested)
			if err != nil {
				return "", err
			}
			if resolved != stored.DatabaseURL {
				return "", NewError("setup_request_changed", "the pending installation uses different database settings", ErrStateConflict, nil)
			}
		}
		return stored.DatabaseURL, nil
	}
	if requested == nil {
		return "", invalidSetup("database", "is required when DATABASE_URL is not managed by the deployment")
	}
	return databaseURL(*requested)
}

func databaseURL(database domain.SetupDatabase) (string, error) {
	host := strings.TrimSpace(database.Host)
	name := strings.TrimSpace(database.Database)
	username := strings.TrimSpace(database.Username)
	if host == "" || name == "" || username == "" {
		return "", invalidSetup("database", "host, database, and username are required")
	}
	if database.Port <= 0 || database.Port > 65535 {
		return "", invalidSetup("database.port", "must be between 1 and 65535")
	}
	switch database.SSLMode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return "", invalidSetup("database.sslMode", "is unsupported")
	}
	parsed := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(username, database.Password),
		Host:   net.JoinHostPort(host, strconv.Itoa(database.Port)),
		Path:   "/" + name,
	}
	query := parsed.Query()
	query.Set("sslmode", database.SSLMode)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (bootstrap *Bootstrap) resolveEncryptionKey(data platformconfig.BootstrapData) ([]byte, string, error) {
	if len(bootstrap.configEncryptionKey) > 0 {
		if len(bootstrap.configEncryptionKey) != 32 {
			return nil, "", fmt.Errorf("configuration encryption key must be exactly 32 bytes")
		}
		return append([]byte(nil), bootstrap.configEncryptionKey...), "", nil
	}
	if data.EncryptionKey != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(data.EncryptionKey)
		if err != nil || len(decoded) != 32 {
			return nil, "", fmt.Errorf("stored configuration encryption key is invalid")
		}
		return decoded, data.EncryptionKey, nil
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(bootstrap.random, key); err != nil {
		return nil, "", fmt.Errorf("generate configuration encryption key: %w", err)
	}
	return key, base64.RawURLEncoding.EncodeToString(key), nil
}

func (bootstrap *Bootstrap) effectiveDatabaseURL(data platformconfig.BootstrapData) string {
	if bootstrap.databaseURL != "" {
		return bootstrap.databaseURL
	}
	return data.DatabaseURL
}

func (bootstrap *Bootstrap) persistedDatabaseURL(databaseURL string) string {
	if bootstrap.databaseManagedExternally {
		return ""
	}
	return databaseURL
}

func (bootstrap *Bootstrap) loadStored() (platformconfig.BootstrapData, bool, error) {
	if bootstrap.store == nil {
		return platformconfig.BootstrapData{}, false, fmt.Errorf("bootstrap configuration store is unavailable")
	}
	data, completed, err := bootstrap.store.Load()
	if errors.Is(err, os.ErrNotExist) {
		return platformconfig.BootstrapData{}, false, nil
	}
	if err != nil {
		return platformconfig.BootstrapData{}, false, err
	}
	return data, completed, nil
}

func invalidSetup(field, reason string) error {
	return NewError("invalid_request", "installation request is invalid", ErrInvalidInput, map[string]any{"field": field, "reason": reason})
}

func setupAlreadyCompleted() error {
	return NewError("setup_already_completed", "installation has already completed", ErrStateConflict, nil)
}

func setupUnavailable(dependency string, cause error) error {
	return NewError("service_unavailable", "installation dependency is unavailable", errors.Join(ErrUnavailable, cause), map[string]any{"dependency": dependency})
}
