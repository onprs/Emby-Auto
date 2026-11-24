package mediatools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
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
	return tools.probe(ctx, executable, inputPath, false)
}

// ProbeWithPacketCounts 额外统计每个流的数据包数量。
// 解复用一整趟文件而不解码，用于选择正片流并校验输出帧数。
func (tools *Tools) ProbeWithPacketCounts(ctx context.Context, executable, inputPath string) (domain.MediaProbe, error) {
	return tools.probe(ctx, executable, inputPath, true)
}

func (tools *Tools) probe(ctx context.Context, executable, inputPath string, countPackets bool) (domain.MediaProbe, error) {
	if strings.TrimSpace(executable) == "" {
		return domain.MediaProbe{}, fmt.Errorf("ffprobe executable is not configured")
	}
	args := []string{"-v", "error"}
	if countPackets {
		args = append(args, "-count_packets")
	}
	args = append(args, "-show_streams", "-show_format", "-of", "json", inputPath)
	output, err := tools.executor.Output(ctx, executable, args...)
	if err != nil {
		return domain.MediaProbe{}, err
	}
	var document probeDocument
	if err := json.Unmarshal(output, &document); err != nil {
		return domain.MediaProbe{}, fmt.Errorf("decode ffprobe output: %w", err)
	}
	probe := domain.MediaProbe{
		FormatNames:     splitFormatNames(document.Format.FormatName),
		DurationSeconds: parseProbeSeconds(document.Format.Duration),
		Streams:         make([]domain.MediaStreamProbe, 0, len(document.Streams)),
	}
	for _, stream := range document.Streams {
		title := strings.TrimSpace(stream.Tags.Title)
		if title == "" {
			title = strings.TrimSpace(stream.Tags.HandlerName)
		}
		duration := parseProbeSeconds(stream.Duration)
		if duration <= 0 {
			duration = parseProbeSeconds(stream.Tags.Duration)
		}
		probe.Streams = append(probe.Streams, domain.MediaStreamProbe{
			Index:           stream.Index,
			Type:            strings.ToLower(stream.CodecType),
			Codec:           strings.ToLower(stream.CodecName),
			Language:        stream.Tags.Language,
			Title:           title,
			Default:         stream.Disposition.Default != 0,
			Forced:          stream.Disposition.Forced != 0,
			AttachedPic:     stream.Disposition.AttachedPic != 0,
			DurationSeconds: duration,
			FrameCount:      parseProbeCount(stream.FrameCount),
			PacketCount:     parseProbeCount(stream.PacketCount),
		})
	}
	return probe, nil
}

// parseProbeSeconds 接受秒数或 Matroska 标签使用的 HH:MM:SS.nnnnnnnnn 格式。
func parseProbeSeconds(value string) float64 {
	value = strings.TrimSpace(value)
	if value == "" || value == "N/A" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return seconds
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0
	}
	hours, hoursErr := strconv.Atoi(parts[0])
	minutes, minutesErr := strconv.Atoi(parts[1])
	seconds, secondsErr := strconv.ParseFloat(parts[2], 64)
	if hoursErr != nil || minutesErr != nil || secondsErr != nil || hours < 0 || minutes < 0 || seconds < 0 {
		return 0
	}
	return float64(hours*3600+minutes*60) + seconds
}

func parseProbeCount(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
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
		Duration    string `json:"duration"`
		FrameCount  string `json:"nb_frames"`
		PacketCount string `json:"nb_read_packets"`
		Disposition struct {
			Default     int `json:"default"`
			Forced      int `json:"forced"`
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
		Tags struct {
			Language    string `json:"language"`
			Title       string `json:"title"`
			HandlerName string `json:"handler_name"`
			Duration    string `json:"duration"`
		} `json:"tags"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
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
