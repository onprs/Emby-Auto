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

func TestSelectMainVideoStreamPrefersLongestNonAttachedVideo(t *testing.T) {
	tests := []struct {
		name  string
		probe MediaProbe
		want  int
	}{
		{
			name: "cover picture before main video",
			probe: MediaProbe{Streams: []MediaStreamProbe{
				{Index: 0, Type: "video", Codec: "png", DurationSeconds: 0.04, PacketCount: 1, FrameCount: 1},
				{Index: 1, Type: "video", Codec: "h264", DurationSeconds: 4.023, PacketCount: 100},
				{Index: 2, Type: "audio", Codec: "aac"},
			}},
			want: 1,
		},
		{
			name: "attached picture is excluded",
			probe: MediaProbe{Streams: []MediaStreamProbe{
				{Index: 0, Type: "video", Codec: "mjpeg", AttachedPic: true, DurationSeconds: 30, PacketCount: 1},
				{Index: 1, Type: "video", Codec: "hevc", DurationSeconds: 30, PacketCount: 750},
			}},
			want: 1,
		},
		{
			name: "same length cover falls back to duration then index",
			probe: MediaProbe{Streams: []MediaStreamProbe{
				{Index: 0, Type: "video", Codec: "png", DurationSeconds: 4, PacketCount: 2},
				{Index: 1, Type: "video", Codec: "h264", DurationSeconds: 4.023, PacketCount: 2},
			}},
			want: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream, ok := SelectMainVideoStream(test.probe)
			if !ok || stream.Index != test.want {
				t.Fatalf("SelectMainVideoStream() = %#v, %v, want index %d", stream, ok, test.want)
			}
		})
	}
	if _, ok := SelectMainVideoStream(MediaProbe{Streams: []MediaStreamProbe{
		{Index: 0, Type: "video", Codec: "mjpeg", AttachedPic: true},
		{Index: 1, Type: "audio", Codec: "aac"},
	}}); ok {
		t.Fatal("SelectMainVideoStream() accepted an attached-picture-only source")
	}
}

func TestValidateTranscodeProbeRejectsShortOrStaticOutput(t *testing.T) {
	expectation := TranscodeProbeExpectation{
		VideoCodec: "h264", ContainerNames: []string{"matroska", "webm"}, FileExtension: "mkv",
		AudioPolicy: "transcode", ExpectedAudioCodec: "aac",
	}
	input := MediaProbe{DurationSeconds: 1440, Streams: []MediaStreamProbe{
		{Index: 0, Type: "video", Codec: "hevc", DurationSeconds: 1440, PacketCount: 34560},
		{Index: 1, Type: "audio", Codec: "flac"},
	}}
	validOutput := MediaProbe{DurationSeconds: 1440, FormatNames: []string{"matroska", "webm"}, Streams: []MediaStreamProbe{
		{Index: 0, Type: "video", Codec: "h264", DurationSeconds: 1440, PacketCount: 34560},
		{Index: 1, Type: "audio", Codec: "aac"},
	}}
	if err := ValidateTranscodeProbe(expectation, input, validOutput, "/staging/episode.mkv"); err != nil {
		t.Fatalf("ValidateTranscodeProbe(valid) error = %v", err)
	}

	staticOutput := validOutput
	staticOutput.Streams = []MediaStreamProbe{
		{Index: 0, Type: "video", Codec: "h264", DurationSeconds: 1440, PacketCount: 1},
		{Index: 1, Type: "audio", Codec: "aac"},
	}
	if err := ValidateTranscodeProbe(expectation, input, staticOutput, "/staging/episode.mkv"); err == nil {
		t.Fatal("ValidateTranscodeProbe() accepted a single-frame output")
	}

	shortOutput := validOutput
	shortOutput.Streams = []MediaStreamProbe{
		{Index: 0, Type: "video", Codec: "h264", DurationSeconds: 30, PacketCount: 720},
		{Index: 1, Type: "audio", Codec: "aac"},
	}
	if err := ValidateTranscodeProbe(expectation, input, shortOutput, "/staging/episode.mkv"); err == nil {
		t.Fatal("ValidateTranscodeProbe() accepted an output shorter than the input")
	}
}

func TestValidateTranscodeProbeSkipsUnavailableDurationAndFrameData(t *testing.T) {
	expectation := TranscodeProbeExpectation{
		VideoCodec: "h264", ContainerNames: []string{"mov", "mp4"}, FileExtension: "mp4",
		AudioPolicy: "transcode", ExpectedAudioCodec: "aac",
	}
	input := MediaProbe{Streams: []MediaStreamProbe{{Index: 0, Type: "video", Codec: "hevc"}, {Index: 1, Type: "audio", Codec: "flac"}}}
	output := MediaProbe{FormatNames: []string{"mov", "mp4"}, Streams: []MediaStreamProbe{{Index: 0, Type: "video", Codec: "h264"}, {Index: 1, Type: "audio", Codec: "aac"}}}
	if err := ValidateTranscodeProbe(expectation, input, output, "/staging/episode.mp4"); err != nil {
		t.Fatalf("ValidateTranscodeProbe() with unavailable data error = %v", err)
	}
}
