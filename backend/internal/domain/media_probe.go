package domain

import (
	"fmt"
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
}

type MediaProbe struct {
	FormatNames []string
	Streams     []MediaStreamProbe
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

func streamCodecs(streams []MediaStreamProbe, streamType string) []string {
	codecs := make([]string, 0)
	for _, stream := range streams {
		if stream.Type == streamType {
			codecs = append(codecs, strings.ToLower(stream.Codec))
		}
	}
	return codecs
}
