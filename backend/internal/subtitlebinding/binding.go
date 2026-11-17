package subtitlebinding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

const Version = 1

const (
	PhaseStarted = "started"
	PhaseBegun   = "begun"
	PhaseScope   = "scope"
)

var ErrUnsupported = errors.New("unsupported subtitle binding")

// AttemptHandle 是 domain.SubtitleAttemptHandle 的兼容别名；保持现有调用方签名。
type AttemptHandle = domain.SubtitleAttemptHandle

type Candidate struct {
	CandidateID string
	Source      string
	StreamIndex int
	Format      string
	Language    string
	Title       string
	Path        string
}

type Content struct {
	TaskID                  uuid.UUID
	SeriesTitle             string
	TargetSeason            int
	TargetEpisode           int
	TargetTitle             string
	SourceVideoRelativePath string
	Candidates              []Candidate
}

type Observation struct {
	ScopePresent        bool
	ScopeID             uuid.UUID
	ScopeStatus         string
	OwnerCount          int
	SelectedCandidateID string
	AppliedResolutionID uuid.UUID
	ContentHash         string
	TaskVersion         int
	TaskState           string
	SubtitleState       string
	VideoState          string
	FailureStage        string
}

type TaskReceipt struct {
	Attempt       int
	AttemptID     uuid.UUID
	TaskVersion   int
	TaskState     string
	SubtitleState string
	VideoState    string
	FailureStage  string
}

type ScopeReceipt struct {
	Attempt            int
	AttemptID          uuid.UUID
	ScopeID            uuid.UUID
	ScopeStatus        string
	ContentHash        string
	CreatedByOperation bool
	TaskVersion        int
	TaskState          string
	SubtitleState      string
	VideoState         string
	FailureStage       string
}

type Binding struct {
	Version               int           `json:"version"`
	OperationID           uuid.UUID     `json:"operationId"`
	Attempt               int           `json:"attempt"`
	AttemptID             uuid.UUID     `json:"attemptId"`
	OperationAttemptCount int           `json:"operationAttemptCount"`
	Phase                 string        `json:"phase"`
	Start                 Observation   `json:"start"`
	Begin                 *TaskReceipt  `json:"begin,omitempty"`
	Scope                 *ScopeReceipt `json:"scope,omitempty"`
}

type Snapshot struct {
	ScopePresent        bool
	ScopeID             uuid.UUID
	ScopeStatus         string
	OwnerCount          int
	SelectedCandidateID string
	AppliedResolutionID uuid.UUID
	Content             Content
	TaskVersion         int
	TaskState           string
	SubtitleState       string
	VideoState          string
	FailureStage        string
}

func ObservationFromSnapshot(snapshot Snapshot) (Observation, error) {
	observation := Observation{
		ScopePresent:        snapshot.ScopePresent,
		ScopeID:             snapshot.ScopeID,
		ScopeStatus:         snapshot.ScopeStatus,
		OwnerCount:          snapshot.OwnerCount,
		SelectedCandidateID: snapshot.SelectedCandidateID,
		AppliedResolutionID: snapshot.AppliedResolutionID,
		TaskVersion:         snapshot.TaskVersion,
		TaskState:           snapshot.TaskState,
		SubtitleState:       snapshot.SubtitleState,
		VideoState:          snapshot.VideoState,
		FailureStage:        snapshot.FailureStage,
	}
	if snapshot.ScopePresent {
		hash, err := HashContent(snapshot.Content)
		if err != nil {
			return Observation{}, err
		}
		observation.ContentHash = hash
	}
	return observation, nil
}

func HashContent(content Content) (string, error) {
	if content.TaskID == uuid.Nil {
		return "", fmt.Errorf("subtitle binding content requires a task")
	}
	candidates := append([]Candidate(nil), content.Candidates...)
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].CandidateID < candidates[j].CandidateID })
	canonical := struct {
		Version                 string      `json:"version"`
		TaskID                  uuid.UUID   `json:"taskId"`
		SeriesTitle             string      `json:"seriesTitle"`
		TargetSeason            int         `json:"targetSeason"`
		TargetEpisode           int         `json:"targetEpisode"`
		TargetTitle             string      `json:"targetTitle"`
		SourceVideoRelativePath string      `json:"sourceVideoRelativePath"`
		Candidates              []Candidate `json:"candidates"`
	}{
		Version:                 "subtitle-binding-v1",
		TaskID:                  content.TaskID,
		SeriesTitle:             content.SeriesTitle,
		TargetSeason:            content.TargetSeason,
		TargetEpisode:           content.TargetEpisode,
		TargetTitle:             content.TargetTitle,
		SourceVideoRelativePath: content.SourceVideoRelativePath,
		Candidates:              candidates,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func NewStartBinding(handle AttemptHandle, observation Observation) Binding {
	return Binding{
		Version:               Version,
		OperationID:           handle.OperationID,
		Attempt:               handle.JobAttempt,
		AttemptID:             handle.AttemptID,
		OperationAttemptCount: handle.OperationAttemptCount,
		Phase:                 PhaseStarted,
		Start:                 observation,
	}
}

func Encode(binding Binding) ([]byte, error) {
	if err := validate(binding); err != nil {
		return nil, err
	}
	return json.Marshal(binding)
}

func Decode(raw []byte, version int32, phase string, attempt int32, attemptID uuid.UUID, operationAttemptCount int32) (Binding, error) {
	if version != Version || attempt <= 0 || attemptID == uuid.Nil || operationAttemptCount <= 0 {
		return Binding{}, ErrUnsupported
	}
	var binding Binding
	if err := json.Unmarshal(raw, &binding); err != nil {
		return Binding{}, ErrUnsupported
	}
	if binding.Version != Version || binding.Attempt != int(attempt) ||
		binding.AttemptID != attemptID || binding.OperationAttemptCount != int(operationAttemptCount) ||
		binding.Phase != phase {
		return Binding{}, ErrUnsupported
	}
	if err := validate(binding); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

func validate(binding Binding) error {
	if binding.Version != Version || binding.OperationID == uuid.Nil {
		return ErrUnsupported
	}
	if binding.Attempt <= 0 || binding.AttemptID == uuid.Nil || binding.OperationAttemptCount <= 0 {
		return ErrUnsupported
	}
	switch binding.Phase {
	case PhaseStarted, PhaseBegun, PhaseScope:
	default:
		return ErrUnsupported
	}
	if err := validateObservation(binding.Start); err != nil {
		return err
	}
	if binding.Phase == PhaseStarted {
		if binding.Begin != nil || binding.Scope != nil {
			return ErrUnsupported
		}
		return nil
	}
	if binding.Begin == nil || binding.Begin.Attempt != binding.Attempt || binding.Begin.AttemptID != binding.AttemptID {
		return ErrUnsupported
	}
	if err := validateTaskReceipt(binding.Begin.TaskVersion, binding.Begin.TaskState, binding.Begin.SubtitleState, binding.Begin.VideoState); err != nil {
		return err
	}
	if binding.Phase == PhaseBegun {
		if binding.Scope != nil {
			return ErrUnsupported
		}
		return nil
	}
	if binding.Scope == nil || !binding.Scope.CreatedByOperation ||
		binding.Scope.Attempt != binding.Attempt || binding.Scope.AttemptID != binding.AttemptID ||
		binding.Scope.ScopeID == uuid.Nil || strings.TrimSpace(binding.Scope.ContentHash) == "" ||
		binding.Scope.ScopeStatus != "pending" {
		return ErrUnsupported
	}
	return validateTaskReceipt(binding.Scope.TaskVersion, binding.Scope.TaskState, binding.Scope.SubtitleState, binding.Scope.VideoState)
}

func validateObservation(observation Observation) error {
	if !observation.ScopePresent {
		if observation.ScopeID != uuid.Nil || observation.ContentHash != "" ||
			observation.SelectedCandidateID != "" || observation.AppliedResolutionID != uuid.Nil ||
			observation.OwnerCount != 0 || observation.ScopeStatus != "" {
			return ErrUnsupported
		}
	} else {
		if observation.ScopeID == uuid.Nil || strings.TrimSpace(observation.ContentHash) == "" || observation.OwnerCount < 0 {
			return ErrUnsupported
		}
		switch observation.ScopeStatus {
		case "pending", "expired":
			if observation.SelectedCandidateID != "" || observation.AppliedResolutionID != uuid.Nil {
				return ErrUnsupported
			}
		case "applied":
			if strings.TrimSpace(observation.SelectedCandidateID) == "" || observation.AppliedResolutionID == uuid.Nil {
				return ErrUnsupported
			}
		default:
			return ErrUnsupported
		}
	}
	return validateTaskReceipt(observation.TaskVersion, observation.TaskState, observation.SubtitleState, observation.VideoState)
}

func validateTaskReceipt(version int, state, subtitle, video string) error {
	if version < 0 || strings.TrimSpace(state) == "" || strings.TrimSpace(subtitle) == "" || strings.TrimSpace(video) == "" {
		return ErrUnsupported
	}
	return nil
}

func (binding Binding) LatestObservation() Observation {
	if binding.Scope != nil {
		return Observation{
			ScopePresent:  true,
			ScopeID:       binding.Scope.ScopeID,
			ScopeStatus:   binding.Scope.ScopeStatus,
			ContentHash:   binding.Scope.ContentHash,
			TaskVersion:   binding.Scope.TaskVersion,
			TaskState:     binding.Scope.TaskState,
			SubtitleState: binding.Scope.SubtitleState,
			VideoState:    binding.Scope.VideoState,
			FailureStage:  binding.Scope.FailureStage,
		}
	}
	return binding.Start
}

func (binding Binding) TaskObservation() TaskReceipt {
	if binding.Begin != nil {
		return *binding.Begin
	}
	return TaskReceipt{
		Attempt:       binding.Attempt,
		AttemptID:     binding.AttemptID,
		TaskVersion:   binding.Start.TaskVersion,
		TaskState:     binding.Start.TaskState,
		SubtitleState: binding.Start.SubtitleState,
		VideoState:    binding.Start.VideoState,
		FailureStage:  binding.Start.FailureStage,
	}
}
