package main

import "testing"

func TestSafeKindDoesNotExposeArbitraryDatabaseText(t *testing.T) {
	tests := map[string]string{
		"agent.resolve":       "agent.resolve",
		"download.enqueue":    "download.enqueue",
		"unexpected value/id": "other",
		"line\nbreak":         "other",
	}
	for input, expected := range tests {
		if actual := safeKind(input); actual != expected {
			t.Fatalf("safeKind(%q) = %q, want %q", input, actual, expected)
		}
	}
}
