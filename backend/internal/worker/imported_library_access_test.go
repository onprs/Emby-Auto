package worker

import (
	"context"
	"errors"
	"testing"
)

type hostControlExecutorStub struct {
	executable string
	command    string
	output     []byte
	err        error
}

func (stub *hostControlExecutorStub) Output(_ context.Context, executable, command string) ([]byte, error) {
	stub.executable = executable
	stub.command = command
	return stub.output, stub.err
}

func TestResolveConfiguredImportedLibraryAccessUsesExplicitOwner(t *testing.T) {
	executor := &hostControlExecutorStub{err: errors.New("must not execute")}
	access, err := ResolveConfiguredImportedLibraryAccess(context.Background(), 10001, "", executor)
	if err != nil {
		t.Fatalf("ResolveConfiguredImportedLibraryAccess() error = %v", err)
	}
	resolved, ok := access.(osImportedLibraryAccess)
	if !ok || resolved.ownerUID != 10001 {
		t.Fatalf("resolved access = %#v", access)
	}
	if executor.command != "" {
		t.Fatalf("host control command = %q, want no invocation", executor.command)
	}
}

func TestResolveImportedLibraryAccessUsesFixedHostCommand(t *testing.T) {
	executor := &hostControlExecutorStub{output: []byte("999\n")}
	access, err := ResolveImportedLibraryAccess(context.Background(), "/app/bin/emby-auto-host-control", executor)
	if err != nil {
		t.Fatalf("ResolveImportedLibraryAccess() error = %v", err)
	}
	resolved, ok := access.(osImportedLibraryAccess)
	if !ok || resolved.ownerUID != 999 {
		t.Fatalf("resolved access = %#v", access)
	}
	if executor.executable != "/app/bin/emby-auto-host-control" || executor.command != "media-owner" {
		t.Fatalf("host control call = %q %q", executor.executable, executor.command)
	}
}

func TestResolveImportedLibraryAccessRejectsUnavailableOrInvalidOwner(t *testing.T) {
	tests := []struct {
		name       string
		executable string
		executor   HostControlExecutor
	}{
		{name: "missing executable", executor: &hostControlExecutorStub{output: []byte("999\n")}},
		{name: "missing executor", executable: "/host-control"},
		{name: "execution failure", executable: "/host-control", executor: &hostControlExecutorStub{err: errors.New("socket unavailable")}},
		{name: "root owner", executable: "/host-control", executor: &hostControlExecutorStub{output: []byte("0\n")}},
		{name: "nonnumeric owner", executable: "/host-control", executor: &hostControlExecutorStub{output: []byte("emby\n")}},
		{name: "oversized response", executable: "/host-control", executor: &hostControlExecutorStub{output: make([]byte, 33)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ResolveImportedLibraryAccess(context.Background(), test.executable, test.executor); err == nil {
				t.Fatal("ResolveImportedLibraryAccess() error = nil")
			}
		})
	}
}
