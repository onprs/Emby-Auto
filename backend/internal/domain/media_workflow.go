package domain

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type MediaWorkflowError struct {
	Code      string
	Message   string
	Retryable bool
}

func (err *MediaWorkflowError) Error() string {
	return err.Message
}

type TaskExternalSubtitle struct {
	SourceFileID uuid.UUID
	RelativePath string
	Language     string
	Format       SubtitleFormat
}

type TaskMediaCommand struct {
	TaskID                  uuid.UUID
	MediaType               TaskMediaType
	State                   TaskState
	VideoState              VideoState
	SubtitleState           SubtitleState
	DownloadID              uuid.UUID
	SavePath                string
	SourceVideoFileID       uuid.UUID
	SourceVideoRelativePath string
	ExternalSubtitles       []TaskExternalSubtitle
	TranscodeProfileID      uuid.UUID
	TranscodeProfile        TranscodeProfile
	Names                   EpisodeFileNames
	OutputRelativeDirectory string
}

type MediaArtifact struct {
	ID                 uuid.UUID
	TaskID             uuid.UUID
	SourceFileID       uuid.UUID
	TranscodeProfileID uuid.UUID
	Kind               MediaKind
	BaseName           string
	FilePath           string
	Format             string
	SizeBytes          int64
	ChecksumSHA256     []byte
	Metadata           map[string]any
}

type MediaArtifactCompletion struct {
	TaskID             uuid.UUID
	OperationID        uuid.UUID
	SourceFileID       uuid.UUID
	TranscodeProfileID uuid.UUID
	Kind               MediaKind
	BaseName           string
	FilePath           string
	Format             string
	SizeBytes          int64
	ChecksumSHA256     []byte
	Metadata           map[string]any
}

type FinalizeMediaCommand struct {
	TaskID             uuid.UUID
	State              TaskState
	TranscodeProfileID uuid.UUID
	Video              MediaArtifact
	Subtitle           MediaArtifact
}

func SubtitleFormatFromPath(filePath string) SubtitleFormat {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".ass":
		return SubtitleASS
	case ".ssa":
		return SubtitleSSA
	case ".srt":
		return SubtitleSRT
	case ".vtt":
		return SubtitleWebVTT
	case ".idx", ".sub":
		return SubtitleVobSub
	default:
		return ""
	}
}

func SubtitleFormatFromCodec(codec string) SubtitleFormat {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "ass":
		return SubtitleASS
	case "ssa":
		return SubtitleSSA
	case "subrip", "srt":
		return SubtitleSRT
	case "webvtt":
		return SubtitleWebVTT
	case "mov_text":
		return SubtitleMovText
	case "hdmv_pgs_subtitle", "pgs":
		return SubtitlePGS
	case "dvd_subtitle", "vobsub":
		return SubtitleVobSub
	default:
		return ""
	}
}

func ValidateArtifactCompletion(completion MediaArtifactCompletion) error {
	if completion.TaskID == uuid.Nil || completion.OperationID == uuid.Nil {
		return fmt.Errorf("artifact completion requires task and operation IDs")
	}
	if completion.SourceFileID == uuid.Nil {
		return fmt.Errorf("artifact completion requires a source file ID")
	}
	if completion.Kind != MediaVideo && completion.Kind != MediaSubtitle {
		return fmt.Errorf("artifact completion kind must be video or subtitle")
	}
	if completion.Kind == MediaVideo && completion.TranscodeProfileID == uuid.Nil {
		return fmt.Errorf("video artifact requires a transcode profile")
	}
	if strings.TrimSpace(completion.BaseName) == "" || strings.TrimSpace(completion.FilePath) == "" || strings.TrimSpace(completion.Format) == "" {
		return fmt.Errorf("artifact completion requires basename, path, and format")
	}
	fileName := filepath.Base(completion.FilePath)
	if strings.TrimSuffix(fileName, filepath.Ext(fileName)) != completion.BaseName {
		return fmt.Errorf("artifact filename must use its canonical basename")
	}
	if completion.Kind == MediaSubtitle && (strings.ToLower(completion.Format) != "ass" || strings.ToLower(filepath.Ext(fileName)) != ".ass") {
		return fmt.Errorf("subtitle artifact must be an ASS file")
	}
	if completion.SizeBytes <= 0 || len(completion.ChecksumSHA256) != 32 {
		return fmt.Errorf("artifact completion requires positive size and SHA-256 checksum")
	}
	return nil
}
