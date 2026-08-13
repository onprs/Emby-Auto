//go:build linux

package systemmetrics

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type stubHostControlReader struct {
	received uint64
	sent     uint64
	ok       bool
}

func (stub stubHostControlReader) ReadHostCounters(context.Context) (uint64, uint64, bool) {
	return stub.received, stub.sent, stub.ok
}

func TestGopsutilSourceUsesHostControlCounters(t *testing.T) {
	source := NewGopsutilSource(stubHostControlReader{received: 111, sent: 222, ok: true})
	reading := source.Read(context.Background(), nil)
	if !reading.NetworkAvailable {
		t.Fatal("NetworkAvailable = false, want true")
	}
	if reading.NetworkBytesRecv != 111 || reading.NetworkBytesSent != 222 {
		t.Fatalf("counters = %d/%d, want 111/222", reading.NetworkBytesRecv, reading.NetworkBytesSent)
	}
}

func TestGopsutilSourceHostControlFailureExplicitlyUnavailable(t *testing.T) {
	source := NewGopsutilSource(stubHostControlReader{ok: false})
	reading := source.Read(context.Background(), nil)
	if reading.NetworkAvailable {
		t.Fatal("NetworkAvailable = true, want explicit unavailable on host-control failure")
	}
	if reading.NetworkBytesRecv != 0 || reading.NetworkBytesSent != 0 {
		t.Fatalf("counters = %d/%d, want zeroed counters", reading.NetworkBytesRecv, reading.NetworkBytesSent)
	}
}

func TestCommandHostControlNetworkReaderParsesCounters(t *testing.T) {
	executable := writeFixtureExecutable(t, "#!/bin/sh\nprintf '123456 789012\\n'\n")
	reader := CommandHostControlNetworkReader{Executable: executable}
	received, sent, ok := reader.ReadHostCounters(context.Background())
	if !ok || received != 123456 || sent != 789012 {
		t.Fatalf("counters = %d/%d ok=%t, want 123456/789012 true", received, sent, ok)
	}
}

func TestCommandHostControlNetworkReaderRejectsInvalidOutput(t *testing.T) {
	executable := writeFixtureExecutable(t, "#!/bin/sh\nprintf 'not-a-counter\\n'\n")
	reader := CommandHostControlNetworkReader{Executable: executable}
	if _, _, ok := reader.ReadHostCounters(context.Background()); ok {
		t.Fatal("ReadHostCounters() ok = true for invalid output")
	}
}

func TestCommandHostControlNetworkReaderFailureExplicitlyUnavailable(t *testing.T) {
	executable := writeFixtureExecutable(t, "#!/bin/sh\nexit 3\n")
	reader := CommandHostControlNetworkReader{Executable: executable}
	if _, _, ok := reader.ReadHostCounters(context.Background()); ok {
		t.Fatal("ReadHostCounters() ok = true for failing executable")
	}
}

func TestCommandHostControlNetworkReaderMissingExecutableUnavailable(t *testing.T) {
	reader := CommandHostControlNetworkReader{}
	if _, _, ok := reader.ReadHostCounters(context.Background()); ok {
		t.Fatal("ReadHostCounters() ok = true without executable")
	}
}

func writeFixtureExecutable(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "host-control-fixture")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
