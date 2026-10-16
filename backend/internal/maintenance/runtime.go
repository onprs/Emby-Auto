package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/config"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
	"github.com/onprs/emby-auto/backend/internal/repository"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type Scope struct {
	Config        config.Config
	Pool          *pgxpool.Pool
	Queries       *db.Queries
	Configuration *service.ConfigurationService
	ActorID       uuid.UUID
	lock          *repository.BackgroundRuntimeLock
	controller    *service.BackgroundRuntimeController
	transfers     *service.QBittorrentBackgroundTransfers
}

type RuntimeStatus struct {
	State                 domain.BackgroundRuntimeState    `json:"state"`
	Verified              bool                             `json:"verified"`
	DownloadTorrentHashes int                              `json:"downloadTorrentReferences"`
	Transfers             service.BackgroundTransferStatus `json:"transfers"`
}

func Open(ctx context.Context, containerHostAddress string) (*Scope, error) {
	if containerHostAddress != "" {
		address := net.ParseIP(containerHostAddress)
		if address == nil || !address.IsLoopback() {
			return nil, errors.New("容器服务替代地址必须是回环 IP")
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.DatabaseURL == "" || len(cfg.ConfigEncryptionKey) != 32 || cfg.HostControlExecutable == "" {
		return nil, errors.New("需要已完成初始化的运行配置及后台控制入口")
	}
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Scope, error) { pool.Close(); return nil, err }
	if err := database.RequireCurrentMigrations(ctx, pool); err != nil {
		return fail(err)
	}
	queries := db.New(pool)
	installation, err := queries.GetInstallationState(ctx)
	if err != nil || !installation.CompletedBy.Valid {
		return fail(errors.New("安装状态未完成"))
	}
	cipher, err := service.NewSecretCipher(cfg.ConfigEncryptionKey)
	if err != nil {
		return fail(err)
	}
	configuration := service.NewConfigurationService(repository.NewConfiguration(queries, database.NewTransactor(pool)), cipher)
	transfers := service.NewQBittorrentBackgroundTransfers(configuration, func(options qbittorrent.ClientOptions) (service.BackgroundTorrentClient, error) {
		if containerHostAddress != "" {
			var err error
			options.BaseURL, _, err = ReplaceURLHost(options.BaseURL, "host.docker.internal", containerHostAddress)
			if err != nil {
				return nil, err
			}
		}
		return qbittorrent.NewClient(options)
	})
	return &Scope{
		Config: cfg, Pool: pool, Queries: queries, Configuration: configuration,
		ActorID: repository.UUIDFromPG(installation.CompletedBy),
		lock:    repository.NewBackgroundRuntimeLock(pool), transfers: transfers,
		controller: service.NewBackgroundRuntimeController(cfg.HostControlExecutable, service.OSBackgroundRuntimeExecutor{}, transfers),
	}, nil
}

func (scope *Scope) Close() { scope.Pool.Close() }

func (scope *Scope) Runtime(ctx context.Context, state *domain.BackgroundRuntimeState) (RuntimeStatus, error) {
	unlock, err := scope.lock.Lock(ctx)
	if err != nil {
		return RuntimeStatus{}, err
	}
	defer unlock()
	var runtime domain.BackgroundRuntime
	if state == nil {
		runtime, err = scope.controller.Get(ctx)
	} else {
		runtime, err = scope.controller.Set(ctx, *state)
	}
	if err != nil {
		return RuntimeStatus{}, err
	}
	transfers, references, err := scope.inspectTransfers(ctx)
	if err != nil {
		if state != nil && *state == domain.BackgroundRuntimeRunning {
			scope.compensate(ctx)
		}
		return RuntimeStatus{}, errors.New("无法核实应用 torrent 状态")
	}
	status := RuntimeStatus{State: runtime.State, DownloadTorrentHashes: references, Transfers: transfers}
	status.Verified = transfers.Configured && references == len(transfers.Selected) &&
		((runtime.State == domain.BackgroundRuntimeStopped && transfers.Active == 0) ||
			(runtime.State == domain.BackgroundRuntimeRunning && transfers.RuntimeMarks == 0))
	if state != nil && !status.Verified {
		if *state == domain.BackgroundRuntimeRunning {
			scope.compensate(ctx)
		}
		return status, errors.New("后台状态尚未通过转换门禁")
	}
	return status, nil
}

func (scope *Scope) inspectTransfers(ctx context.Context) (service.BackgroundTransferStatus, int, error) {
	rows, err := scope.Queries.ListActiveDownloadHashes(ctx)
	if err != nil {
		return service.BackgroundTransferStatus{}, 0, err
	}
	seen := make(map[string]bool)
	hashes := make([]string, 0, len(rows))
	for _, hash := range rows {
		if hash == nil {
			continue
		}
		value := strings.ToLower(strings.TrimSpace(*hash))
		if value != "" && !seen[value] {
			seen[value] = true
			hashes = append(hashes, value)
		}
	}
	status, err := scope.transfers.Inspect(ctx, hashes...)
	return status, len(hashes), err
}

func (scope *Scope) compensate(ctx context.Context) {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	_, _ = scope.controller.Set(stopCtx, domain.BackgroundRuntimeStopped)
}

func (scope *Scope) WithStopped(ctx context.Context, action func() error) error {
	unlock, err := scope.lock.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	runtime, err := scope.controller.Get(ctx)
	if err != nil || runtime.State != domain.BackgroundRuntimeStopped {
		return errors.New("迁移要求 Worker 已停止")
	}
	transfers, references, err := scope.inspectTransfers(ctx)
	if err != nil || !transfers.Configured || transfers.Active != 0 || len(transfers.Selected) != references {
		return errors.New("迁移要求应用 torrent 已暂停并可核实")
	}
	return action()
}

func ReplaceURLHost(value, from, to string) (string, bool, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", false, errors.New("服务地址无效")
	}
	if !strings.EqualFold(parsed.Hostname(), from) {
		return value, false, nil
	}
	if port := parsed.Port(); port != "" {
		parsed.Host = net.JoinHostPort(to, port)
	} else if strings.Contains(to, ":") {
		parsed.Host = "[" + to + "]"
	} else {
		parsed.Host = to
	}
	return parsed.String(), true, nil
}

// WriteFailure 只输出稳定分类，阻止数据库和外部服务错误泄露运行配置。
func WriteFailure(output io.Writer, code string) {
	_ = json.NewEncoder(output).Encode(map[string]string{"error": code})
}

func WriteJSON(output io.Writer, value any) error {
	if err := json.NewEncoder(output).Encode(value); err != nil {
		return fmt.Errorf("无法输出维护结果")
	}
	return nil
}
