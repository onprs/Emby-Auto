package service

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	platformconfig "github.com/onprs/emby-auto/backend/internal/platform/config"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

// stalledPostgresListener 完成 PostgreSQL v3 启动与明文口令握手后保持静默：
// 它能被真实 pgx 客户端接受，但不会回复任何查询。纯 TCP 静默由连接超时覆盖，
// 这里覆盖“握手成功后才不再响应”的剩余路径。
func stalledPostgresListener(t *testing.T) (host string, port int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				close(done)
				return
			}
			go serveStalledPostgres(connection)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
	})
	addr := listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func serveStalledPostgres(connection net.Conn) {
	defer func() { _ = connection.Close() }()
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)

	// 启动消息：int32 长度 + int32 协议版本 + 参数。
	lengthBuffer := make([]byte, 4)
	if _, err := io.ReadFull(reader, lengthBuffer); err != nil {
		return
	}
	length := int(binary.BigEndian.Uint32(lengthBuffer)) - 4
	if length <= 0 || length > 1<<16 {
		return
	}
	if _, err := io.ReadFull(reader, make([]byte, length)); err != nil {
		return
	}

	// 明文口令认证：请求口令 → 读走口令消息 → 认证成功。
	if err := writePostgresMessage(writer, 'R', postgresInt32(3)); err != nil {
		return
	}
	if _, err := reader.ReadByte(); err != nil {
		return
	}
	if _, err := io.ReadFull(reader, lengthBuffer); err != nil {
		return
	}
	passwordLength := int(binary.BigEndian.Uint32(lengthBuffer)) - 4
	if passwordLength < 0 || passwordLength > 1<<16 {
		return
	}
	if _, err := io.ReadFull(reader, make([]byte, passwordLength)); err != nil {
		return
	}
	if err := writePostgresMessage(writer, 'R', postgresInt32(0)); err != nil {
		return
	}
	for _, parameter := range [][2]string{
		{"server_version", "17.0"},
		{"server_encoding", "UTF8"},
		{"client_encoding", "UTF8"},
		{"DateStyle", "ISO, MDY"},
		{"standard_conforming_strings", "on"},
		{"TimeZone", "UTC"},
	} {
		payload := append(append([]byte(parameter[0]), 0), append([]byte(parameter[1]), 0)...)
		if err := writePostgresMessage(writer, 'S', payload); err != nil {
			return
		}
	}
	if err := writePostgresMessage(writer, 'K', append(postgresInt32(1234), postgresInt32(5678)...)); err != nil {
		return
	}
	if err := writePostgresMessage(writer, 'Z', []byte{'I'}); err != nil {
		return
	}

	// 握手完成：读取并丢弃客户端消息，从不回复。
	for {
		kind, readErr := reader.ReadByte()
		if readErr != nil {
			return
		}
		if _, readErr := io.ReadFull(reader, lengthBuffer); readErr != nil {
			return
		}
		size := int(binary.BigEndian.Uint32(lengthBuffer)) - 4
		if size < 0 || size > 1<<24 {
			return
		}
		if _, readErr := io.ReadFull(reader, make([]byte, size)); readErr != nil {
			return
		}
		if kind == 'X' {
			return
		}
	}
}

func writePostgresMessage(writer *bufio.Writer, kind byte, payload []byte) error {
	header := make([]byte, 5)
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)+4))
	if _, err := writer.Write(header); err != nil {
		return err
	}
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	return writer.Flush()
}

func postgresInt32(value int32) []byte {
	buffer := make([]byte, 4)
	binary.BigEndian.PutUint32(buffer, uint32(value))
	return buffer
}

func stalledSetupInput(host string, port int) domain.InitializeSetup {
	return domain.InitializeSetup{
		Database: &domain.SetupDatabase{
			Host:     host,
			Port:     port,
			Database: "emby_auto",
			Username: "emby_auto",
			Password: "fixture-password",
			SSLMode:  "disable",
		},
		AdministratorUsername: "setup-admin",
		AdministratorPassword: "fixture-password-123",
	}
}

func TestBootstrapInitializeReleasesSetupLockWhenDatabaseStallsAfterHandshake(t *testing.T) {
	host, port := stalledPostgresListener(t)
	const operationTimeout = 300 * time.Millisecond
	bootstrap := NewBootstrap(BootstrapOptions{
		Store:            platformconfig.NewBootstrapStore(filepath.Join(t.TempDir(), "bootstrap.json")),
		Migrator:         database.NewMigratorWithConnectTimeout(operationTimeout),
		Passwords:        NewPasswordHasher(),
		ConnectTimeout:   operationTimeout,
		OperationTimeout: operationTimeout,
	})
	input := stalledSetupInput(host, port)

	started := time.Now()
	_, err := bootstrap.Initialize(context.Background(), input)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Initialize() error = nil, want bounded failure")
	}
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "service_unavailable" || serviceErr.Details["dependency"] != "postgresql" {
		t.Fatalf("Initialize() error = %#v, want postgresql service_unavailable", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Initialize() took %s, want bounded by operation timeout %s", elapsed, operationTimeout)
	}

	// 超时后互斥必须释放，不能留下仍在占用初始化的工作。
	_, retryErr := bootstrap.Initialize(context.Background(), input)
	if errors.As(retryErr, &serviceErr) && serviceErr.Code == "setup_in_progress" {
		t.Fatalf("second Initialize() error = %#v, want released setup lock", retryErr)
	}
	if retryErr == nil {
		t.Fatal("second Initialize() error = nil, want bounded failure")
	}
}

func TestBootstrapActivateExistingReleasesStartupWhenDatabaseStallsAfterHandshake(t *testing.T) {
	host, port := stalledPostgresListener(t)
	const operationTimeout = 300 * time.Millisecond
	databaseURL := "postgres://emby_auto:fixture-password@" + net.JoinHostPort(host, strconv.Itoa(port)) + "/emby_auto?sslmode=disable"
	store := platformconfig.NewBootstrapStore(filepath.Join(t.TempDir(), "bootstrap.json"))
	passwords := NewPasswordHasher()
	passwordHash, err := passwords.Hash("fixture-password-123")
	if err != nil {
		t.Fatalf("hash fixture password: %v", err)
	}
	if err := store.WriteConfigured(platformconfig.BootstrapData{
		DatabaseURL:       databaseURL,
		AdminID:           uuid.New(),
		AdminUsername:     "setup-admin",
		AdminPasswordHash: passwordHash,
	}); err != nil {
		t.Fatalf("write bootstrap fixture: %v", err)
	}
	bootstrap := NewBootstrap(BootstrapOptions{
		Store:            store,
		Migrator:         database.NewMigratorWithConnectTimeout(operationTimeout),
		Passwords:        passwords,
		ConnectTimeout:   operationTimeout,
		OperationTimeout: operationTimeout,
	})

	started := time.Now()
	activated, err := bootstrap.ActivateExisting(context.Background())
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("ActivateExisting() error = nil, want bounded failure")
	}
	if activated {
		t.Fatal("ActivateExisting() activated = true, want not activated")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("ActivateExisting() took %s, want bounded by operation timeout %s", elapsed, operationTimeout)
	}
}
