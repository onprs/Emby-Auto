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
	created       service.AgentResolutionCommandResult
	retryCalls    int
	retryErr      error
}

func (stub *subtitleMatchAgentStub) CapabilityEnabled(context.Context, domain.AgentCapability) (bool, error) {
	return stub.enabled, stub.enabledErr
}

func (stub *subtitleMatchAgentStub) CreateAutomatic(_ context.Context, input service.AutomaticAgentResolutionRequest) (service.AgentResolutionCommandResult, error) {
	stub.createCalls++
	stub.createdScopes = append(stub.createdScopes, input.ResourceID)
	return stub.created, stub.createErr
}

func (stub *subtitleMatchAgentStub) RetryAutomatic(context.Context, uuid.UUID, int) (service.AgentResolutionCommandResult, error) {
	stub.retryCalls++
	return service.AgentResolutionCommandResult{}, stub.retryErr
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

func TestSubtitlePrepareKeepsSameBasenameExternalCandidatesDistinct(t *testing.T) {
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	for _, directory := range []string{"first", "second"} {
		if err := os.MkdirAll(filepath.Join(downloadRoot, directory), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640); err != nil {
		t.Fatal(err)
	}
	firstID := uuid.MustParse("74000000-0000-4000-8000-000000000041")
	secondID := uuid.MustParse("74000000-0000-4000-8000-000000000042")
	taskID := uuid.MustParse("74000000-0000-4000-8000-000000000043")
	store := &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E01", SubtitleName: "Show - S01E01.ass"},
		ExternalSubtitles: []domain.TaskExternalSubtitle{
			{SourceFileID: firstID, RelativePath: "first/episode.zh-Hans.ass", Language: "zh-Hans", Format: domain.SubtitleASS},
			{SourceFileID: secondID, RelativePath: "second/episode.zh-Hans.ass", Language: "zh-Hans", Format: domain.SubtitleASS},
		},
	}, scopeID: uuid.New()}
	tools := &mediaToolsStub{probe: func(string) (domain.MediaProbe, error) {
		return domain.MediaProbe{}, nil
	}}
	agent := &subtitleMatchAgentStub{enabled: true}
	if err := NewSubtitlePrepareHandler(
		mediaTestConfiguration(filepath.Join(root, "staging")), tools, store, agent,
	).Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(store.scopeCandidates) != 2 {
		t.Fatalf("scope candidates = %#v, want two", store.scopeCandidates)
	}
	if store.scopeCandidates[0].CandidateID != "external:"+firstID.String() ||
		store.scopeCandidates[1].CandidateID != "external:"+secondID.String() {
		t.Fatalf("candidate identities = %#v", store.scopeCandidates)
	}
}

func ambiguousSubtitlePrepareFixture(t *testing.T, scopeID uuid.UUID) (tools *mediaToolsStub, store *subtitleStoreStub, taskID uuid.UUID, stagingRoot string) {
	t.Helper()
	root := t.TempDir()
	downloadRoot := filepath.Join(root, "download")
	stagingRoot = filepath.Join(root, "staging")
	if err := os.MkdirAll(downloadRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloadRoot, "episode.mkv"), []byte("video"), 0o640); err != nil {
		t.Fatal(err)
	}
	taskID = uuid.New()
	store = &subtitleStoreStub{command: domain.TaskMediaCommand{
		TaskID: taskID, State: domain.TaskProcessing, SubtitleState: domain.SubtitleExtractingConverting,
		SavePath: downloadRoot, SourceVideoFileID: uuid.New(), SourceVideoRelativePath: "episode.mkv",
		Names: domain.EpisodeFileNames{BaseName: "Show - S01E01", SubtitleName: "Show - S01E01.ass"},
	}, scopeID: scopeID}
	tools = &mediaToolsStub{probe: func(string) (domain.MediaProbe, error) {
		return domain.MediaProbe{Streams: []domain.MediaStreamProbe{
			{Index: 2, Type: "subtitle", Codec: "ass", Language: "chi", Title: "简日双语"},
			{Index: 3, Type: "subtitle", Codec: "ass", Language: "chi", Title: "繁日雙語"},
		}}, nil
	}}
	return tools, store, taskID, stagingRoot
}

func TestSubtitlePrepareResumesRetryableAgentFailure(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	agent := &subtitleMatchAgentStub{enabled: true, created: service.AgentResolutionCommandResult{Resolution: domain.AgentResolution{
		Status: domain.AgentResolutionFailed, Trigger: "automatic", ErrorCode: "agent_request_timeout", Proposal: []byte(`{}`),
	}}}
	store.schedulingState = domain.SubtitleVideoMatchSchedulingState{ScopeID: scopeID, ScopeStatus: "pending", TaskVersion: 1}
	store.selection = domain.SubtitleMatchSelection{}
	if err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if agent.retryCalls != 1 {
		t.Fatalf("RetryAutomatic() calls = %d, want 1", agent.retryCalls)
	}
}

func TestSubtitlePrepareFailsTaskWhenAgentFailureIsNotRetryable(t *testing.T) {
	scopeID := uuid.New()
	tools, store, taskID, stagingRoot := ambiguousSubtitlePrepareFixture(t, scopeID)
	agent := &subtitleMatchAgentStub{enabled: true, created: service.AgentResolutionCommandResult{Resolution: domain.AgentResolution{
		Status: domain.AgentResolutionFailed, Trigger: "automatic", ErrorCode: "agent_tool_scope_violation", Proposal: []byte(`{}`),
	}}}
	err := NewSubtitlePrepareHandler(mediaTestConfiguration(stagingRoot), tools, store, agent).Handle(context.Background(), domain.Operation{ID: uuid.New(), ResourceType: "episode_task", ResourceID: taskID})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "agent_subtitle_match_failed" || failure.Retryable {
		t.Fatalf("Handle() error = %v, want permanent agent_subtitle_match_failed", err)
	}
	if len(store.expiredScopes) != 1 || store.expiredScopes[0] != scopeID {
		t.Fatalf("expired scopes = %#v, want %s", store.expiredScopes, scopeID)
	}
	if agent.retryCalls != 0 {
		t.Fatalf("RetryAutomatic() calls = %d, want 0", agent.retryCalls)
	}
}

func TestFilterPlansBySelectionReplaysLegacyExternalCandidateByPersistedPath(t *testing.T) {
	root := t.TempDir()
	firstID, secondID := uuid.New(), uuid.New()
	firstPath := filepath.Join(root, "first", "episode.zh-Hans.ass")
	secondPath := filepath.Join(root, "second", "episode.zh-Hans.ass")
	plans := []domain.SubtitlePlan{
		{Source: domain.SubtitleSourceExternal, SourceFileID: firstID, InputPath: firstPath},
		{Source: domain.SubtitleSourceExternal, SourceFileID: secondID, InputPath: secondPath},
	}
	legacyID := "file:episode.zh-Hans.ass"
	selected := filterPlansBySelection(plans, domain.SubtitleMatchSelection{CandidateID: legacyID, Path: secondPath})
	if len(selected) != 1 || selected[0].SourceFileID != secondID {
		t.Fatalf("legacy selection = %#v, want second persisted path", selected)
	}
	if ambiguous := filterPlansBySelection(plans, domain.SubtitleMatchSelection{CandidateID: legacyID}); len(ambiguous) != 0 {
		t.Fatalf("legacy selection without persisted path = %#v, want ambiguity rejection", ambiguous)
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
	}, selection: domain.SubtitleMatchSelection{CandidateID: "stream:3"}}
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
