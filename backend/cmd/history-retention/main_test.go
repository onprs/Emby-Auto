package main

import (
	"io"
	"strings"
	"testing"
)

func TestRunRejectsRetentionWindowBelowMinimum(t *testing.T) {
	err := run([]string{"--older-than", "1h"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunRejectsOversizedBatch(t *testing.T) {
	err := run([]string{"--batch-size", "5001"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "batch-size") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunRejectsUnexpectedArguments(t *testing.T) {
	err := run([]string{"unexpected"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "positional") {
		t.Fatalf("run() error = %v", err)
	}
}
