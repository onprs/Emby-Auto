//go:build linux

package main

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRequest(t *testing.T) {
	t.Parallel()

	for _, request := range []controlRequest{
		{Command: "worker-status"},
		{Command: "worker-start"},
		{Command: "worker-stop"},
		{Command: "media-owner"},
	} {
		if err := validateRequest(request); err != nil {
			t.Fatalf("validateRequest(%+v) error = %v", request, err)
		}
	}

	for _, request := range []controlRequest{{}, {Command: "shell"}, {Command: "apply"}} {
		if err := validateRequest(request); err == nil {
			t.Fatalf("validateRequest(%+v) error = nil", request)
		}
	}
}

func TestResolveMediaOwnerUIDUsesFixedEmbyAccount(t *testing.T) {
	requested := ""
	uid, err := resolveMediaOwnerUID(func(username string) (*user.User, error) {
		requested = username
		return &user.User{Username: "emby", Uid: "999"}, nil
	})
	if err != nil {
		t.Fatalf("resolveMediaOwnerUID() error = %v", err)
	}
	if requested != "emby" || uid != 999 {
		t.Fatalf("lookup = %q, UID = %d", requested, uid)
	}
}

func TestResolveMediaOwnerUIDRejectsMissingRootOrNonNumericAccount(t *testing.T) {
	tests := []struct {
		name string
		user *user.User
		err  error
	}{
		{name: "missing", err: os.ErrNotExist},
		{name: "root", user: &user.User{Username: "emby", Uid: "0"}},
		{name: "nonnumeric", user: &user.User{Username: "emby", Uid: "not-a-uid"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolveMediaOwnerUID(func(string) (*user.User, error) { return test.user, test.err }); err == nil {
				t.Fatal("resolveMediaOwnerUID() error = nil")
			}
		})
	}
}

func TestExecuteRequestRestrictsWorkerHelperArgumentsAndEnvironment(t *testing.T) {
	directory := t.TempDir()
	outputPath := filepath.Join(directory, "output")
	helperPath := filepath.Join(directory, "runtime-helper")
	helper := "#!/bin/sh\n" +
		"printf '%s\\n' \"$#\" \"$1\" \"${UNTRUSTED_ENVIRONMENT-unset}\" > \"" + outputPath + "\"\n" +
		"printf 'stopped\\n'\n"
	if err := os.WriteFile(helperPath, []byte(helper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UNTRUSTED_ENVIRONMENT", "must-not-pass")

	response := executeRequest(context.Background(), helperPath, controlRequest{Command: "worker-stop"})
	if response.Error != "" || response.Status != "stopped" {
		t.Fatalf("executeRequest() = %+v, want stopped", response)
	}
	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "1\nstop\nunset\n"; string(output) != want {
		t.Fatalf("runtime helper invocation = %q, want %q", output, want)
	}
}

func TestExecuteRequestRejectsInvalidWorkerStatus(t *testing.T) {
	directory := t.TempDir()
	helperPath := filepath.Join(directory, "runtime-helper")
	if err := os.WriteFile(helperPath, []byte("#!/bin/sh\nprintf 'unknown\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	response := executeRequest(context.Background(), helperPath, controlRequest{Command: "worker-status"})
	if !strings.Contains(response.Error, "invalid Worker status") {
		t.Fatalf("executeRequest() = %+v", response)
	}
}
