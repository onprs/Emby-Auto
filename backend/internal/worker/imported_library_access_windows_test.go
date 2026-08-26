//go:build windows

package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestImportedLibraryAccessAppliesTreeOnWindowsWithoutUIDOwnership(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "episode.mkv")
	if err := os.WriteFile(filePath, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	access, err := NewImportedLibraryAccess(1000)
	if err != nil {
		t.Fatalf("NewImportedLibraryAccess() error = %v", err)
	}
	if err := access.ApplyTree(context.Background(), root); err != nil {
		t.Fatalf("ApplyTree() error = %v", err)
	}
	if data, err := os.ReadFile(filePath); err != nil || string(data) != "fixture" {
		t.Fatalf("imported file = %q, %v", data, err)
	}
}
