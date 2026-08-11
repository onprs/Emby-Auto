package main

import (
	"reflect"
	"testing"
)

func TestParseEpisodesSortsExplicitSelection(t *testing.T) {
	got, err := parseEpisodes("11,2,9,4")
	if err != nil {
		t.Fatalf("parseEpisodes() error = %v", err)
	}
	want := []int32{2, 4, 9, 11}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseEpisodes() = %v, want %v", got, want)
	}
}

func TestParseEpisodesRejectsBlankAndInvalidValues(t *testing.T) {
	for _, value := range []string{"", "2,", "0", "one"} {
		if _, err := parseEpisodes(value); err == nil {
			t.Fatalf("parseEpisodes(%q) error = nil", value)
		}
	}
}
