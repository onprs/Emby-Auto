package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

type subtitleMatchAgentStub struct {
	enabled       bool
	enabledErr    error
	createErr     error
	createCalls   int
	createdScopes []uuid.UUID
}

func (stub *subtitleMatchAgentStub) CapabilityEnabled(context.Context, domain.AgentCapability) (bool, error) {
	return stub.enabled, stub.enabledErr
}

func (stub *subtitleMatchAgentStub) CreateAutomatic(_ context.Context, input service.AutomaticAgentResolutionRequest) (service.AgentResolutionCommandResult, error) {
	stub.createCalls++
	stub.createdScopes = append(stub.createdScopes, input.ResourceID)
	return service.AgentResolutionCommandResult{}, stub.createErr
}

func TestSubtitlePrepareSchedulesAgentWhenAmbiguousAndEnabled(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	_ = os.MkdirAll(downloadRoot, 0o750)
	_ = os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640)
	taskID := uuid.MustParse("74000000-0000-4000-8000-000000000031")
	scopeID := uuid.MustParse("74000000-0000-4000-8000-000000000032")
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E01", SubtitleName: "Show - S01E01.ass"},
	}, scopeID: scopeID}
	tools := &mediaToolsStub{probe: func(string) (domain.MediaProbe, error) {
		return domain.MediaProbe{Streams: []domain.MediaStreamProbe{
			{Index: 2, Type: "subtitle", Codec: "ass", Language: "chi", Title: "简日双语"},
			{Index: 3, Type: "subtitle", Codec: "ass", Language: "chi", Title: "繁日雙語"},
		}}, nil
	}}
	agent := &subtitleMatchAgentStub{enabled: true}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(filepath.Join(root, "staging")), tools, store, agent).Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if store.scopeCalls != 1 || agent.createCalls != 1 || len(agent.createdScopes) != 1 || agent.createdScopes[0] != scopeID {
		t.Fatalf("scope calls/create = %d/%d/%v", store.scopeCalls, agent.createCalls, agent.createdScopes)
	}
	if store.completion.TaskID != uuid.Nil {
		t.Fatalf("subtitle should not be committed while awaiting Agent selection: %#v", store.completion)
	}
}

func TestSubtitlePrepareUsesAppliedAgentSelection(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	stagingRoot := filepath.Join(root, "staging")
	_ = os.MkdirAll(downloadRoot, 0o750)
	_ = os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640)
	taskID := uuid.MustParse("74000000-0000-4000-8000-000000000033")
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E01", SubtitleName: "Show - S01E01.ass"},
	}, selection: "stream:3"}
	traditional := subtitleContentFixture("我們已經決定繼續學習繁體中文。")
	tools := &mediaToolsStub{
		probe: func(string) (domain.MediaProbe, error) {
			return domain.MediaProbe{Streams: []domain.MediaStreamProbe{
				{Index: 2, Type: "subtitle", Codec: "ass", Language: "chi", Title: "简日双语"},
				{Index: 3, Type: "subtitle", Codec: "ass", Language: "chi", Title: "繁日雙語"},
			}}, nil
		},
		run: func(args []string) ([]byte, error) {
			if args[4] == "0:3" {
				return traditional, nil
			}
			return nil, errors.New("unexpected stream " + args[4])
		},
	}
	agent := &subtitleMatchAgentStub{enabled: true}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if store.scopeCalls != 0 || agent.createCalls != 0 {
		t.Fatalf("agent should not re-schedule when a selection exists: scope=%d create=%d", store.scopeCalls, agent.createCalls)
	}
	if store.completion.Metadata["languageEvidence"] != domain.SubtitleEvidenceTraditional {
		t.Fatalf("applied selection should pick the traditional candidate: %#v", store.completion.Metadata)
	}
}
