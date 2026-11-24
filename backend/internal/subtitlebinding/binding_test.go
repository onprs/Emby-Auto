package subtitlebinding

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func testHandle() AttemptHandle {
	return AttemptHandle{
		OperationID:           uuid.New(),
		JobAttempt:            1,
		AttemptID:             uuid.New(),
		OperationAttemptCount: 1,
		ResourceType:          "episode_task",
		ResourceID:            uuid.New(),
	}
}

func testContent() Content {
	return Content{
		TaskID:                  uuid.New(),
		SeriesTitle:             "Series",
		TargetSeason:            1,
		TargetEpisode:           1,
		TargetTitle:             "Pilot",
		SourceVideoRelativePath: "Show.S01E01.mkv",
		Candidates: []Candidate{
			{CandidateID: "b", Source: "embedded", StreamIndex: 3, Format: "ass", Language: "chi", Title: "B", Path: "b.ass"},
			{CandidateID: "a", Source: "embedded", StreamIndex: 2, Format: "ass", Language: "chi", Title: "A", Path: "a.ass"},
		},
	}
}

func TestBindingRoundTripAndUnsupported(t *testing.T) {
	handle := testHandle()
	observation, err := ObservationFromSnapshot(Snapshot{
		ScopePresent:  true,
		ScopeID:       uuid.New(),
		ScopeStatus:   "pending",
		OwnerCount:    0,
		Content:       testContent(),
		TaskVersion:   3,
		TaskState:     "processing",
		SubtitleState: "subtitle_queued",
		VideoState:    "transcoding",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := NewStartBinding(handle, observation)
	raw, err := Encode(binding)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw, Version, PhaseStarted, int32(handle.JobAttempt), handle.AttemptID, int32(handle.OperationAttemptCount))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Start.ScopeID != observation.ScopeID || decoded.Start.ContentHash != observation.ContentHash {
		t.Fatalf("decoded start = %+v, want %+v", decoded.Start, observation)
	}
	otherID := uuid.New()
	if _, err := Decode(raw, Version, PhaseStarted, int32(handle.JobAttempt), otherID, int32(handle.OperationAttemptCount)); err != ErrUnsupported {
		t.Fatalf("wrong audit UUID decode error = %v, want unsupported", err)
	}
	if _, err := Decode(raw, 2, PhaseStarted, int32(handle.JobAttempt), handle.AttemptID, int32(handle.OperationAttemptCount)); err != ErrUnsupported {
		t.Fatalf("unknown version decode error = %v, want unsupported", err)
	}
	if _, err := Decode([]byte(`{"version":1}`), Version, PhaseStarted, 1, handle.AttemptID, 1); err != ErrUnsupported {
		t.Fatalf("malformed decode error = %v, want unsupported", err)
	}
}

func TestContentHashStableAcrossCandidateOrderAndSensitiveToContent(t *testing.T) {
	content := testContent()
	first, err := HashContent(content)
	if err != nil {
		t.Fatal(err)
	}
	reordered := content
	reordered.Candidates = []Candidate{content.Candidates[1], content.Candidates[0]}
	second, err := HashContent(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("hash changed with candidate order: %s/%s", first, second)
	}
	changed := content
	changed.SourceVideoRelativePath = "Show.S01E02.mkv"
	third, err := HashContent(changed)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("hash ignored video identity")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(first), &decoded); err == nil {
		t.Fatal("content hash must be a hex digest, not JSON")
	}
}
