package service

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	platformconfig "github.com/onprs/emby-auto/backend/internal/platform/config"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
)

// silentPostgresListener 接受 TCP 连接但从不回复，模拟不响应的数据库目标。
func silentPostgresListener(t *testing.T) (host string, port int) {
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
			// 保持连接打开且不发送任何字节，直到测试结束关闭监听器。
			go func() {
				<-done
				_ = connection.Close()
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
	})
	addr := listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func TestBootstrapInitializeReleasesSetupLockWhenDatabaseNeverResponds(t *testing.T) {
	host, port := silentPostgresListener(t)
	const connectTimeout = 200 * time.Millisecond
	bootstrap := NewBootstrap(BootstrapOptions{
		Store:          platformconfig.NewBootstrapStore(filepath.Join(t.TempDir(), "bootstrap.json")),
		Migrator:       database.NewMigratorWithConnectTimeout(connectTimeout),
		Passwords:      NewPasswordHasher(),
		ConnectTimeout: connectTimeout,
	})
	input := domain.InitializeSetup{
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

	started := time.Now()
	_, err := bootstrap.Initialize(context.Background(), input)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Initialize() error = nil, want unavailable database error")
	}
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "service_unavailable" || serviceErr.Details["dependency"] != "postgresql" {
		t.Fatalf("Initialize() error = %#v, want postgresql service_unavailable", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Initialize() took %s, want bounded by connect timeout %s", elapsed, connectTimeout)
	}

	// 失败后初始化标志必须释放，随后的请求仍会真正尝试连接而不是返回 setup_in_progress。
	_, retryErr := bootstrap.Initialize(context.Background(), input)
	if errors.As(retryErr, &serviceErr) && serviceErr.Code == "setup_in_progress" {
		t.Fatalf("second Initialize() error = %#v, want released setup lock", retryErr)
	}
	if retryErr == nil {
		t.Fatal("second Initialize() error = nil, want unavailable database error")
	}
}

// blockingMigrator 模拟已完成连接但永不返回查询响应的数据库依赖。
type blockingMigrator struct{}

func (blockingMigrator) Migrate(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestBootstrapInitializeReleasesSetupLockWhenDependencyStalls(t *testing.T) {
	bootstrap := NewBootstrap(BootstrapOptions{
		Store:            platformconfig.NewBootstrapStore(filepath.Join(t.TempDir(), "bootstrap.json")),
		Migrator:         blockingMigrator{},
		Passwords:        NewPasswordHasher(),
		OperationTimeout: 50 * time.Millisecond,
	})
	input := domain.InitializeSetup{
		Database: &domain.SetupDatabase{
			Host: "127.0.0.1", Port: 5432, Database: "emby_auto", Username: "emby_auto",
			Password: "fixture-password", SSLMode: "disable",
		},
		AdministratorUsername: "setup-admin",
		AdministratorPassword: "fixture-password-123",
	}

	started := time.Now()
	_, err := bootstrap.Initialize(context.Background(), input)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("Initialize() error = nil, want setup timeout")
	}
	var serviceErr *Error
	if !errors.As(err, &serviceErr) || serviceErr.Code != "service_unavailable" {
		t.Fatalf("Initialize() error = %#v, want service_unavailable", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Initialize() took %s, want bounded by setup timeout", elapsed)
	}

	// 超时后初始化标志必须释放，下一次请求仍会真正尝试而不是返回 setup_in_progress。
	_, retryErr := bootstrap.Initialize(context.Background(), input)
	if errors.As(retryErr, &serviceErr) && serviceErr.Code == "setup_in_progress" {
		t.Fatalf("second Initialize() error = %#v, want released setup lock", retryErr)
	}
	if retryErr == nil {
		t.Fatal("second Initialize() error = nil, want setup timeout")
	}
}

func TestBootstrapDatabaseInstallationStateHonorsConnectTimeout(t *testing.T) {
	host, port := silentPostgresListener(t)
	const connectTimeout = 200 * time.Millisecond
	bootstrap := NewBootstrap(BootstrapOptions{ConnectTimeout: connectTimeout})
	databaseURL := "postgres://emby_auto:fixture-password@" + net.JoinHostPort(host, strconv.Itoa(port)) + "/emby_auto?sslmode=disable"

	started := time.Now()
	_, _, err := bootstrap.databaseInstallationState(context.Background(), databaseURL)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("databaseInstallationState() error = nil, want connection error")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("databaseInstallationState() took %s, want bounded by connect timeout %s", elapsed, connectTimeout)
	}
}
