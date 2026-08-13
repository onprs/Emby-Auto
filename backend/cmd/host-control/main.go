//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	defaultSocketPath        = "/run/emby-auto-host/control.sock"
	defaultRuntimeHelperPath = "/usr/local/libexec/emby-auto-worker-runtime"
	hostNetworkDevPath       = "/proc/net/dev"
	sysfsNetRoot             = "/sys"
	maxMessageBytes          = 16 << 10
	requestTimeout           = 2 * time.Minute
)

type controlRequest struct {
	Command string `json:"command"`
}

type controlResponse struct {
	Status              string  `json:"status,omitempty"`
	UID                 *uint32 `json:"uid,omitempty"`
	NetworkReceiveBytes *uint64 `json:"networkReceiveBytes,omitempty"`
	NetworkSendBytes    *uint64 `json:"networkSendBytes,omitempty"`
	Error               string  `json:"error,omitempty"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "serve" {
		return runServerCommand(args[1:])
	}
	if len(args) != 1 {
		return errors.New("usage: emby-auto-host-control worker-status|worker-start|worker-stop|media-owner|host-network-counters|serve")
	}

	request := controlRequest{Command: args[0]}
	if err := validateRequest(request); err != nil {
		return err
	}

	socketPath := valueOrDefault(os.Getenv("EMBY_AUTO_HOST_CONTROL_SOCKET"), defaultSocketPath)
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	response, err := sendRequest(ctx, socketPath, request)
	if err != nil {
		return err
	}
	if isWorkerCommand(request.Command) {
		if _, err := fmt.Fprintln(os.Stdout, response.Status); err != nil {
			return fmt.Errorf("write Worker status: %w", err)
		}
	}
	if request.Command == "media-owner" {
		if response.UID == nil {
			return errors.New("host-control response did not include the Emby media owner UID")
		}
		if _, err := fmt.Fprintln(os.Stdout, *response.UID); err != nil {
			return fmt.Errorf("write Emby media owner UID: %w", err)
		}
	}
	if request.Command == "host-network-counters" {
		if response.NetworkReceiveBytes == nil || response.NetworkSendBytes == nil {
			return errors.New("host-control response did not include host network counters")
		}
		if _, err := fmt.Fprintf(os.Stdout, "%d %d\n", *response.NetworkReceiveBytes, *response.NetworkSendBytes); err != nil {
			return fmt.Errorf("write host network counters: %w", err)
		}
	}
	return nil
}

func runServerCommand(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socketPath := flags.String("socket", defaultSocketPath, "Unix socket path")
	runtimeHelperPath := flags.String("runtime-helper", defaultRuntimeHelperPath, "Worker runtime helper path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: emby-auto-host-control serve [--socket PATH] [--runtime-helper PATH]")
	}
	if os.Geteuid() != 0 {
		return errors.New("host-control server must run as root")
	}
	if !filepath.IsAbs(*socketPath) || !filepath.IsAbs(*runtimeHelperPath) {
		return errors.New("socket and runtime helper paths must be absolute")
	}
	if err := validateExecutable(*runtimeHelperPath); err != nil {
		return fmt.Errorf("validate runtime helper: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, *socketPath, *runtimeHelperPath)
}

func serve(ctx context.Context, socketPath, runtimeHelperPath string) error {
	if err := prepareSocketPath(socketPath); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen on host-control socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("secure host-control socket: %w", err)
	}

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept host-control connection: %w", acceptErr)
		}
		handleConnection(ctx, connection, runtimeHelperPath)
	}
}

func handleConnection(parent context.Context, connection *net.UnixConn, runtimeHelperPath string) {
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(requestTimeout))

	response := controlResponse{}
	if uid, err := peerUID(connection); err != nil {
		response.Error = fmt.Sprintf("inspect peer credentials: %v", err)
	} else if uid != 0 {
		response.Error = "host-control socket only accepts uid 0 clients"
	} else {
		request, err := decodeRequest(connection)
		if err != nil {
			response.Error = err.Error()
		} else {
			ctx, cancel := context.WithTimeout(parent, requestTimeout)
			response = executeRequest(ctx, runtimeHelperPath, request)
			cancel()
		}
	}
	_ = json.NewEncoder(connection).Encode(response)
}

func decodeRequest(reader io.Reader) (controlRequest, error) {
	line, err := bufio.NewReader(io.LimitReader(reader, maxMessageBytes+1)).ReadBytes('\n')
	if err != nil {
		return controlRequest{}, fmt.Errorf("read host-control request: %w", err)
	}
	if len(line) > maxMessageBytes {
		return controlRequest{}, errors.New("host-control request is too large")
	}

	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var request controlRequest
	if err := decoder.Decode(&request); err != nil {
		return controlRequest{}, fmt.Errorf("decode host-control request: %w", err)
	}
	if err := validateRequest(request); err != nil {
		return controlRequest{}, err
	}
	return request, nil
}

func executeRequest(ctx context.Context, runtimeHelperPath string, request controlRequest) controlResponse {
	if request.Command == "media-owner" {
		uid, err := resolveMediaOwnerUID(user.Lookup)
		if err != nil {
			return controlResponse{Error: err.Error()}
		}
		return controlResponse{UID: &uid}
	}
	if request.Command == "host-network-counters" {
		received, sent, err := readHostNetworkCounters(hostNetworkDevPath, sysfsNetRoot)
		if err != nil {
			return controlResponse{Error: err.Error()}
		}
		return controlResponse{NetworkReceiveBytes: &received, NetworkSendBytes: &sent}
	}

	action := strings.TrimPrefix(request.Command, "worker-")
	output, err := executeHelper(ctx, runtimeHelperPath, []string{action})
	if err != nil {
		return controlResponse{Error: helperError(output, err)}
	}
	status := strings.TrimSpace(string(output))
	switch status {
	case "running", "stopped", "transitioning":
		return controlResponse{Status: status}
	default:
		return controlResponse{Error: "runtime helper returned an invalid Worker status"}
	}
}

func executeHelper(ctx context.Context, helperPath string, args []string) ([]byte, error) {
	command := exec.CommandContext(ctx, helperPath, args...)
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	return command.CombinedOutput()
}

func helperError(output []byte, err error) string {
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	if len(message) > 2048 {
		message = message[:2048]
	}
	return message
}

func sendRequest(ctx context.Context, socketPath string, request controlRequest) (controlResponse, error) {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return controlResponse{}, fmt.Errorf("connect to host-control socket: %w", err)
	}
	defer func() { _ = connection.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return controlResponse{}, fmt.Errorf("send host-control request: %w", err)
	}
	var response controlResponse
	decoder := json.NewDecoder(io.LimitReader(connection, maxMessageBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return controlResponse{}, fmt.Errorf("read host-control response: %w", err)
	}
	if response.Error != "" {
		return controlResponse{}, errors.New(response.Error)
	}
	return response, nil
}

func validateRequest(request controlRequest) error {
	if request.Command == "media-owner" || request.Command == "host-network-counters" || isWorkerCommand(request.Command) {
		return nil
	}
	return errors.New("command must be worker-status, worker-start, worker-stop, media-owner, or host-network-counters")
}

// deviceBackedInterface 判断接口是否为真实设备背板的物理网卡：
// 内核 sysfs 只给 PCI/USB/virtio 等真实设备背板的网卡提供
// /sys/class/net/<name>/device 条目；bridge、bond、VLAN 子接口、WireGuard、
// veth、tun/tap、dummy 与 loopback 等逻辑接口都没有。这些逻辑接口与底层物理
// 网卡同时累计同一批流量，若一起求和会把宿主网络速度重复计为约两倍，
// 因此只统计有 device 条目的物理接口。
func deviceBackedInterface(sysfsRoot, name string) bool {
	info, err := os.Stat(filepath.Join(sysfsRoot, "class", "net", name, "device"))
	return err == nil && info.IsDir()
}

// readHostNetworkCounters 解析宿主 /proc/net/dev，返回物理网卡（由 sysfs
// device 条目识别）的接收/发送字节计数。格式与内核 procfs 一致：接口行以
// "name:" 开头，随后是接收与发送两组以空格分隔的计数器，rx 为第 1 列、
// tx 为第 9 列。
func readHostNetworkCounters(path, sysfsRoot string) (received, sent uint64, err error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, fmt.Errorf("read host network counters: %w", err)
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) < 3 {
		return 0, 0, fmt.Errorf("host network counters file %s has no interface rows", path)
	}

	for _, line := range lines[2:] {
		separator := strings.LastIndex(line, ":")
		if separator == -1 {
			continue
		}
		// 接口名大小写敏感（Linux 允许 WAN0 等混合/大写名称），
		// 必须保留原始名称用于 sysfs 路径查询，不得小写化。
		name := strings.TrimSpace(line[:separator])
		if name == "" || !deviceBackedInterface(sysfsRoot, name) {
			continue
		}
		fields := strings.Fields(line[separator+1:])
		if len(fields) < 9 {
			continue
		}
		rx, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parse receive counter of interface %s: %w", name, err)
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("parse send counter of interface %s: %w", name, err)
		}
		received += rx
		sent += tx
	}
	return received, sent, nil
}

func resolveMediaOwnerUID(lookup func(string) (*user.User, error)) (uint32, error) {
	account, err := lookup("emby")
	if err != nil {
		return 0, fmt.Errorf("look up host emby user: %w", err)
	}
	parsed, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil || parsed == 0 {
		return 0, errors.New("host emby user must have a non-root numeric UID")
	}
	return uint32(parsed), nil
}

func isWorkerCommand(command string) bool {
	return command == "worker-status" || command == "worker-start" || command == "worker-stop"
}

func validateExecutable(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect host helper: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
		return errors.New("host helper must be an executable regular file, not a symlink")
	}
	return nil
}

func prepareSocketPath(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create host-control runtime directory: %w", err)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect host-control socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("refusing to replace a non-socket host-control path")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale host-control socket: %w", err)
	}
	return nil
}

func peerUID(connection *net.UnixConn) (uint32, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credentials *unix.Ucred
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		credentials, controlErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if controlErr != nil {
		return 0, controlErr
	}
	return credentials.Uid, nil
}

func valueOrDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
