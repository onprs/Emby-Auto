package domain

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

type TranscodeProfile struct {
	Name           string
	VideoCodec     string
	Encoder        string
	Container      string
	FileExtension  string
	QualityMode    string
	QualityValue   float64
	AudioPolicy    string
	AudioCodec     string
	Preset         string
	PixelFormat    string
	ThreadCount    int
	MaxConcurrency int
}

func DefaultTranscodeProfile() TranscodeProfile {
	return TranscodeProfile{
		Name:           "default-h264",
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
		ThreadCount:    0,
		MaxConcurrency: 1,
	}
}

type TranscodeProfileError struct {
	Field  string
	Reason string
}

func (err *TranscodeProfileError) Error() string {
	return fmt.Sprintf("invalid transcode profile field %s: %s", err.Field, err.Reason)
}

type TranscodeProbeExpectation struct {
	VideoCodec         string
	ContainerNames     []string
	FileExtension      string
	AudioPolicy        string
	ExpectedAudioCodec string
}

var codecEncoders = map[string]map[string]struct{}{
	"h264": {
		"libx264": {}, "h264_nvenc": {},
	},
	"hevc": {
		"libx265": {}, "hevc_nvenc": {},
	},
	"av1": {
		"libsvtav1": {}, "libaom-av1": {}, "av1_nvenc": {},
	},
}

var containerExtensions = map[string]string{
	"matroska": "mkv",
	"mp4":      "mp4",
	"webm":     "webm",
}

var containerVideoCodecs = map[string]map[string]struct{}{
	"matroska": {"h264": {}, "hevc": {}, "av1": {}},
	"mp4":      {"h264": {}, "hevc": {}, "av1": {}},
	"webm":     {"av1": {}},
}

var containerAudioCodecs = map[string]map[string]struct{}{
	"matroska": {"aac": {}, "ac3": {}, "eac3": {}, "flac": {}, "opus": {}, "vorbis": {}},
	"mp4":      {"aac": {}, "ac3": {}, "eac3": {}},
	"webm":     {"opus": {}, "vorbis": {}},
}

var pixelFormats = map[string]struct{}{
	"yuv420p": {}, "yuv420p10le": {}, "yuv422p": {}, "yuv422p10le": {},
	"yuv444p": {}, "yuv444p10le": {}, "nv12": {}, "p010le": {},
}

func ValidateTranscodeProfile(profile TranscodeProfile) error {
	if strings.TrimSpace(profile.Name) == "" {
		return invalidTranscodeField("name", "must not be blank")
	}
	encoders, ok := codecEncoders[profile.VideoCodec]
	if !ok {
		return invalidTranscodeField("videoCodec", "must be h264, hevc, or av1")
	}
	if _, ok := encoders[profile.Encoder]; !ok {
		return invalidTranscodeField("encoder", "is not compatible with the selected video codec")
	}
	extension, ok := containerExtensions[profile.Container]
	if !ok {
		return invalidTranscodeField("container", "must be matroska, mp4, or webm")
	}
	if allowed := containerVideoCodecs[profile.Container]; allowed == nil {
		return invalidTranscodeField("container", "has no supported video codecs")
	} else if _, ok := allowed[profile.VideoCodec]; !ok {
		return invalidTranscodeField("container", "is not compatible with the selected video codec")
	}
	if profile.FileExtension != extension {
		return invalidTranscodeField("fileExtension", "does not match the selected container")
	}
	if err := validateQuality(profile); err != nil {
		return err
	}
	if err := validateAudio(profile); err != nil {
		return err
	}
	if !validPreset(profile.Encoder, profile.Preset) {
		return invalidTranscodeField("preset", "is not allowed for the selected encoder")
	}
	if _, ok := pixelFormats[profile.PixelFormat]; !ok {
		return invalidTranscodeField("pixelFormat", "is not in the supported pixel format allowlist")
	}
	if profile.ThreadCount < 0 || profile.ThreadCount > 256 {
		return invalidTranscodeField("threadCount", "must be between 0 and 256")
	}
	if profile.MaxConcurrency < 1 || profile.MaxConcurrency > 64 {
		return invalidTranscodeField("maxConcurrency", "must be between 1 and 64")
	}
	return nil
}

func BuildTranscodeFFmpegArgs(profile TranscodeProfile, inputPath, temporaryOutput string) ([]string, error) {
	if err := ValidateTranscodeProfile(profile); err != nil {
		return nil, err
	}
	if strings.TrimSpace(inputPath) == "" {
		return nil, invalidTranscodeField("inputPath", "must not be blank")
	}
	if strings.TrimSpace(temporaryOutput) == "" {
		return nil, invalidTranscodeField("temporaryOutput", "must not be blank")
	}
	outputExtension := strings.TrimPrefix(strings.ToLower(filepath.Ext(temporaryOutput)), ".")
	if outputExtension != profile.FileExtension {
		return nil, invalidTranscodeField("temporaryOutput", "must retain the profile file extension")
	}

	qualityOption := "-" + profile.QualityMode
	qualityValue := formatProfileNumber(profile.QualityValue)
	if profile.QualityMode == "bitrate" {
		qualityOption = "-b:v"
		qualityValue += "k"
	}
	presetOption := "-preset"
	if profile.Encoder == "libaom-av1" {
		presetOption = "-cpu-used"
	}
	args := []string{
		"-y", "-i", inputPath,
		"-map", "0:v:0", "-map", "0:a?", "-sn",
		"-c:v", profile.Encoder, qualityOption, qualityValue,
		presetOption, profile.Preset,
		"-pix_fmt", profile.PixelFormat,
		"-threads", strconv.Itoa(profile.ThreadCount),
		"-c:a",
	}
	if profile.AudioPolicy == "copy" {
		args = append(args, "copy")
	} else {
		args = append(args, profile.AudioCodec)
	}
	args = append(args, "-f", profile.Container, temporaryOutput)
	return args, nil
}

func BuildTranscodeProbeExpectation(profile TranscodeProfile) (TranscodeProbeExpectation, error) {
	if err := ValidateTranscodeProfile(profile); err != nil {
		return TranscodeProbeExpectation{}, err
	}
	containers := map[string][]string{
		"matroska": {"matroska", "webm"},
		"mp4":      {"mov", "mp4", "m4a", "3gp", "3g2", "mj2"},
		"webm":     {"matroska", "webm"},
	}
	expectedAudioCodec := ""
	if profile.AudioPolicy == "transcode" {
		expectedAudioCodec = profile.AudioCodec
	}
	return TranscodeProbeExpectation{
		VideoCodec:         profile.VideoCodec,
		ContainerNames:     containers[profile.Container],
		FileExtension:      profile.FileExtension,
		AudioPolicy:        profile.AudioPolicy,
		ExpectedAudioCodec: expectedAudioCodec,
	}, nil
}

func validateQuality(profile TranscodeProfile) error {
	if math.IsNaN(profile.QualityValue) || math.IsInf(profile.QualityValue, 0) {
		return invalidTranscodeField("qualityValue", "must be a finite number")
	}
	if math.Abs(profile.QualityValue*1000-math.Round(profile.QualityValue*1000)) > 1e-9 {
		return invalidTranscodeField("qualityValue", "must use at most three decimal places")
	}
	switch profile.QualityMode {
	case "crf":
		if strings.HasSuffix(profile.Encoder, "_nvenc") {
			return invalidTranscodeField("qualityMode", "CRF is not supported by the selected encoder")
		}
		if profile.QualityValue < 0 || profile.QualityValue > 63 {
			return invalidTranscodeField("qualityValue", "CRF must be between 0 and 63")
		}
	case "cq":
		if !strings.HasSuffix(profile.Encoder, "_nvenc") {
			return invalidTranscodeField("qualityMode", "CQ is only supported by configured NVENC encoders")
		}
		if profile.QualityValue < 0 || profile.QualityValue > 63 {
			return invalidTranscodeField("qualityValue", "CQ must be between 0 and 63")
		}
	case "bitrate":
		if profile.QualityValue <= 0 || profile.QualityValue > 1_000_000 {
			return invalidTranscodeField("qualityValue", "bitrate must be between 1 and 1000000 kbps")
		}
	default:
		return invalidTranscodeField("qualityMode", "must be crf, cq, or bitrate")
	}
	return nil
}

func validateAudio(profile TranscodeProfile) error {
	switch profile.AudioPolicy {
	case "copy":
		if profile.AudioCodec != "" {
			return invalidTranscodeField("audioCodec", "must be empty when audio policy is copy")
		}
	case "transcode":
		if _, ok := containerAudioCodecs[profile.Container][profile.AudioCodec]; !ok {
			return invalidTranscodeField("audioCodec", "is not compatible with the selected container")
		}
	default:
		return invalidTranscodeField("audioPolicy", "must be copy or transcode")
	}
	return nil
}

func validPreset(encoder, preset string) bool {
	switch encoder {
	case "libx264", "libx265":
		return containsString([]string{
			"ultrafast", "superfast", "veryfast", "faster", "fast", "medium",
			"slow", "slower", "veryslow", "placebo",
		}, preset)
	case "libsvtav1":
		value, err := strconv.Atoi(preset)
		return err == nil && value >= 0 && value <= 13
	case "libaom-av1":
		value, err := strconv.Atoi(preset)
		return err == nil && value >= 0 && value <= 8
	case "h264_nvenc", "hevc_nvenc", "av1_nvenc":
		return containsString([]string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "slow", "medium", "fast"}, preset)
	default:
		return false
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func formatProfileNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func invalidTranscodeField(field, reason string) *TranscodeProfileError {
	return &TranscodeProfileError{Field: field, Reason: reason}
}
