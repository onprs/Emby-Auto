package legacymigration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func WriteReport(path string, report Report) error {
	if path == "" {
		return fmt.Errorf("migration report path is required")
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o750); err != nil {
		return fmt.Errorf("create migration report directory: %w", err)
	}
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode migration report: %w", err)
	}
	payload = append(payload, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(absolute), ".migration-report-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary migration report: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o640); err != nil {
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		return fmt.Errorf("write migration report: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync migration report: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close migration report: %w", err)
	}
	if err := os.Rename(temporaryPath, absolute); err != nil {
		return fmt.Errorf("commit migration report: %w", err)
	}
	removeTemporary = false
	return nil
}
