package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestControlSocketDoesNotReplaceRunningListener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	defer func() { _ = listener.Close() }()
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareSocketPath(path); err == nil {
		t.Fatal("替换了正在使用的控制 socket")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("运行中的 socket 发生变化：%v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := prepareSocketPath(path); err != nil {
		t.Fatalf("无法清理已停止的 socket：%v", err)
	}
}

func TestPrivilegedHelperRejectsUntrustedParent(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("在隔离 root 容器中验证特权脚本父目录")
	}
	parent := filepath.Join(t.TempDir(), "helper-directory")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(parent, "helper.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(parent, 10001, -1); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivilegedExecutable(helper); err == nil {
		t.Fatal("允许从非 root 所有的目录执行特权脚本")
	}
}
