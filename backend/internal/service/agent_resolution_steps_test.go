package service

import (
	"math"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/agentharness"
)

func TestAgentResolutionStepSequenceUsesResolutionVersionGeneration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		resolutionVersion int
		stepSequence      int
		want              int32
	}{
		{name: "first step in first started version", resolutionVersion: 2, stepSequence: 1, want: 65},
		{name: "same step after automatic rebuild", resolutionVersion: 5, stepSequence: 1, want: 257},
		{name: "last step in one version", resolutionVersion: 7, stepSequence: 64, want: 448},
		{name: "first step in next version", resolutionVersion: 8, stepSequence: 1, want: 449},
	}
	seen := make(map[int32]string, len(tests))
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sequence, err := agentResolutionStepSequence(test.resolutionVersion, test.stepSequence)
			if err != nil {
				t.Fatalf("agentResolutionStepSequence() error = %v", err)
			}
			if sequence != test.want {
				t.Fatalf("agentResolutionStepSequence(%d, %d) = %d, want %d", test.resolutionVersion, test.stepSequence, sequence, test.want)
			}
			if previous, duplicate := seen[sequence]; duplicate {
				t.Fatalf("sequence %d collides with %s", sequence, previous)
			}
			seen[sequence] = test.name
		})
	}
}

func TestAgentResolutionStepSequencesRejectInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		resolutionVersion int
		steps             []agentharness.Step
	}{
		{name: "zero resolution version", resolutionVersion: 0},
		{name: "negative resolution version", resolutionVersion: -1, steps: []agentharness.Step{{Sequence: 1}}},
		{name: "zero step sequence", resolutionVersion: 2, steps: []agentharness.Step{{Sequence: 0}}},
		{name: "negative step sequence", resolutionVersion: 2, steps: []agentharness.Step{{Sequence: -1}}},
		{name: "step exceeds generation stride", resolutionVersion: 2, steps: []agentharness.Step{{Sequence: 65}}},
		{name: "duplicate step sequence", resolutionVersion: 2, steps: []agentharness.Step{{Sequence: 1}, {Sequence: 1}}},
		{name: "storage range overflow", resolutionVersion: int(math.MaxInt32), steps: []agentharness.Step{{Sequence: 64}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if sequences, err := agentResolutionStepSequences(test.resolutionVersion, test.steps); err == nil {
				t.Fatalf("agentResolutionStepSequences() = %v, want error", sequences)
			}
		})
	}
}
