package domain

import (
	"errors"
	"reflect"
	"testing"
)

func TestDefaultTranscodeProfileIsValid(t *testing.T) {
	profile := DefaultTranscodeProfile()
	if err := ValidateTranscodeProfile(profile); err != nil {
		t.Fatalf("ValidateTranscodeProfile(DefaultTranscodeProfile()) error = %v", err)
	}
	if profile.FileExtension != "mp4" || profile.MaxConcurrency != 1 {
		t.Fatalf("default profile = %#v", profile)
	}
}

func TestRecommendedTranscodeProfilesPassCompatibilityValidation(t *testing.T) {
	profiles := []TranscodeProfile{
		{
			Name: "compatible-h264", VideoCodec: "h264", Encoder: "libx264",
			Container: "mp4", FileExtension: "mp4", QualityMode: "crf", QualityValue: 20,
			AudioPolicy: "transcode", AudioCodec: "aac", Preset: "medium", PixelFormat: "yuv420p",
			ThreadCount: 0, MaxConcurrency: 1,
		},
		{
			Name: "archive-hevc", VideoCodec: "hevc", Encoder: "libx265",
			Container: "matroska", FileExtension: "mkv", QualityMode: "crf", QualityValue: 22,
			AudioPolicy: "copy", Preset: "slow", PixelFormat: "yuv420p10le",
			ThreadCount: 0, MaxConcurrency: 1,
		},
		{
			Name: "archive-av1", VideoCodec: "av1", Encoder: "libsvtav1",
			Container: "matroska", FileExtension: "mkv", QualityMode: "crf", QualityValue: 28,
			AudioPolicy: "copy", Preset: "6", PixelFormat: "yuv420p10le",
			ThreadCount: 0, MaxConcurrency: 1,
		},
		{
			Name: "nvidia-hevc", VideoCodec: "hevc", Encoder: "hevc_nvenc",
			Container: "matroska", FileExtension: "mkv", QualityMode: "cq", QualityValue: 23,
			AudioPolicy: "copy", Preset: "p4", PixelFormat: "p010le",
			ThreadCount: 0, MaxConcurrency: 1,
		},
	}

	for _, profile := range profiles {
		t.Run(profile.Name, func(t *testing.T) {
			if err := ValidateTranscodeProfile(profile); err != nil {
				t.Fatalf("ValidateTranscodeProfile() error = %v", err)
			}
		})
	}
}

func TestBuildTranscodeFFmpegArgsForDistinctProfiles(t *testing.T) {
	tests := []struct {
		name    string
		profile TranscodeProfile
		input   string
		output  string
		want    []string
	}{
		{
			name: "AV1 Matroska with copied audio",
			profile: TranscodeProfile{
				Name:           "archive-av1",
				VideoCodec:     "av1",
				Encoder:        "libsvtav1",
				Container:      "matroska",
				FileExtension:  "mkv",
				QualityMode:    "crf",
				QualityValue:   22,
				AudioPolicy:    "copy",
				Preset:         "6",
				PixelFormat:    "yuv420p10le",
				ThreadCount:    4,
				MaxConcurrency: 1,
			},
			input:  "/downloads/source.mkv",
			output: "/work/.episode.part.mkv",
			want: []string{
				"-y", "-i", "/downloads/source.mkv",
				"-map", "0:v:0", "-map", "0:a?", "-sn",
				"-c:v", "libsvtav1", "-crf", "22", "-preset", "6",
				"-pix_fmt", "yuv420p10le", "-threads", "4",
				"-c:a", "copy", "-f", "matroska", "/work/.episode.part.mkv",
			},
		},
		{
			name: "H264 MP4 with AAC audio",
			profile: TranscodeProfile{
				Name:           "compatible-h264",
				VideoCodec:     "h264",
				Encoder:        "libx264",
				Container:      "mp4",
				FileExtension:  "mp4",
				QualityMode:    "crf",
				QualityValue:   20,
				AudioPolicy:    "transcode",
				AudioCodec:     "aac",
				Preset:         "slow",
				PixelFormat:    "yuv420p",
				ThreadCount:    0,
				MaxConcurrency: 4,
			},
			input:  "/downloads/source.m2ts",
			output: "/work/.episode.part.mp4",
			want: []string{
				"-y", "-i", "/downloads/source.m2ts",
				"-map", "0:v:0", "-map", "0:a?", "-sn",
				"-c:v", "libx264", "-crf", "20", "-preset", "slow",
				"-pix_fmt", "yuv420p", "-threads", "0",
				"-c:a", "aac", "-f", "mp4", "/work/.episode.part.mp4",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := BuildTranscodeFFmpegArgs(test.profile, test.input, test.output)
			if err != nil {
				t.Fatalf("BuildTranscodeFFmpegArgs() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("arguments = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestValidateTranscodeProfileRejectsIncompatibleOrUnsafeValues(t *testing.T) {
	base := TranscodeProfile{
		Name:           "base",
		VideoCodec:     "h264",
		Encoder:        "libx264",
		Container:      "mp4",
		FileExtension:  "mp4",
		QualityMode:    "crf",
		QualityValue:   20,
		AudioPolicy:    "transcode",
		AudioCodec:     "aac",
		Preset:         "medium",
		PixelFormat:    "yuv420p",
		ThreadCount:    2,
		MaxConcurrency: 2,
	}

	tests := []struct {
		name   string
		mutate func(*TranscodeProfile)
		field  string
	}{
		{name: "codec and encoder mismatch", mutate: func(profile *TranscodeProfile) { profile.VideoCodec = "av1" }, field: "encoder"},
		{name: "container and extension mismatch", mutate: func(profile *TranscodeProfile) { profile.FileExtension = "mkv" }, field: "fileExtension"},
		{name: "MP4 incompatible audio codec", mutate: func(profile *TranscodeProfile) { profile.AudioCodec = "opus" }, field: "audioCodec"},
		{name: "arbitrary FFmpeg option in preset", mutate: func(profile *TranscodeProfile) { profile.Preset = "-filter_complex" }, field: "preset"},
		{name: "zero transcode concurrency", mutate: func(profile *TranscodeProfile) { profile.MaxConcurrency = 0 }, field: "maxConcurrency"},
		{name: "CRF outside supported range", mutate: func(profile *TranscodeProfile) { profile.QualityValue = 64 }, field: "qualityValue"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := base
			test.mutate(&profile)
			err := ValidateTranscodeProfile(profile)
			var profileErr *TranscodeProfileError
			if !errors.As(err, &profileErr) || profileErr.Field != test.field {
				t.Fatalf("ValidateTranscodeProfile() error = %#v, want field %q", err, test.field)
			}
		})
	}
}

func TestTranscodeProbeExpectationsMatchProfileOutputs(t *testing.T) {
	av1 := TranscodeProfile{
		Name: "archive-av1", VideoCodec: "av1", Encoder: "libsvtav1",
		Container: "matroska", FileExtension: "mkv", QualityMode: "crf", QualityValue: 22,
		AudioPolicy: "copy", Preset: "6", PixelFormat: "yuv420p10le", ThreadCount: 4, MaxConcurrency: 1,
	}
	got, err := BuildTranscodeProbeExpectation(av1)
	if err != nil {
		t.Fatalf("BuildTranscodeProbeExpectation(AV1) error = %v", err)
	}
	want := TranscodeProbeExpectation{
		VideoCodec:         "av1",
		ContainerNames:     []string{"matroska", "webm"},
		FileExtension:      "mkv",
		AudioPolicy:        "copy",
		ExpectedAudioCodec: "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AV1 expectation = %#v, want %#v", got, want)
	}

	h264 := TranscodeProfile{
		Name: "compatible-h264", VideoCodec: "h264", Encoder: "libx264",
		Container: "mp4", FileExtension: "mp4", QualityMode: "crf", QualityValue: 20,
		AudioPolicy: "transcode", AudioCodec: "aac", Preset: "slow", PixelFormat: "yuv420p", ThreadCount: 0, MaxConcurrency: 4,
	}
	got, err = BuildTranscodeProbeExpectation(h264)
	if err != nil {
		t.Fatalf("BuildTranscodeProbeExpectation(H264) error = %v", err)
	}
	want = TranscodeProbeExpectation{
		VideoCodec:         "h264",
		ContainerNames:     []string{"mov", "mp4", "m4a", "3gp", "3g2", "mj2"},
		FileExtension:      "mp4",
		AudioPolicy:        "transcode",
		ExpectedAudioCodec: "aac",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("H264 expectation = %#v, want %#v", got, want)
	}
}

func TestTranscodeProfileAllowsConfiguredConcurrencyOneTwoAndFour(t *testing.T) {
	for _, concurrency := range []int{1, 2, 4} {
		profile := TranscodeProfile{
			Name: "h264", VideoCodec: "h264", Encoder: "libx264",
			Container: "matroska", FileExtension: "mkv", QualityMode: "crf", QualityValue: 20,
			AudioPolicy: "copy", Preset: "medium", PixelFormat: "yuv420p", ThreadCount: 0,
			MaxConcurrency: concurrency,
		}
		if err := ValidateTranscodeProfile(profile); err != nil {
			t.Fatalf("ValidateTranscodeProfile(concurrency=%d) error = %v", concurrency, err)
		}
	}
}
