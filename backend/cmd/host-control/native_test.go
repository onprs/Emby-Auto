package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeControlAcceptsConfiguredPeerAndPreservesRootAccess(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "worker-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho stopped\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Getuid())
	if uid == 0 {
		uid = 10001
	}
	for _, command := range []string{"worker-status", "media-owner"} {
		socket := filepath.Join(root, command+".sock")
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		done := make(chan struct{})
		go func() {
			defer close(done)
			connection, err := listener.AcceptUnix()
			if err == nil {
				handleConnection(ctx, connection, helper, serverAccess{allowedUID: uid})
			}
		}()
		response, err := sendRequest(ctx, socket, controlRequest{Command: command})
		cancel()
		_ = listener.Close()
		<-done
		if err != nil || response.Error != "" {
			t.Fatalf("应用身份未获授权：%#v，%v", response, err)
		}
		if command == "worker-status" && response.Status != "stopped" {
			t.Fatalf("Worker 状态 = %q", response.Status)
		}
		if command == "media-owner" && (response.UID == nil || *response.UID != uid) {
			t.Fatal("原生媒体 UID 未使用配置的应用身份")
		}
	}
}

func TestNativeControlRejectsOtherUsers(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root 保留维护权限；非 root 拒绝路径由普通账户测试覆盖")
	}
	root := t.TempDir()
	socket := filepath.Join(root, "control.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.AcceptUnix()
		if err == nil {
			handleConnection(ctx, connection, "/unreachable-helper", serverAccess{allowedUID: uint32(os.Getuid() + 1)})
		}
	}()
	response, err := sendRequest(ctx, socket, controlRequest{Command: "worker-status"})
	<-done
	if err == nil || !strings.Contains(err.Error(), "UID") {
		t.Fatalf("未拒绝其它用户：%#v，%v", response, err)
	}
}

func TestPrivilegedHelperRequiresRootOwnershipAndRestrictedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivilegedExecutable(path); (err == nil) != (os.Getuid() == 0) {
		t.Fatalf("可执行文件所有者校验 = %v", err)
	}
	if err := os.Chmod(path, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := validatePrivilegedExecutable(path); err == nil {
		t.Fatal("root 控制器接受了组可写脚本")
	}
}
