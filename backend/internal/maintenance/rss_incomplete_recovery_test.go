package maintenance

import (
	"testing"

	"github.com/google/uuid"
)

func TestValidateIncompleteRSSRecoveryRequestRejectsUnsafeSelections(t *testing.T) {
	tests := []struct {
		name    string
		request IncompleteRSSRecoveryRequest
	}{
		{name: "missing subscription", request: IncompleteRSSRecoveryRequest{SourceEpisodes: []int32{2}}},
		{name: "missing episodes", request: IncompleteRSSRecoveryRequest{SubscriptionID: uuid.New()}},
		{name: "nonpositive episode", request: IncompleteRSSRecoveryRequest{SubscriptionID: uuid.New(), SourceEpisodes: []int32{0}}},
		{name: "duplicate episode", request: IncompleteRSSRecoveryRequest{SubscriptionID: uuid.New(), SourceEpisodes: []int32{2, 2}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateIncompleteRSSRecoveryRequest(test.request); err == nil {
				t.Fatal("ValidateIncompleteRSSRecoveryRequest() error = nil")
			}
		})
	}
}

func TestValidateIncompleteRSSRecoveryRequestAcceptsExplicitUniqueEpisodes(t *testing.T) {
	request := IncompleteRSSRecoveryRequest{SubscriptionID: uuid.New(), SourceEpisodes: []int32{2, 4, 5, 6}}
	if err := ValidateIncompleteRSSRecoveryRequest(request); err != nil {
		t.Fatalf("ValidateIncompleteRSSRecoveryRequest() error = %v", err)
	}
}
