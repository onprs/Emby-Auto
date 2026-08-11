package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/legacymigration"
)

func TestRunDefaultsToReadOnlyDryRunAndWritesReport(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "tasks.json"), []byte("[]\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(t.TempDir(), "report.json")
	var output bytes.Buffer
	if err := run(context.Background(), []string{
		"--runtime-dir", directory,
		"--profile-extension", "mkv",
		"--report", reportPath,
	}, &output); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(output.String(), "mode=dry-run succeeded=true") {
		t.Fatalf("output = %q", output.String())
	}
	payload, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report legacymigration.Report
	if err := json.Unmarshal(payload, &report); err != nil {
		t.Fatal(err)
	}
	if report.Mode != "dry-run" || !report.Succeeded || report.Fingerprint == "" || report.Counts.Discovered != 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestParseFlagsRejectsUnverifiedApply(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	_, err := parseFlags([]string{
		"--runtime-dir", t.TempDir(),
		"--profile-extension", "mkv",
		"--report", filepath.Join(t.TempDir(), "report.json"),
		"--apply",
		"--verify-files=false",
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed with --apply") {
		t.Fatalf("parseFlags() error = %v", err)
	}
}

func TestPathMapFlagRejectsAmbiguousValue(t *testing.T) {
	var mappings pathMapFlags
	if err := mappings.Set("missing-separator"); err == nil {
		t.Fatal("path map without OLD=NEW was accepted")
	}
	if err := mappings.Set("/old=/new"); err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 || mappings[0].From != "/old" || mappings[0].To != "/new" {
		t.Fatalf("mappings = %#v", mappings)
	}
}
