package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
)

func TestBootstrapStoreWritesImmutableConfigurationAndCompletionMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "bootstrap.json")
	store := NewBootstrapStore(path)
	data := BootstrapData{
		DatabaseURL:       "postgres://user:password@postgres:5432/emby?sslmode=disable",
		EncryptionKey:     "fixture-key",
		AdminID:           uuid.MustParse("81000000-0000-0000-0000-000000000001"),
		AdminUsername:     "admin",
		AdminPasswordHash: "fixture-password-hash",
	}
	if err := store.WriteConfigured(data); err != nil {
		t.Fatalf("WriteConfigured() error = %v", err)
	}
	loaded, completed, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if completed || loaded.DatabaseURL != data.DatabaseURL || loaded.AdminID != data.AdminID {
		t.Fatalf("Load() = %#v, %v", loaded, completed)
	}
	if err := store.WriteConfigured(data); err != nil {
		t.Fatalf("idempotent WriteConfigured() error = %v", err)
	}
	changed := data
	changed.DatabaseURL = "postgres://different@postgres/other"
	if err := store.WriteConfigured(changed); err == nil {
		t.Fatal("changed WriteConfigured() error = nil")
	}
	if err := store.MarkCompleted(); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}
	if err := store.MarkCompleted(); err != nil {
		t.Fatalf("idempotent MarkCompleted() error = %v", err)
	}
	_, completed, err = store.Load()
	if err != nil || !completed {
		t.Fatalf("completed Load() = %v, %v", completed, err)
	}
	if runtime.GOOS != "windows" {
		for _, candidate := range []string{path, path + ".complete"} {
			info, err := os.Stat(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("%s permissions = %o", candidate, info.Mode().Perm())
			}
		}
	}
}

func TestBootstrapStoreRejectsTamperedCompletedConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap.json")
	store := NewBootstrapStore(path)
	data := BootstrapData{
		DatabaseURL:       "postgres://user:password@postgres/emby",
		EncryptionKey:     "fixture-key",
		AdminID:           uuid.MustParse("81000000-0000-0000-0000-000000000002"),
		AdminUsername:     "admin",
		AdminPasswordHash: "fixture-password-hash",
	}
	if err := store.WriteConfigured(data); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCompleted(); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded[0] = ' '
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(); err == nil {
		t.Fatal("Load(tampered) error = nil")
	}
}

func TestBootstrapStoreMissingFile(t *testing.T) {
	_, _, err := NewBootstrapStore(filepath.Join(t.TempDir(), "missing.json")).Load()
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(missing) error = %v", err)
	}
}
