package mediatools

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
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

func TestProbeWithPacketCountsParsesDurationFramesAndAttachedPictures(t *testing.T) {
	executor := &executorStub{output: []byte(`{
		"streams":[
			{"index":0,"codec_name":"png","codec_type":"video","disposition":{"attached_pic":0},"tags":{"DURATION":"00:00:00.040000000"}},
			{"index":1,"codec_name":"h264","codec_type":"video","duration":"4.023000","nb_frames":"100","nb_read_packets":"100","disposition":{"attached_pic":0},"tags":{}},
			{"index":2,"codec_name":"mjpeg","codec_type":"video","nb_frames":"1","nb_read_packets":"1","disposition":{"attached_pic":1},"tags":{}}
		],
		"format":{"format_name":"matroska,webm","duration":"4.063000"}
	}`)}
	probe, err := New(executor).ProbeWithPacketCounts(context.Background(), "ffprobe-custom", "/media/source.mkv")
	if err != nil {
		t.Fatalf("ProbeWithPacketCounts() error = %v", err)
	}
	wantArgs := []string{"-v", "error", "-count_packets", "-show_streams", "-show_format", "-of", "json", "/media/source.mkv"}
	if !reflect.DeepEqual(executor.outputArgs, wantArgs) {
		t.Fatalf("ffprobe args = %#v, want %#v", executor.outputArgs, wantArgs)
	}
	if probe.DurationSeconds != 4.063 || len(probe.Streams) != 3 {
		t.Fatalf("probe = %#v", probe)
	}
	cover := probe.Streams[0]
	if cover.DurationSeconds != 0.04 || cover.PacketCount != 0 {
		t.Fatalf("Matroska tag duration not parsed: %#v", cover)
	}
	main := probe.Streams[1]
	if main.DurationSeconds != 4.023 || main.FrameCount != 100 || main.PacketCount != 100 || main.AttachedPic {
		t.Fatalf("main stream = %#v", main)
	}
	stream, ok := domain.SelectMainVideoStream(probe)
	if !ok || stream.Index != 1 {
		t.Fatalf("SelectMainVideoStream() = %#v, %v, want index 1", stream, ok)
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
