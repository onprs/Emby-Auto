package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
)

const backgroundTorrentRequestTimeout = 15 * time.Second

type BackgroundRuntimeExecutor interface {
	Run(context.Context, string, string) ([]byte, error)
}

type OSBackgroundRuntimeExecutor struct{}

func (OSBackgroundRuntimeExecutor) Run(ctx context.Context, executable, command string) ([]byte, error) {
	return exec.CommandContext(ctx, executable, command).Output()
}

type BackgroundTransferController interface {
	Pause(context.Context) error
	Resume(context.Context) error
}

type BackgroundTransferConfiguration interface {
	Load(context.Context) (domain.Configuration, error)
	ResolveSecret(context.Context, string) (string, error)
}

type BackgroundTorrentClient interface {
	Login(context.Context) error
	ListTorrents(context.Context, string) ([]qbittorrent.Torrent, error)
	AddTorrentTags(context.Context, []string, ...string) error
	RemoveTorrentTags(context.Context, []string, ...string) error
	StopTorrents(context.Context, []string) error
	ResumeTorrents(context.Context, []string) error
}

type BackgroundTorrentClientFactory func(qbittorrent.ClientOptions) (BackgroundTorrentClient, error)

type QBittorrentBackgroundTransfers struct {
	configuration BackgroundTransferConfiguration
	newClient     BackgroundTorrentClientFactory
}

func NewQBittorrentBackgroundTransfers(
	configuration BackgroundTransferConfiguration,
	newClient BackgroundTorrentClientFactory,
) *QBittorrentBackgroundTransfers {
	return &QBittorrentBackgroundTransfers{configuration: configuration, newClient: newClient}
}

func (transfers *QBittorrentBackgroundTransfers) Pause(ctx context.Context) error {
	client, configured, err := transfers.client(ctx)
	if err != nil || !configured {
		return err
	}
	torrents, err := client.ListTorrents(ctx, qbittorrent.ManagedCategory)
	if err != nil {
		return fmt.Errorf("list application torrents: %w", err)
	}

	newlyTagged := make([]string, 0, len(torrents))
	toStop := make([]string, 0, len(torrents))
	for _, torrent := range torrents {
		tagged := qbittorrent.TorrentHasTag(torrent, qbittorrent.RuntimePausedTag)
		if !tagged && qbittorrent.IsTorrentStopped(torrent) {
			continue
		}
		toStop = append(toStop, torrent.Hash)
		if !tagged {
			newlyTagged = append(newlyTagged, torrent.Hash)
		}
	}
	if len(toStop) == 0 {
		return nil
	}
	if len(newlyTagged) > 0 {
		if err := client.AddTorrentTags(ctx, newlyTagged, qbittorrent.RuntimePausedTag); err != nil {
			return fmt.Errorf("mark application torrents for runtime pause: %w", err)
		}
	}
	if err := client.StopTorrents(ctx, toStop); err != nil {
		return fmt.Errorf("stop application torrents: %w", err)
	}
	return nil
}

func (transfers *QBittorrentBackgroundTransfers) Resume(ctx context.Context) error {
	client, configured, err := transfers.client(ctx)
	if err != nil || !configured {
		return err
	}
	torrents, err := client.ListTorrents(ctx, qbittorrent.ManagedCategory)
	if err != nil {
		return fmt.Errorf("list application torrents: %w", err)
	}

	toResume := make([]string, 0, len(torrents))
	for _, torrent := range torrents {
		if qbittorrent.TorrentHasTag(torrent, qbittorrent.RuntimePausedTag) {
			toResume = append(toResume, torrent.Hash)
		}
	}
	if len(toResume) == 0 {
		return nil
	}
	if err := client.ResumeTorrents(ctx, toResume); err != nil {
		return fmt.Errorf("resume runtime-paused application torrents: %w", err)
	}
	if err := client.RemoveTorrentTags(ctx, toResume, qbittorrent.RuntimePausedTag); err != nil {
		return fmt.Errorf("clear application torrent runtime pause marks: %w", err)
	}
	return nil
}

func (transfers *QBittorrentBackgroundTransfers) client(ctx context.Context) (BackgroundTorrentClient, bool, error) {
	if transfers == nil || transfers.configuration == nil || transfers.newClient == nil {
		return nil, false, errors.New("background transfer control is not configured")
	}
	configuration, err := transfers.configuration.Load(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("load runtime configuration: %w", err)
	}
	settings := configuration.Settings.QBittorrent
	if strings.TrimSpace(settings.URL) == "" {
		return nil, false, nil
	}
	password, err := transfers.configuration.ResolveSecret(ctx, domain.SecretQBittorrentPassword)
	if err != nil {
		return nil, false, fmt.Errorf("resolve qBittorrent credentials: %w", err)
	}
	client, err := transfers.newClient(qbittorrent.ClientOptions{
		BaseURL:        settings.URL,
		Username:       settings.Username,
		Password:       password,
		RequestTimeout: backgroundTorrentRequestTimeout,
	})
	if err != nil {
		return nil, false, fmt.Errorf("create qBittorrent client: %w", err)
	}
	if err := client.Login(ctx); err != nil {
		return nil, false, fmt.Errorf("authenticate qBittorrent client: %w", err)
	}
	return client, true, nil
}

type BackgroundRuntimeController struct {
	mu         sync.Mutex
	executable string
	executor   BackgroundRuntimeExecutor
	transfers  BackgroundTransferController
}

func NewBackgroundRuntimeController(
	executable string,
	executor BackgroundRuntimeExecutor,
	transfers BackgroundTransferController,
) *BackgroundRuntimeController {
	return &BackgroundRuntimeController{
		executable: strings.TrimSpace(executable),
		executor:   executor,
		transfers:  transfers,
	}
}

func (controller *BackgroundRuntimeController) Get(ctx context.Context) (domain.BackgroundRuntime, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.run(ctx, "worker-status")
}

func (controller *BackgroundRuntimeController) Set(ctx context.Context, state domain.BackgroundRuntimeState) (domain.BackgroundRuntime, error) {
	if state != domain.BackgroundRuntimeRunning && state != domain.BackgroundRuntimeStopped {
		return domain.BackgroundRuntime{}, NewError(
			"invalid_background_runtime_state",
			"background runtime state must be running or stopped",
			ErrInvalidInput,
			map[string]any{"state": state},
		)
	}

	controller.mu.Lock()
	defer controller.mu.Unlock()

	if controller.transfers == nil {
		return domain.BackgroundRuntime{}, backgroundRuntimeUnavailable(
			errors.New("background transfer control is not configured"),
			"qbittorrent",
		)
	}
	if state == domain.BackgroundRuntimeStopped {
		runtime, err := controller.run(ctx, "worker-stop")
		if err != nil {
			return domain.BackgroundRuntime{}, err
		}
		if err := controller.transfers.Pause(ctx); err != nil {
			return domain.BackgroundRuntime{}, backgroundRuntimeUnavailable(err, "qbittorrent")
		}
		return runtime, nil
	}

	if err := controller.transfers.Resume(ctx); err != nil {
		return domain.BackgroundRuntime{}, backgroundRuntimeUnavailable(err, "qbittorrent")
	}
	runtime, err := controller.run(ctx, "worker-start")
	if err == nil {
		return runtime, nil
	}
	compensationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if compensationErr := controller.transfers.Pause(compensationCtx); compensationErr != nil {
		err = errors.Join(err, fmt.Errorf("re-pause application torrents after Worker start failed: %w", compensationErr))
	}
	return domain.BackgroundRuntime{}, err
}

func (controller *BackgroundRuntimeController) run(ctx context.Context, command string) (domain.BackgroundRuntime, error) {
	if controller.executable == "" || controller.executor == nil {
		return domain.BackgroundRuntime{}, backgroundRuntimeUnavailable(errors.New("host control executable is not configured"), "host_control")
	}
	output, err := controller.executor.Run(ctx, controller.executable, command)
	if err != nil {
		return domain.BackgroundRuntime{}, backgroundRuntimeUnavailable(fmt.Errorf("run %s: %w", command, err), "host_control")
	}
	if len(output) > 64 {
		return domain.BackgroundRuntime{}, backgroundRuntimeUnavailable(errors.New("host control output is too large"), "host_control")
	}
	state := domain.BackgroundRuntimeState(strings.TrimSpace(string(output)))
	switch state {
	case domain.BackgroundRuntimeRunning, domain.BackgroundRuntimeStopped, domain.BackgroundRuntimeTransitioning:
		return domain.BackgroundRuntime{State: state}, nil
	default:
		return domain.BackgroundRuntime{}, backgroundRuntimeUnavailable(fmt.Errorf("invalid host control state %q", state), "host_control")
	}
}

func backgroundRuntimeUnavailable(cause error, dependency string) error {
	return NewError(
		"background_runtime_unavailable",
		"background task control is unavailable",
		errors.Join(ErrUnavailable, cause),
		map[string]any{"dependency": dependency},
	)
}
