package database

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	appmigrations "github.com/onprs/emby-auto/backend/db/migrations"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

const applicationMigrationLock int64 = 0x454d42594d494752

type Migrator struct{}

type MigrationStatus struct {
	ApplicationCurrent int64
	ApplicationLatest  int64
	ApplicationDirty   bool
	RiverCurrent       int
	RiverLatest        int
}

func (status MigrationStatus) Current() bool {
	return !status.ApplicationDirty &&
		status.ApplicationCurrent == status.ApplicationLatest &&
		status.RiverCurrent == status.RiverLatest
}

type applicationMigration struct {
	version int64
	name    string
	sql     string
}

func NewMigrator() *Migrator {
	return &Migrator{}
}

func LatestApplicationMigrationVersion() (int64, error) {
	migrations, err := loadApplicationMigrations()
	if err != nil {
		return 0, err
	}
	if len(migrations) == 0 {
		return 0, nil
	}
	return migrations[len(migrations)-1].version, nil
}

func CheckMigrationStatus(ctx context.Context, pool *pgxpool.Pool) (MigrationStatus, error) {
	latestApplication, err := LatestApplicationMigrationVersion()
	if err != nil {
		return MigrationStatus{}, err
	}
	status := MigrationStatus{ApplicationLatest: latestApplication}
	var applicationTableExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&applicationTableExists); err != nil {
		return MigrationStatus{}, fmt.Errorf("check application migration table: %w", err)
	}
	if applicationTableExists {
		var rowCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.schema_migrations`).Scan(&rowCount); err != nil {
			return MigrationStatus{}, fmt.Errorf("count application migration rows: %w", err)
		}
		if rowCount > 1 {
			return MigrationStatus{}, fmt.Errorf("application migration table contains %d rows; expected at most one", rowCount)
		}
		if rowCount == 1 {
			if err := pool.QueryRow(ctx, `SELECT version, dirty FROM public.schema_migrations`).Scan(&status.ApplicationCurrent, &status.ApplicationDirty); err != nil {
				return MigrationStatus{}, fmt.Errorf("read application migration status: %w", err)
			}
		}
	}

	riverMigrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return MigrationStatus{}, fmt.Errorf("create River migrator: %w", err)
	}
	riverExisting, err := riverMigrator.ExistingVersions(ctx)
	if err != nil {
		return MigrationStatus{}, fmt.Errorf("read River migration status: %w", err)
	}
	riverAll := riverMigrator.AllVersions()
	status.RiverCurrent = len(riverExisting)
	status.RiverLatest = len(riverAll)
	for index, existing := range riverExisting {
		if index >= len(riverAll) || existing.Version != riverAll[index].Version {
			return MigrationStatus{}, fmt.Errorf("river migration history is not a prefix of the bundled migration line")
		}
	}
	return status, nil
}

func RequireCurrentMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	status, err := CheckMigrationStatus(ctx, pool)
	if err != nil {
		return err
	}
	if status.ApplicationDirty {
		return fmt.Errorf("application schema is dirty at migration version %d", status.ApplicationCurrent)
	}
	if status.ApplicationCurrent != status.ApplicationLatest {
		return fmt.Errorf("application migrations are behind: database=%d bundled=%d", status.ApplicationCurrent, status.ApplicationLatest)
	}
	if status.RiverCurrent != status.RiverLatest {
		return fmt.Errorf("river migrations are behind: database=%d bundled=%d", status.RiverCurrent, status.RiverLatest)
	}
	return nil
}

func (migrator *Migrator) Migrate(ctx context.Context, databaseURL string) error {
	pool, err := Open(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open PostgreSQL for migrations: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("connect PostgreSQL for migrations: %w", err)
	}
	if err := migrateApplication(ctx, pool); err != nil {
		return err
	}
	riverMigrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("create River migrator: %w", err)
	}
	if _, err := riverMigrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("apply River migrations: %w", err)
	}
	return nil
}

func migrateApplication(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadApplicationMigrations()
	if err != nil {
		return err
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire PostgreSQL migration connection: %w", err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, "SELECT pg_advisory_lock($1)", applicationMigrationLock); err != nil {
		return fmt.Errorf("lock application migrations: %w", err)
	}
	defer func() {
		_, _ = connection.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", applicationMigrationLock)
	}()
	if _, err := connection.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.schema_migrations (
			version bigint NOT NULL PRIMARY KEY,
			dirty boolean NOT NULL
		)`); err != nil {
		return fmt.Errorf("ensure application migration table: %w", err)
	}
	var currentVersion int64
	var dirty bool
	err = connection.QueryRow(ctx, "SELECT version, dirty FROM public.schema_migrations LIMIT 1").Scan(&currentVersion, &dirty)
	if err == pgx.ErrNoRows {
		currentVersion = 0
		dirty = false
	} else if err != nil {
		return fmt.Errorf("read application migration version: %w", err)
	}
	if dirty {
		return fmt.Errorf("application schema is dirty at migration version %d", currentVersion)
	}
	if currentVersion > int64(len(migrations)) {
		return fmt.Errorf("database application migration version %d is newer than bundled version %d", currentVersion, len(migrations))
	}
	for _, migration := range migrations {
		if migration.version <= currentVersion {
			continue
		}
		tx, err := connection.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("begin application migration %s: %w", migration.name, err)
		}
		if _, err := tx.Exec(ctx, migration.sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply application migration %s: %w", migration.name, err)
		}
		if _, err := tx.Exec(ctx, "TRUNCATE public.schema_migrations"); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("update application migration %s: %w", migration.name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO public.schema_migrations (version, dirty) VALUES ($1, false)", migration.version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record application migration %s: %w", migration.name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit application migration %s: %w", migration.name, err)
		}
		currentVersion = migration.version
	}
	return nil
}

func loadApplicationMigrations() ([]applicationMigration, error) {
	entries, err := fs.ReadDir(appmigrations.Files, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded application migrations: %w", err)
	}
	migrations := make([]applicationMigration, 0, len(entries)/2)
	seen := map[int64]struct{}{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		separator := strings.IndexByte(name, '_')
		if separator <= 0 {
			return nil, fmt.Errorf("migration filename %q has no numeric version", name)
		}
		version, err := strconv.ParseInt(name[:separator], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("migration filename %q has an invalid version", name)
		}
		if _, duplicate := seen[version]; duplicate {
			return nil, fmt.Errorf("application migration version %d is duplicated", version)
		}
		seen[version] = struct{}{}
		contents, err := appmigrations.Files.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read embedded migration %q: %w", name, err)
		}
		migrations = append(migrations, applicationMigration{version: version, name: name, sql: string(contents)})
	}
	sort.Slice(migrations, func(left, right int) bool { return migrations[left].version < migrations[right].version })
	for index, migration := range migrations {
		expected := int64(index + 1)
		if migration.version != expected {
			return nil, fmt.Errorf("application migrations are not contiguous: expected %d, found %d", expected, migration.version)
		}
	}
	return migrations, nil
}
