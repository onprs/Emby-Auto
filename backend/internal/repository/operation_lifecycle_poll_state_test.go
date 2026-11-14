package repository

import (
	"testing"
	"time"
)

func TestDecodeRSSSubscriptionDeletionPollState(t *testing.T) {
	nextPollAt := "2026-11-14T08:31:17.924Z"
	cases := []struct {
		name        string
		payload     string
		wantOK      bool
		wantEnabled bool
		wantVersion int32
		wantNext    string
	}{
		{
			name:        "captured running subscription",
			payload:     `{"command":"delete","pollState":{"enabled":true,"nextPollAt":"` + nextPollAt + `","version":2}}`,
			wantOK:      true,
			wantEnabled: true,
			wantVersion: 2,
			wantNext:    nextPollAt,
		},
		{
			name:        "captured paused subscription",
			payload:     `{"command":"delete","pollState":{"enabled":false,"nextPollAt":null,"version":7}}`,
			wantOK:      true,
			wantEnabled: false,
			wantVersion: 7,
		},
		{
			name:    "legacy payload without snapshot",
			payload: `{"command":"delete","subscriptionId":"6f1d5f15-0f5c-4d16-9f5e-2a8f7c5f0f0a","deleteImported":false}`,
		},
		{
			name:    "captured snapshot with invalid version",
			payload: `{"command":"delete","pollState":{"enabled":true,"nextPollAt":null,"version":0}}`,
		},
		{
			name:    "malformed payload",
			payload: `{"command":`,
		},
		{
			name:    "empty payload",
			payload: "",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			state, ok := decodeRSSSubscriptionDeletionPollState([]byte(testCase.payload))
			if ok != testCase.wantOK {
				t.Fatalf("decode ok = %t, want %t", ok, testCase.wantOK)
			}
			if !ok {
				if state.Enabled || state.NextPollAt != nil || state.Version != 0 {
					t.Fatalf("legacy decode returned non-zero state: %#v", state)
				}
				return
			}
			if state.Enabled != testCase.wantEnabled || state.Version != testCase.wantVersion {
				t.Fatalf("decoded state = %#v, want enabled=%t version=%d", state, testCase.wantEnabled, testCase.wantVersion)
			}
			if testCase.wantNext == "" {
				if state.NextPollAt != nil {
					t.Fatalf("decoded next poll = %v, want nil", state.NextPollAt)
				}
				return
			}
			if state.NextPollAt == nil || state.NextPollAt.Format(time.RFC3339Nano) != testCase.wantNext {
				t.Fatalf("decoded next poll = %v, want %s", state.NextPollAt, testCase.wantNext)
			}
		})
	}
}
