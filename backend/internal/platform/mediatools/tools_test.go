package mediatools

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
)

type executorStub struct {
	outputName string
	outputArgs []string
	output     []byte
	outputErr  error
	runName    string
	runArgs    []string
	runErr     error
}

func (stub *executorStub) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	stub.outputName = name
	stub.outputArgs = args
	return stub.output, stub.outputErr
}

func (stub *executorStub) Run(_ context.Context, name string, args ...string) error {
	stub.runName = name
	stub.runArgs = args
	return stub.runErr
}

func TestProbeUsesExactFFprobeContractAndMapsMetadata(t *testing.T) {
	executor := &executorStub{output: []byte(`{
		"streams":[
			{"index":0,"codec_name":"h264","codec_type":"video","disposition":{"default":1,"forced":0},"tags":{}},
			{"index":3,"codec_name":"ass","codec_type":"subtitle","disposition":{"default":0,"forced":1},"tags":{"language":"chi","title":"简体中文"}},
			{"index":4,"codec_name":"mov_text","codec_type":"subtitle","disposition":{"default":0,"forced":0},"tags":{"language":"zho","handler_name":"繁體中文"}}
		],
		"format":{"format_name":"matroska,webm"}
	}`)}
	probe, err := New(executor).Probe(context.Background(), "ffprobe-custom", "/media/source.mkv")
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	wantArgs := []string{"-v", "error", "-show_streams", "-show_format", "-of", "json", "/media/source.mkv"}
	if executor.outputName != "ffprobe-custom" || !reflect.DeepEqual(executor.outputArgs, wantArgs) {
		t.Fatalf("ffprobe command = %q %#v", executor.outputName, executor.outputArgs)
	}
	if !reflect.DeepEqual(probe.FormatNames, []string{"matroska", "webm"}) || len(probe.Streams) != 3 || probe.Streams[1].Language != "chi" || probe.Streams[1].Title != "简体中文" || !probe.Streams[1].Forced || probe.Streams[2].Title != "繁體中文" {
		t.Fatalf("probe = %#v", probe)
	}
}

func TestRunFFmpegForwardsExactArguments(t *testing.T) {
	executor := &executorStub{}
	args := []string{"-y", "-i", "input.mkv", "output.mp4"}
	if err := New(executor).RunFFmpeg(context.Background(), "ffmpeg-custom", args); err != nil {
		t.Fatalf("RunFFmpeg() error = %v", err)
	}
	if executor.runName != "ffmpeg-custom" || !reflect.DeepEqual(executor.runArgs, args) {
		t.Fatalf("ffmpeg command = %q %#v", executor.runName, executor.runArgs)
	}
}

func TestLimitedBufferRejectsOversizedOutputWithoutGrowingPastLimit(t *testing.T) {
	var destination bytes.Buffer
	writer := &limitedBuffer{buffer: &destination, limit: 4}
	written, err := writer.Write([]byte("123456"))
	if err != nil || written != 6 {
		t.Fatalf("Write() = %d, %v", written, err)
	}
	if destination.String() != "1234" || !writer.truncated {
		t.Fatalf("limited output = %q, truncated=%t", destination.String(), writer.truncated)
	}
}

func TestProbeRejectsInvalidJSON(t *testing.T) {
	_, err := New(&executorStub{output: []byte("not-json")}).Probe(context.Background(), "ffprobe", "input.mkv")
	if err == nil {
		t.Fatal("Probe() error = nil")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("Probe() error = %v", err)
	}
}
