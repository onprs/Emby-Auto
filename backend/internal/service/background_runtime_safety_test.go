package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
)

type blockingBackgroundExecutor struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (executor *blockingBackgroundExecutor) Run(ctx context.Context, _, _ string) ([]byte, error) {
	if executor.calls.Add(1) == 1 {
		close(executor.entered)
	}
	select {
	case <-executor.release:
		return []byte("stopped\n"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestBackgroundRuntimeCancelsQueuedRequestWithoutExecutingIt(t *testing.T) {
	executor := &blockingBackgroundExecutor{entered: make(chan struct{}), release: make(chan struct{})}
	controller := NewBackgroundRuntimeController("/host-control", executor, nil)
	first := make(chan error, 1)
	go func() { _, err := controller.Get(context.Background()); first <- err }()
	defer func() {
		close(executor.release)
		if err := <-first; err != nil {
			t.Errorf("初始状态读取失败：%v", err)
		}
	}()
	<-executor.entered
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { _, err := controller.Get(ctx); second <- err }()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) || executor.calls.Load() != 1 {
			t.Fatalf("取消排队请求仍然执行了控制命令：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("取消请求仍在等待前一项状态操作")
	}
}

func TestBackgroundRuntimeControllerCompensatesPartialResumeBeforeReturning(t *testing.T) {
	order := []string{}
	executor := &backgroundRuntimeExecutorStub{
		outputs: map[string][]byte{"worker-stop": []byte("stopped\n")}, errors: map[string]error{}, events: &order,
	}
	transfers := &backgroundTransferControllerStub{events: &order, resumeErr: errors.New("部分 torrent 已恢复后请求失败")}
	controller := NewBackgroundRuntimeController("/host-control", executor, transfers)
	if _, err := controller.Set(context.Background(), domain.BackgroundRuntimeRunning); err == nil {
		t.Fatal("恢复失败后仍然返回成功")
	}
	want := []string{"torrent-resume", "worker-stop", "torrent-pause"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("补偿顺序 = %v，期望 %v", order, want)
	}
}

func TestBackgroundRuntimeControllerRejectsUnconfirmedStopWithoutPausingTorrents(t *testing.T) {
	order := []string{}
	executor := &backgroundRuntimeExecutorStub{
		outputs: map[string][]byte{"worker-stop": []byte("running\n")}, errors: map[string]error{}, events: &order,
	}
	controller := NewBackgroundRuntimeController("/host-control", executor, &backgroundTransferControllerStub{events: &order})
	if _, err := controller.Set(context.Background(), domain.BackgroundRuntimeStopped); err == nil {
		t.Fatal("Worker 仍在运行时确认了停止状态")
	}
	if !reflect.DeepEqual(order, []string{"worker-stop"}) {
		t.Fatalf("停止顺序 = %v", order)
	}
}

func TestBackgroundRuntimeControllerRejectsUnexpectedStartStateAndCompensates(t *testing.T) {
	order := []string{}
	executor := &backgroundRuntimeExecutorStub{
		outputs: map[string][]byte{"worker-start": []byte("stopped\n"), "worker-stop": []byte("stopped\n")},
		errors:  map[string]error{}, events: &order,
	}
	controller := NewBackgroundRuntimeController("/host-control", executor, &backgroundTransferControllerStub{events: &order})
	if _, err := controller.Set(context.Background(), domain.BackgroundRuntimeRunning); err == nil {
		t.Fatal("Worker 未确认运行时返回了成功")
	}
	want := []string{"torrent-resume", "worker-start", "worker-stop", "torrent-pause"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("补偿顺序 = %v，期望 %v", order, want)
	}
}

func TestBackgroundTransferInspectionRedactsSelectedDownload(t *testing.T) {
	client := &backgroundTorrentClientStub{torrents: []qbittorrent.Torrent{
		{Hash: backgroundTorrentHashA, Name: "不应输出的标题", State: "不应输出的状态响应", Progress: 0.5, ContentPath: "/private/source.mkv", Tags: qbittorrent.RuntimePausedTag},
		{Hash: backgroundTorrentHashB, State: "stoppedUP", Progress: 1},
	}}
	status, err := configuredBackgroundTransfers(client).Inspect(context.Background(), backgroundTorrentHashA)
	if err != nil || status.Managed != 2 || status.Active != 1 || status.RuntimeMarks != 1 || len(status.Selected) != 1 {
		t.Fatalf("下载检查 = %#v，%v", status, err)
	}
	selected := status.Selected[0]
	if selected.State != "unknown" || selected.Progress != 0.5 || !selected.RuntimeMarked || len(selected.Digest) != 64 {
		t.Fatalf("下载证据 = %#v", selected)
	}
	body, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{backgroundTorrentHashA, "不应输出", "/private/source.mkv"} {
		if strings.Contains(string(body), private) {
			t.Fatal("下载检查暴露了原始 Provider 信息")
		}
	}
}

func TestBackgroundTorrentPauseWaitsForTheActualStoppedState(t *testing.T) {
	client := &backgroundTorrentClientStub{inertStop: true, torrents: []qbittorrent.Torrent{
		{Hash: backgroundTorrentHashA, State: "downloading"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if err := configuredBackgroundTransfers(client).Pause(ctx); err == nil {
		t.Fatal("只有停止请求成功，torrent 仍在运行时却通过了门禁")
	}
	if !qbittorrent.TorrentHasTag(client.torrents[0], qbittorrent.RuntimePausedTag) {
		t.Fatal("暂停失败后丢失了恢复标记")
	}
}

func TestBackgroundTorrentResumeKeepsMarksUntilRunningIsConfirmed(t *testing.T) {
	client := &backgroundTorrentClientStub{inertStart: true, torrents: []qbittorrent.Torrent{
		{Hash: backgroundTorrentHashA, State: "stoppedDL", Tags: qbittorrent.RuntimePausedTag},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	if err := configuredBackgroundTransfers(client).Resume(ctx); err == nil {
		t.Fatal("torrent 尚未恢复时却通过了门禁")
	}
	if len(client.removed) != 0 || !qbittorrent.TorrentHasTag(client.torrents[0], qbittorrent.RuntimePausedTag) {
		t.Fatal("torrent 尚未恢复时移除了恢复标记")
	}
}
