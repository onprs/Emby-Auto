package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvalidArgumentsDoNotOpenRuntime(t *testing.T) {
	var output bytes.Buffer
	if status := run(context.Background(), []string{"--state", "invalid"}, &output, &output); status != 2 || !strings.Contains(output.String(), "invalid_arguments") {
		t.Fatalf("参数校验 = %d，%s", status, output.String())
	}
}

func TestRuntimeFailureDoesNotExposeConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://fixture_user:fixture_private_password@database.invalid/fixture")
	t.Setenv("CONFIG_ENCRYPTION_KEY", "fixture-invalid-encryption-key")
	t.Setenv("BOOTSTRAP_CONFIG_PATH", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("EMBY_AUTO_HOST_CONTROL_EXECUTABLE", "/fixture/control")
	var output bytes.Buffer
	if status := run(context.Background(), []string{"--state", "status"}, &output, &output); status != 1 || !strings.Contains(output.String(), "runtime_configuration_unavailable") {
		t.Fatalf("配置失败分类 = %d，%s", status, output.String())
	}
	for _, private := range []string{"fixture_private_password", "database.invalid", "fixture-invalid-encryption-key"} {
		if strings.Contains(output.String(), private) {
			t.Fatal("维护命令暴露了运行配置")
		}
	}
}
