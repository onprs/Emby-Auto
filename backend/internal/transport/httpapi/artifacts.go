package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/service"
)

func (server *Server) GetTaskArtifactContent(ctx context.Context, request GetTaskArtifactContentRequestObject) (GetTaskArtifactContentResponseObject, error) {
	if server.artifacts == nil {
		return GetTaskArtifactContent503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "artifacts")}, nil
	}
	content, err := server.artifacts.OpenArtifact(ctx, uuid.UUID(request.TaskId), uuid.UUID(request.ArtifactId))
	var serviceErr *service.Error
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return GetTaskArtifactContent404JSONResponse{NotFoundJSONResponse: notFoundError(ctx, "the artifact was not found")}, nil
	case errors.As(err, &serviceErr):
		return GetTaskArtifactContent503JSONResponse{ServiceUnavailableJSONResponse: ServiceUnavailableJSONResponse(apiErrorFromService(ctx, serviceErr))}, nil
	case err != nil:
		return GetTaskArtifactContent503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "artifacts")}, nil
	}

	contentType := content.ContentType
	acceptRanges := "bytes"
	size := content.SizeBytes

	if request.Params.Range != nil {
		start, end, ok := parseByteRange(*request.Params.Range, size)
		if !ok {
			_ = content.Reader.Close()
			return GetTaskArtifactContent416JSONResponse(ApiError{
				Code:      "range_not_satisfiable",
				Message:   "the requested byte range cannot be satisfied",
				Details:   map[string]any{},
				RequestId: middleware.GetReqID(ctx),
			}), nil
		}
		reader, ok := content.Reader.(io.ReadSeekCloser)
		if !ok {
			_ = content.Reader.Close()
			return GetTaskArtifactContent503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "artifacts")}, nil
		}
		if _, err := reader.Seek(start, io.SeekStart); err != nil {
			_ = reader.Close()
			return GetTaskArtifactContent503JSONResponse{ServiceUnavailableJSONResponse: serviceUnavailableError(ctx, "artifacts")}, nil
		}
		length := end - start + 1
		contentRange := fmt.Sprintf("bytes %d-%d/%d", start, end, size)
		return GetTaskArtifactContent206ApplicationoctetStreamResponse{
			Body:          readCloser{Reader: io.LimitReader(reader, length), Closer: reader},
			ContentLength: length,
			Headers: GetTaskArtifactContent206ResponseHeaders{
				AcceptRanges: &acceptRanges, ContentRange: &contentRange, ContentType: &contentType,
			},
		}, nil
	}

	return GetTaskArtifactContent200ApplicationoctetStreamResponse{
		Body:          content.Reader,
		ContentLength: size,
		Headers: GetTaskArtifactContent200ResponseHeaders{
			AcceptRanges: &acceptRanges, ContentType: &contentType,
		},
	}, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

func parseByteRange(header string, size int64) (int64, int64, bool) {
	if !strings.HasPrefix(header, "bytes=") || size <= 0 {
		return 0, 0, false
	}
	spec := strings.TrimPrefix(header, "bytes=")
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	var start, end int64
	if parts[0] == "" {
		var suffix int64
		if _, err := fmt.Sscanf(parts[1], "%d", &suffix); err != nil || suffix <= 0 {
			return 0, 0, false
		}
		if suffix > size {
			suffix = size
		}
		start = size - suffix
		end = size - 1
		return start, end, true
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &start); err != nil {
		return 0, 0, false
	}
	if parts[1] == "" {
		end = size - 1
	} else if _, err := fmt.Sscanf(parts[1], "%d", &end); err != nil {
		return 0, 0, false
	}
	if start < 0 || end < start || start >= size {
		return 0, 0, false
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true
}
