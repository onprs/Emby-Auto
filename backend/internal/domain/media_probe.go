package domain

import (
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
)

type MediaStreamProbe struct {
	Index    int
	Type     string
	Codec    string
	Language string
	Title    string
	Default  bool
	Forced   bool
	// AttachedPic marks cover art or thumbnails that FFmpeg exposes as video streams.
	AttachedPic bool
	// DurationSeconds is the stream duration when the probe can determine it.
	DurationSeconds float64
	// FrameCount is the container-reported frame count; it is often absent for Matroska.
	FrameCount int64
	// PacketCount is only populated by packet-counting probes.
	PacketCount int64
}

type MediaProbe struct {
	FormatNames     []string
	DurationSeconds float64
	Streams         []MediaStreamProbe
}

func (probe MediaProbe) SubtitleStreams() []SubtitleStream {
	streams := make([]SubtitleStream, 0)
	for _, stream := range probe.Streams {
		if stream.Type != "subtitle" {
			continue
		}
		streams = append(streams, SubtitleStream{
			Index:    stream.Index,
			Format:   SubtitleFormatFromCodec(stream.Codec),
			Language: stream.Language,
			Title:    stream.Title,
			Default:  stream.Default,
			Forced:   stream.Forced,
		})
	}
	return streams
}

// SelectMainVideoStream 选择正片视频流。附加图片不属于正片；
// 在候选流中优先选择数据包最多、其次时长最长、再次容器默认标记、
// 最后索引最小的视频流，避免封面、预览等短流抢占正片位置。
func SelectMainVideoStream(probe MediaProbe) (MediaStreamProbe, bool) {
	var selected MediaStreamProbe
	found := false
	for _, stream := range probe.Streams {
		if strings.ToLower(stream.Type) != "video" || stream.AttachedPic {
			continue
		}
		if !found || betterMainVideoStream(stream, selected) {
			selected = stream
			found = true
		}
	}
	return selected, found
}

func betterMainVideoStream(candidate, current MediaStreamProbe) bool {
	candidateFrames := candidateFrameCount(candidate)
	currentFrames := candidateFrameCount(current)
	if candidateFrames != currentFrames {
		return candidateFrames > currentFrames
	}
	if candidate.DurationSeconds != current.DurationSeconds {
		return candidate.DurationSeconds > current.DurationSeconds
	}
	// 包数与时长完全相同时尊重容器默认轨约定，再由索引兜底。
	if candidate.Default != current.Default {
		return candidate.Default
	}
	return candidate.Index < current.Index
}

func candidateFrameCount(stream MediaStreamProbe) int64 {
	if stream.PacketCount > 0 {
		return stream.PacketCount
	}
	return stream.FrameCount
}

func ValidateTranscodeProbe(
	expectation TranscodeProbeExpectation,
	input MediaProbe,
	output MediaProbe,
	outputPath string,
) error {
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(outputPath)), ".")
	if extension != expectation.FileExtension {
		return fmt.Errorf("transcode output extension %q does not match %q", extension, expectation.FileExtension)
	}
	containerMatched := false
	for _, actual := range output.FormatNames {
		if slices.Contains(expectation.ContainerNames, strings.ToLower(actual)) {
			containerMatched = true
			break
		}
	}
	if !containerMatched {
		return fmt.Errorf("transcode output container %q is incompatible", strings.Join(output.FormatNames, ","))
	}
	videoCodecs := streamCodecs(output.Streams, "video")
	if len(videoCodecs) != 1 || videoCodecs[0] != expectation.VideoCodec {
		return fmt.Errorf("transcode output video codecs %q do not match %q", strings.Join(videoCodecs, ","), expectation.VideoCodec)
	}
	mainVideo, ok := SelectMainVideoStream(input)
	if !ok {
		return fmt.Errorf("transcode input has no main video stream")
	}
	outputVideo, ok := SelectMainVideoStream(output)
	if !ok {
		return fmt.Errorf("transcode output has no main video stream")
	}
	if err := validateTranscodeDuration(mainVideo, input, outputVideo, output); err != nil {
		return err
	}
	outputFrames := candidateFrameCount(outputVideo)
	if inputFrames := candidateFrameCount(mainVideo); inputFrames > 0 && outputFrames > 0 && outputFrames*100 < inputFrames*90 {
		return fmt.Errorf("transcode output has %d frames, less than 90%% of the input %d frames", outputFrames, inputFrames)
	}
	inputAudio := streamCodecs(input.Streams, "audio")
	outputAudio := streamCodecs(output.Streams, "audio")
	if len(inputAudio) != len(outputAudio) {
		return fmt.Errorf("transcode output audio stream count %d does not match input %d", len(outputAudio), len(inputAudio))
	}
	switch expectation.AudioPolicy {
	case "transcode":
		for _, codec := range outputAudio {
			if codec != expectation.ExpectedAudioCodec {
				return fmt.Errorf("transcode output audio codec %q does not match %q", codec, expectation.ExpectedAudioCodec)
			}
		}
	case "copy":
		if !slices.Equal(inputAudio, outputAudio) {
			return fmt.Errorf("copied audio codecs %q do not match input %q", strings.Join(outputAudio, ","), strings.Join(inputAudio, ","))
		}
	default:
		return fmt.Errorf("unknown audio policy %q", expectation.AudioPolicy)
	}
	return nil
}

// validateTranscodeDuration 保证产物覆盖源主视频的时间轴。
// 短于输入时长减去容差说明映射到了封面、预览或截断的输出。
func validateTranscodeDuration(
	mainVideo MediaStreamProbe,
	input MediaProbe,
	outputVideo MediaStreamProbe,
	output MediaProbe,
) error {
	inputDuration := mainVideo.DurationSeconds
	if inputDuration <= 0 {
		inputDuration = input.DurationSeconds
	}
	outputDuration := outputVideo.DurationSeconds
	if outputDuration <= 0 {
		outputDuration = output.DurationSeconds
	}
	if inputDuration <= 0 || outputDuration <= 0 {
		return nil
	}
	tolerance := math.Max(3, inputDuration*0.05)
	if outputDuration < inputDuration-tolerance {
		return fmt.Errorf(
			"transcode output duration %.3fs is shorter than input %.3fs",
			outputDuration, inputDuration,
		)
	}
	return nil
}

func streamCodecs(streams []MediaStreamProbe, streamType string) []string {
	codecs := make([]string, 0)
	for _, stream := range streams {
		if stream.Type == streamType {
			codecs = append(codecs, strings.ToLower(stream.Codec))
		}
	}
	return codecs
}
