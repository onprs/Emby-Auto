package domain

import "testing"

func TestValidateTranscodeProbeChecksContainerVideoAndAudio(t *testing.T) {
	expectation := TranscodeProbeExpectation{
		VideoCodec:         "h264",
		ContainerNames:     []string{"mov", "mp4"},
		FileExtension:      "mp4",
		AudioPolicy:        "transcode",
		ExpectedAudioCodec: "aac",
	}
	input := MediaProbe{Streams: []MediaStreamProbe{{Type: "video", Codec: "hevc"}, {Type: "audio", Codec: "flac"}}}
	output := MediaProbe{
		FormatNames: []string{"mov", "mp4"},
		Streams:     []MediaStreamProbe{{Type: "video", Codec: "h264"}, {Type: "audio", Codec: "aac"}},
	}
	if err := ValidateTranscodeProbe(expectation, input, output, "/staging/episode.mp4"); err != nil {
		t.Fatalf("ValidateTranscodeProbe(valid) error = %v", err)
	}

	invalid := output
	invalid.Streams = []MediaStreamProbe{{Type: "video", Codec: "hevc"}, {Type: "audio", Codec: "aac"}}
	if err := ValidateTranscodeProbe(expectation, input, invalid, "/staging/episode.mp4"); err == nil {
		t.Fatal("ValidateTranscodeProbe(codec mismatch) error = nil")
	}
}

func TestValidateTranscodeProbeRequiresCopiedAudioIdentity(t *testing.T) {
	expectation := TranscodeProbeExpectation{
		VideoCodec:     "av1",
		ContainerNames: []string{"matroska", "webm"},
		FileExtension:  "mkv",
		AudioPolicy:    "copy",
	}
	input := MediaProbe{Streams: []MediaStreamProbe{{Type: "video", Codec: "h264"}, {Type: "audio", Codec: "flac"}, {Type: "audio", Codec: "aac"}}}
	output := MediaProbe{FormatNames: []string{"matroska", "webm"}, Streams: []MediaStreamProbe{{Type: "video", Codec: "av1"}, {Type: "audio", Codec: "flac"}, {Type: "audio", Codec: "aac"}}}
	if err := ValidateTranscodeProbe(expectation, input, output, "/staging/episode.mkv"); err != nil {
		t.Fatalf("ValidateTranscodeProbe(copy) error = %v", err)
	}
	output.Streams[2].Codec = "opus"
	if err := ValidateTranscodeProbe(expectation, input, output, "/staging/episode.mkv"); err == nil {
		t.Fatal("ValidateTranscodeProbe(changed copied audio) error = nil")
	}
}

func TestValidateTranscodeProbeRejectsDroppedAudioStream(t *testing.T) {
	expectation := TranscodeProbeExpectation{
		VideoCodec:         "h264",
		ContainerNames:     []string{"mov", "mp4"},
		FileExtension:      "mp4",
		AudioPolicy:        "transcode",
		ExpectedAudioCodec: "aac",
	}
	input := MediaProbe{Streams: []MediaStreamProbe{{Type: "video", Codec: "hevc"}, {Type: "audio", Codec: "flac"}, {Type: "audio", Codec: "ac3"}}}
	output := MediaProbe{FormatNames: []string{"mov", "mp4"}, Streams: []MediaStreamProbe{{Type: "video", Codec: "h264"}, {Type: "audio", Codec: "aac"}}}
	if err := ValidateTranscodeProbe(expectation, input, output, "/staging/episode.mp4"); err == nil {
		t.Fatal("ValidateTranscodeProbe(dropped audio) error = nil")
	}
}

func TestMediaProbeMapsSupportedAndBitmapSubtitleStreams(t *testing.T) {
	probe := MediaProbe{Streams: []MediaStreamProbe{
		{Index: 2, Type: "subtitle", Codec: "ass", Language: "chi", Title: "简体中文", Default: true},
		{Index: 4, Type: "subtitle", Codec: "hdmv_pgs_subtitle", Language: "eng"},
		{Index: 5, Type: "audio", Codec: "aac"},
	}}
	streams := probe.SubtitleStreams()
	if len(streams) != 2 || streams[0].Format != SubtitleASS || streams[0].Title != "简体中文" || streams[1].Format != SubtitlePGS || !streams[0].Default {
		t.Fatalf("subtitle streams = %#v", streams)
	}
}
