package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/repository"
)

// ArtifactContent is an opened, server-registered artifact ready to stream.
type ArtifactContent struct {
	Reader      io.ReadCloser
	SizeBytes   int64
	ContentType string
}

// ArtifactContentService serves artifact bytes only for artifacts the task
// workflow already registered in the database. Clients never supply paths.
type ArtifactContentService struct {
	queries *db.Queries
}

func NewArtifactContentService(queries *db.Queries) *ArtifactContentService {
	return &ArtifactContentService{queries: queries}
}

func (service *ArtifactContentService) OpenArtifact(ctx context.Context, taskID, artifactID uuid.UUID) (ArtifactContent, error) {
	artifact, err := service.queries.GetArtifactByID(ctx, repository.UUIDToPG(artifactID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactContent{}, domain.ErrNotFound
	}
	if err != nil {
		return ArtifactContent{}, fmt.Errorf("load artifact: %w", err)
	}
	if repository.UUIDFromPG(artifact.TaskID) != taskID {
		return ArtifactContent{}, domain.ErrNotFound
	}
	file, err := os.Open(artifact.FilePath)
	if errors.Is(err, os.ErrNotExist) {
		return ArtifactContent{}, NewError(
			"artifact_unavailable",
			"the artifact file is not available on the server",
			err,
			map[string]any{"artifactId": artifactID.String()},
		)
	}
	if err != nil {
		return ArtifactContent{}, fmt.Errorf("open artifact: %w", err)
	}
	return ArtifactContent{
		Reader:      file,
		SizeBytes:   artifact.SizeBytes,
		ContentType: artifactContentType(artifact.Kind, artifact.Format),
	}, nil
}

func artifactContentType(kind, format string) string {
	if kind == "subtitle" {
		return "text/plain; charset=utf-8"
	}
	switch format {
	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mkv", "matroska":
		return "video/x-matroska"
	default:
		return "application/octet-stream"
	}
}
