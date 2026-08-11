package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestValidateArtifactCompletionRequiresCanonicalASSArtifact(t *testing.T) {
	completion := MediaArtifactCompletion{
		TaskID:         uuid.MustParse("76000000-0000-0000-0000-000000000001"),
		OperationID:    uuid.MustParse("76000000-0000-0000-0000-000000000002"),
		SourceFileID:   uuid.MustParse("76000000-0000-0000-0000-000000000003"),
		Kind:           MediaSubtitle,
		BaseName:       "Canonical Show - S02E01 - Episode Name",
		FilePath:       "/staging/Canonical Show - S02E01 - Episode Name.ass",
		Format:         "ass",
		SizeBytes:      1024,
		ChecksumSHA256: make([]byte, 32),
	}
	if err := ValidateArtifactCompletion(completion); err != nil {
		t.Fatalf("ValidateArtifactCompletion(valid) error = %v", err)
	}

	completion.FilePath = "/staging/different-name.ass"
	if err := ValidateArtifactCompletion(completion); err == nil {
		t.Fatal("ValidateArtifactCompletion(noncanonical basename) error = nil")
	}

	completion.FilePath = "/staging/Canonical Show - S02E01 - Episode Name.srt"
	completion.Format = "srt"
	if err := ValidateArtifactCompletion(completion); err == nil {
		t.Fatal("ValidateArtifactCompletion(SRT artifact) error = nil")
	}
}
