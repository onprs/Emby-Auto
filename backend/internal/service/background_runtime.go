package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
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

type BackgroundRuntimeLocker interface {
	Lock(context.Context) (func(), error)
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

type BackgroundTorrentEvidence struct {
	Digest        string  `json:"digest"`
	State         string  `json:"state"`
	Progress      float64 `json:"progress"`
	RuntimeMarked bool    `json:"runtimeMarked"`
}

type BackgroundTransferStatus struct {
	Configured   bool                        `json:"configured"`
	Managed      int                         `json:"managed"`
	Active       int                         `json:"active"`
	RuntimeMarks int                         `json:"runtimeMarks"`
	Selected     []BackgroundTorrentEvidence `json:"selected,omitempty"`
}

func (transfers *QBittorrentBackgroundTransfers) Inspect(ctx context.Context, hashes ...string) (BackgroundTransferStatus, error) {
	client, configured, err := transfers.client(ctx)
	status := BackgroundTransferStatus{Configured: configured}
	if err != nil || !configured {
		return status, err
	}
	torrents, err := client.ListTorrents(ctx, qbittorrent.ManagedCategory)
	if err != nil {
		return status, err
	}
	selected := make(map[string]bool, len(hashes))
	for _, hash := range hashes {
		selected[strings.ToLower(hash)] = true
	}
	for _, torrent := range torrents {
		status.Managed++
		if !qbittorrent.IsTorrentStopped(torrent) {
			status.Active++
		}
		if qbittorrent.TorrentHasTag(torrent, qbittorrent.RuntimePausedTag) {
			status.RuntimeMarks++
		}
		if selected[strings.ToLower(torrent.Hash)] {
			digest := sha256.Sum256([]byte(strings.ToLower(torrent.Hash)))
			status.Selected = append(status.Selected, BackgroundTorrentEvidence{
				Digest: hex.EncodeToString(digest[:]), State: backgroundTorrentEvidenceState(torrent.State), Progress: torrent.Progress,
				RuntimeMarked: qbittorrent.TorrentHasTag(torrent, qbittorrent.RuntimePausedTag),
			})
		}
	}
	return status, nil
}

func backgroundTorrentEvidenceState(state string) string {
	switch state {
	case "error", "missingFiles", "uploading", "pausedUP", "queuedUP", "stalledUP", "checkingUP", "forcedUP",
		"allocating", "downloading", "metaDL", "pausedDL", "queuedDL", "stalledDL", "checkingDL", "forcedDL",
		"checkingResumeData", "moving", "stoppedDL", "stoppedUP":
		return state
	default:
		return "unknown"
	}
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
	return waitForBackgroundTorrents(ctx, client, toStop, true)
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
	if err := waitForBackgroundTorrents(ctx, client, toResume, false); err != nil {
		return err
	}
	if err := client.RemoveTorrentTags(ctx, toResume, qbittorrent.RuntimePausedTag); err != nil {
		return fmt.Errorf("clear application torrent runtime pause marks: %w", err)
	}
	return nil
}

func waitForBackgroundTorrents(ctx context.Context, client BackgroundTorrentClient, hashes []string, stopped bool) error {
	waitCtx, cancel := context.WithTimeout(ctx, backgroundTorrentRequestTimeout)
	defer cancel()
	selected := make(map[string]bool, len(hashes))
	for _, hash := range hashes {
		selected[strings.ToLower(strings.TrimSpace(hash))] = true
	}
	for {
		torrents, err := client.ListTorrents(waitCtx, qbittorrent.ManagedCategory)
		if err != nil {
			return errors.New("无法核实应用 torrent 的运行状态")
		}
		pending := false
		found := make(map[string]bool, len(selected))
		for _, torrent := range torrents {
			hash := strings.ToLower(strings.TrimSpace(torrent.Hash))
			if !selected[hash] {
				continue
			}
			found[hash] = true
			switch backgroundTorrentEvidenceState(torrent.State) {
			case "unknown", "error", "missingFiles":
				return errors.New("无法确认应用 torrent 的有效状态")
			}
			if qbittorrent.IsTorrentStopped(torrent) != stopped {
				pending = true
			}
		}
		if len(found) != len(selected) {
			return errors.New("无法定位待转换的应用 torrent")
		}
		if !pending {
			return nil
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return errors.New("等待应用 torrent 状态收敛超时")
		case <-timer.C:
		}
	}
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
	gate       chan struct{}
	executable string
	executor   BackgroundRuntimeExecutor
	transfers  BackgroundTransferController
	locker     BackgroundRuntimeLocker
}

func NewBackgroundRuntimeController(
	executable string,
	executor BackgroundRuntimeExecutor,
	transfers BackgroundTransferController,
) *BackgroundRuntimeController {
	return &BackgroundRuntimeController{
		gate:       make(chan struct{}, 1),
		executable: strings.TrimSpace(executable),
		executor:   executor,
		transfers:  transfers,
	}
}

func (controller *BackgroundRuntimeController) WithLock(locker BackgroundRuntimeLocker) *BackgroundRuntimeController {
	controller.locker = locker
	return controller
}

func (controller *BackgroundRuntimeController) lockRuntime(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, backgroundRuntimeUnavailable(err, "runtime_lock")
	}
	select {
	case controller.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, backgroundRuntimeUnavailable(ctx.Err(), "runtime_lock")
	}
	release := func() { <-controller.gate }
	if controller.locker == nil {
		return release, nil
	}
	unlock, err := controller.locker.Lock(ctx)
	if err != nil {
		release()
		return nil, backgroundRuntimeUnavailable(err, "runtime_lock")
	}
	return func() {
		defer release()
		unlock()
	}, nil
}

func (controller *BackgroundRuntimeController) Get(ctx context.Context) (domain.BackgroundRuntime, error) {
	unlock, err := controller.lockRuntime(ctx)
	if err != nil {
		return domain.BackgroundRuntime{}, err
	}
	defer unlock()
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

	unlock, err := controller.lockRuntime(ctx)
	if err != nil {
		return domain.BackgroundRuntime{}, err
	}
	defer unlock()

	if controller.transfers == nil {
		return domain.BackgroundRuntime{}, backgroundRuntimeUnavailable(
			errors.New("background transfer control is not configured"),
			"qbittorrent",
		)
	}
	if state == domain.BackgroundRuntimeStopped {
		return controller.stop(ctx)
	}

	var runtime domain.BackgroundRuntime
	if resumeErr := controller.transfers.Resume(ctx); resumeErr != nil {
		err = backgroundRuntimeUnavailable(resumeErr, "qbittorrent")
	} else {
		runtime, err = controller.run(ctx, "worker-start")
		if err == nil && runtime.State != domain.BackgroundRuntimeRunning {
			err = backgroundRuntimeUnavailable(errors.New("未确认 Worker 运行"), "host_control")
		}
	}
	if err == nil {
		return runtime, nil
	}
	// 恢复失败可能已产生副作用，必须先停 Worker，再重新标记并暂停 torrent。
	compensationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	if _, compensationErr := controller.stop(compensationCtx); compensationErr != nil {
		err = errors.Join(err, compensationErr)
	}
	return domain.BackgroundRuntime{}, err
}

func (controller *BackgroundRuntimeController) stop(ctx context.Context) (domain.BackgroundRuntime, error) {
	runtime, err := controller.run(ctx, "worker-stop")
	if err != nil {
		return domain.BackgroundRuntime{}, err
	}
	if runtime.State != domain.BackgroundRuntimeStopped {
		return domain.BackgroundRuntime{}, backgroundRuntimeUnavailable(errors.New("未确认 Worker 停止"), "host_control")
	}
	if err := controller.transfers.Pause(ctx); err != nil {
		return domain.BackgroundRuntime{}, backgroundRuntimeUnavailable(err, "qbittorrent")
	}
	return runtime, nil
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
