package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/qbittorrent"
)

const (
	backgroundTorrentHashA = "0123456789abcdef0123456789abcdef01234567"
	backgroundTorrentHashB = "abcdef0123456789abcdef0123456789abcdef01"
	backgroundTorrentHashC = "1111111111111111111111111111111111111111"
)

type backgroundRuntimeExecutorStub struct {
	outputs  map[string][]byte
	errors   map[string]error
	commands []string
	paths    []string
	events   *[]string
}

func (stub *backgroundRuntimeExecutorStub) Run(_ context.Context, executable, command string) ([]byte, error) {
	stub.paths = append(stub.paths, executable)
	stub.commands = append(stub.commands, command)
	if stub.events != nil {
		*stub.events = append(*stub.events, command)
	}
	return stub.outputs[command], stub.errors[command]
}

type backgroundTransferControllerStub struct {
	pauseErr  error
	resumeErr error
	paused    int
	resumed   int
	events    *[]string
}

func (stub *backgroundTransferControllerStub) Pause(context.Context) error {
	stub.paused++
	if stub.events != nil {
		*stub.events = append(*stub.events, "torrent-pause")
	}
	return stub.pauseErr
}

func (stub *backgroundTransferControllerStub) Resume(context.Context) error {
	stub.resumed++
	if stub.events != nil {
		*stub.events = append(*stub.events, "torrent-resume")
	}
	return stub.resumeErr
}

func TestBackgroundRuntimeControllerReadsAndSetsFixedCommands(t *testing.T) {
	executor := &backgroundRuntimeExecutorStub{outputs: map[string][]byte{
		"worker-status": []byte("stopped\n"),
		"worker-start":  []byte("running\n"),
		"worker-stop":   []byte("stopped\n"),
	}, errors: map[string]error{}}
	transfers := &backgroundTransferControllerStub{}
	controller := NewBackgroundRuntimeController("/app/bin/emby-auto-host-control", executor, transfers)

	status, err := controller.Get(context.Background())
	if err != nil || status.State != domain.BackgroundRuntimeStopped {
		t.Fatalf("Get() = %+v, %v", status, err)
	}
	status, err = controller.Set(context.Background(), domain.BackgroundRuntimeRunning)
	if err != nil || status.State != domain.BackgroundRuntimeRunning {
		t.Fatalf("Set(running) = %+v, %v", status, err)
	}
	status, err = controller.Set(context.Background(), domain.BackgroundRuntimeStopped)
	if err != nil || status.State != domain.BackgroundRuntimeStopped {
		t.Fatalf("Set(stopped) = %+v, %v", status, err)
	}
	if want := []string{"worker-status", "worker-start", "worker-stop"}; !reflect.DeepEqual(executor.commands, want) {
		t.Fatalf("commands = %v, want %v", executor.commands, want)
	}
	if transfers.resumed != 1 || transfers.paused != 1 {
		t.Fatalf("transfer calls = resumed %d, paused %d", transfers.resumed, transfers.paused)
	}
	for _, path := range executor.paths {
		if path != "/app/bin/emby-auto-host-control" {
			t.Fatalf("executable = %q", path)
		}
	}
}

func TestBackgroundRuntimeControllerOrdersTransfersAndCompensatesFailedStart(t *testing.T) {
	events := []string{}
	executor := &backgroundRuntimeExecutorStub{
		outputs: map[string][]byte{},
		errors:  map[string]error{"worker-start": errors.New("start failed")},
		events:  &events,
	}
	transfers := &backgroundTransferControllerStub{events: &events}
	controller := NewBackgroundRuntimeController("/control", executor, transfers)

	_, err := controller.Set(context.Background(), domain.BackgroundRuntimeRunning)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Set(running) error = %v, want unavailable", err)
	}
	if want := []string{"torrent-resume", "worker-start", "torrent-pause"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestBackgroundRuntimeControllerStopsWorkerBeforeTransfers(t *testing.T) {
	events := []string{}
	executor := &backgroundRuntimeExecutorStub{
		outputs: map[string][]byte{"worker-stop": []byte("stopped\n")},
		errors:  map[string]error{},
		events:  &events,
	}
	transfers := &backgroundTransferControllerStub{events: &events}
	controller := NewBackgroundRuntimeController("/control", executor, transfers)

	if _, err := controller.Set(context.Background(), domain.BackgroundRuntimeStopped); err != nil {
		t.Fatalf("Set(stopped) error = %v", err)
	}
	if want := []string{"worker-stop", "torrent-pause"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestBackgroundRuntimeControllerRejectsUnsupportedState(t *testing.T) {
	executor := &backgroundRuntimeExecutorStub{errors: map[string]error{}}
	controller := NewBackgroundRuntimeController("/app/bin/emby-auto-host-control", executor, &backgroundTransferControllerStub{})
	_, err := controller.Set(context.Background(), domain.BackgroundRuntimeState("restart"))
	if !errors.Is(err, ErrInvalidInput) || len(executor.commands) != 0 {
		t.Fatalf("Set(restart) error/commands = %v/%v", err, executor.commands)
	}
}

func TestBackgroundRuntimeControllerClassifiesUnavailableControl(t *testing.T) {
	tests := []struct {
		name       string
		controller *BackgroundRuntimeController
	}{
		{name: "not configured", controller: NewBackgroundRuntimeController("", &backgroundRuntimeExecutorStub{errors: map[string]error{}}, &backgroundTransferControllerStub{})},
		{name: "execution failed", controller: NewBackgroundRuntimeController("/control", &backgroundRuntimeExecutorStub{errors: map[string]error{"worker-status": errors.New("failed")}}, &backgroundTransferControllerStub{})},
		{name: "invalid state", controller: NewBackgroundRuntimeController("/control", &backgroundRuntimeExecutorStub{outputs: map[string][]byte{"worker-status": []byte("unknown\n")}, errors: map[string]error{}}, &backgroundTransferControllerStub{})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.controller.Get(context.Background())
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Get() error = %v, want unavailable", err)
			}
		})
	}
}

type backgroundTransferConfigurationStub struct {
	configuration domain.Configuration
	password      string
	err           error
}

func (stub *backgroundTransferConfigurationStub) Load(context.Context) (domain.Configuration, error) {
	return stub.configuration, stub.err
}

func (stub *backgroundTransferConfigurationStub) ResolveSecret(context.Context, string) (string, error) {
	return stub.password, stub.err
}

type backgroundTorrentClientStub struct {
	torrents []qbittorrent.Torrent
	added    [][]string
	removed  [][]string
	stopped  [][]string
	resumed  [][]string
	loggedIn int
	err      error
}

func (stub *backgroundTorrentClientStub) Login(context.Context) error {
	stub.loggedIn++
	return stub.err
}

func (stub *backgroundTorrentClientStub) ListTorrents(context.Context, string) ([]qbittorrent.Torrent, error) {
	return stub.torrents, stub.err
}

func (stub *backgroundTorrentClientStub) AddTorrentTags(_ context.Context, hashes []string, _ ...string) error {
	stub.added = append(stub.added, append([]string(nil), hashes...))
	return stub.err
}

func (stub *backgroundTorrentClientStub) RemoveTorrentTags(_ context.Context, hashes []string, _ ...string) error {
	stub.removed = append(stub.removed, append([]string(nil), hashes...))
	return stub.err
}

func (stub *backgroundTorrentClientStub) StopTorrents(_ context.Context, hashes []string) error {
	stub.stopped = append(stub.stopped, append([]string(nil), hashes...))
	return stub.err
}

func (stub *backgroundTorrentClientStub) ResumeTorrents(_ context.Context, hashes []string) error {
	stub.resumed = append(stub.resumed, append([]string(nil), hashes...))
	return stub.err
}

func configuredBackgroundTransfers(client *backgroundTorrentClientStub) *QBittorrentBackgroundTransfers {
	configuration := &backgroundTransferConfigurationStub{
		configuration: domain.Configuration{Settings: domain.RuntimeSettings{QBittorrent: domain.QBittorrentSettings{
			URL: "http://qb.test", Username: "worker",
		}}},
		password: "secret",
	}
	return NewQBittorrentBackgroundTransfers(configuration, func(qbittorrent.ClientOptions) (BackgroundTorrentClient, error) {
		return client, nil
	})
}

func TestQBittorrentBackgroundTransfersPausesOnlyRuntimeOwnedActivity(t *testing.T) {
	client := &backgroundTorrentClientStub{torrents: []qbittorrent.Torrent{
		{Hash: backgroundTorrentHashA, State: "downloading"},
		{Hash: backgroundTorrentHashB, State: "pausedDL"},
		{Hash: backgroundTorrentHashC, State: "stoppedDL", Tags: qbittorrent.RuntimePausedTag},
	}}
	transfers := configuredBackgroundTransfers(client)

	if err := transfers.Pause(context.Background()); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if want := [][]string{{backgroundTorrentHashA}}; !reflect.DeepEqual(client.added, want) {
		t.Fatalf("tagged hashes = %v, want %v", client.added, want)
	}
	if want := [][]string{{backgroundTorrentHashA, backgroundTorrentHashC}}; !reflect.DeepEqual(client.stopped, want) {
		t.Fatalf("stopped hashes = %v, want %v", client.stopped, want)
	}
}

func TestQBittorrentBackgroundTransfersResumesOnlyRuntimeTaggedTorrents(t *testing.T) {
	client := &backgroundTorrentClientStub{torrents: []qbittorrent.Torrent{
		{Hash: backgroundTorrentHashA, State: "stoppedDL", Tags: qbittorrent.RuntimePausedTag},
		{Hash: backgroundTorrentHashB, State: "pausedDL"},
	}}
	transfers := configuredBackgroundTransfers(client)

	if err := transfers.Resume(context.Background()); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	want := [][]string{{backgroundTorrentHashA}}
	if !reflect.DeepEqual(client.resumed, want) || !reflect.DeepEqual(client.removed, want) {
		t.Fatalf("resume/remove hashes = %v/%v, want %v", client.resumed, client.removed, want)
	}
}

func TestQBittorrentBackgroundTransfersNoopsWhenUnconfigured(t *testing.T) {
	factoryCalled := false
	transfers := NewQBittorrentBackgroundTransfers(
		&backgroundTransferConfigurationStub{},
		func(qbittorrent.ClientOptions) (BackgroundTorrentClient, error) {
			factoryCalled = true
			return &backgroundTorrentClientStub{}, nil
		},
	)
	if err := transfers.Pause(context.Background()); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if err := transfers.Resume(context.Background()); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if factoryCalled {
		t.Fatal("qBittorrent client factory was called without a configured URL")
	}
}
