package mediatools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

const maxCommandOutput = 4 << 20

type Executor interface {
	Output(context.Context, string, ...string) ([]byte, error)
	Run(context.Context, string, ...string) error
}

type OSExecutor struct{}

func (OSExecutor) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	stdoutWriter := &limitedBuffer{buffer: &stdout, limit: maxCommandOutput}
	command.Stdout = stdoutWriter
	command.Stderr = &limitedBuffer{buffer: &stderr, limit: maxCommandOutput}
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("run %s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	if stdoutWriter.truncated {
		return nil, fmt.Errorf("run %s: output exceeds %d bytes", name, maxCommandOutput)
	}
	return stdout.Bytes(), nil
}

func (OSExecutor) Run(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	var output bytes.Buffer
	command.Stdout = &limitedBuffer{buffer: &output, limit: maxCommandOutput}
	command.Stderr = &limitedBuffer{buffer: &output, limit: maxCommandOutput}
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w: %s", name, err, strings.TrimSpace(output.String()))
	}
	return nil
}

type Tools struct {
	executor Executor
}

func New(executor Executor) *Tools {
	if executor == nil {
		executor = OSExecutor{}
	}
	return &Tools{executor: executor}
}

func (tools *Tools) Probe(ctx context.Context, executable, inputPath string) (domain.MediaProbe, error) {
	if strings.TrimSpace(executable) == "" {
		return domain.MediaProbe{}, fmt.Errorf("ffprobe executable is not configured")
	}
	output, err := tools.executor.Output(
		ctx,
		executable,
		"-v", "error", "-show_streams", "-show_format", "-of", "json", inputPath,
	)
	if err != nil {
		return domain.MediaProbe{}, err
	}
	var document probeDocument
	if err := json.Unmarshal(output, &document); err != nil {
		return domain.MediaProbe{}, fmt.Errorf("decode ffprobe output: %w", err)
	}
	probe := domain.MediaProbe{FormatNames: splitFormatNames(document.Format.FormatName), Streams: make([]domain.MediaStreamProbe, 0, len(document.Streams))}
	for _, stream := range document.Streams {
		title := strings.TrimSpace(stream.Tags.Title)
		if title == "" {
			title = strings.TrimSpace(stream.Tags.HandlerName)
		}
		probe.Streams = append(probe.Streams, domain.MediaStreamProbe{
			Index:    stream.Index,
			Type:     strings.ToLower(stream.CodecType),
			Codec:    strings.ToLower(stream.CodecName),
			Language: stream.Tags.Language,
			Title:    title,
			Default:  stream.Disposition.Default != 0,
			Forced:   stream.Disposition.Forced != 0,
		})
	}
	return probe, nil
}

func (tools *Tools) RunFFmpeg(ctx context.Context, executable string, args []string) error {
	if strings.TrimSpace(executable) == "" {
		return fmt.Errorf("ffmpeg executable is not configured")
	}
	return tools.executor.Run(ctx, executable, args...)
}

// CheckExecutable verifies an executable can be invoked by querying its version.
func (tools *Tools) CheckExecutable(ctx context.Context, executable string) error {
	if strings.TrimSpace(executable) == "" {
		return fmt.Errorf("media tool executable is not configured")
	}
	return tools.executor.Run(ctx, executable, "-version")
}

type probeDocument struct {
	Streams []struct {
		Index       int    `json:"index"`
		CodecName   string `json:"codec_name"`
		CodecType   string `json:"codec_type"`
		Disposition struct {
			Default int `json:"default"`
			Forced  int `json:"forced"`
		} `json:"disposition"`
		Tags struct {
			Language    string `json:"language"`
			Title       string `json:"title"`
			HandlerName string `json:"handler_name"`
		} `json:"tags"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
	} `json:"format"`
}

func splitFormatNames(value string) []string {
	result := make([]string, 0)
	for _, name := range strings.Split(value, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			result = append(result, name)
		}
	}
	return result
}

type limitedBuffer struct {
	buffer    *bytes.Buffer
	limit     int
	truncated bool
}

func (writer *limitedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := writer.limit - writer.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			writer.truncated = true
			value = value[:remaining]
		}
		_, _ = writer.buffer.Write(value)
	} else if originalLength > 0 {
		writer.truncated = true
	}
	return originalLength, nil
}
