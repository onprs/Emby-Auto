package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]
	if controlEnabled("media_slow") {
		time.Sleep(10 * time.Second)
	}
	if contains(args, "-version") {
		fmt.Println("Emby Auto E2E media fixture 1.0")
		return
	}
	if contains(args, "-show_streams") {
		codec := "h264"
		if controlEnabled("media_invalid") {
			codec = "mpeg2video"
		}
		fmt.Printf(`{"streams":[{"index":0,"codec_name":%q,"codec_type":"video","disposition":{"default":1,"forced":0},"tags":{"language":"und"}},{"index":1,"codec_name":"aac","codec_type":"audio","disposition":{"default":1,"forced":0},"tags":{"language":"ja"}}],"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2"}}`, codec)
		return
	}
	if len(args) == 0 {
		fatal("media fixture requires FFmpeg arguments")
	}
	output := args[len(args)-1]
	if strings.EqualFold(filepath.Ext(output), ".ass") {
		content := []byte("[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\nStyle: Default,Arial,24,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:00.00,0:00:02.00,Default,,0,0,0,,Fixture subtitle\n")
		if err := os.WriteFile(output, content, 0o644); err != nil {
			fatal(err.Error())
		}
		return
	}
	input := argumentAfter(args, "-i")
	if input == "" {
		fatal("FFmpeg fixture did not receive an input")
	}
	if err := copyFile(input, output); err != nil {
		fatal(err.Error())
	}
}

func controlEnabled(name string) bool {
	controlDir := strings.TrimSpace(os.Getenv("E2E_CONTROL_DIR"))
	if controlDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(controlDir, name))
	return err == nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func argumentAfter(values []string, target string) string {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == target {
			return values[index+1]
		}
	}
	return ""
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func fatal(message string) {
	_, _ = fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
