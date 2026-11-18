package maintenance

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestValidateHistoryRetentionRequest(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	valid := HistoryRetentionRequest{
		Before: now.Add(-48 * time.Hour), BatchSize: 100, MaxBatches: 5,
	}
	if err := ValidateHistoryRetentionRequest(valid, now); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	tests := []struct {
		name    string
		mutate  func(*HistoryRetentionRequest)
		message string
	}{
		{name: "zero before", mutate: func(request *HistoryRetentionRequest) { request.Before = time.Time{} }, message: "at least"},
		{name: "recent before", mutate: func(request *HistoryRetentionRequest) { request.Before = now.Add(-time.Hour) }, message: "at least"},
		{name: "zero batch", mutate: func(request *HistoryRetentionRequest) { request.BatchSize = 0 }, message: "batch size"},
		{name: "oversized batch", mutate: func(request *HistoryRetentionRequest) { request.BatchSize = MaxHistoryRetentionBatchSize + 1 }, message: "batch size"},
		{name: "zero batches", mutate: func(request *HistoryRetentionRequest) { request.MaxBatches = 0 }, message: "max batches"},
		{name: "too many batches", mutate: func(request *HistoryRetentionRequest) { request.MaxBatches = MaxHistoryRetentionBatches + 1 }, message: "max batches"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			err := ValidateHistoryRetentionRequest(request, now)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want containing %q", err, test.message)
			}
		})
	}
}

func TestHistoryRetentionRequiresConfiguration(t *testing.T) {
	var retention *HistoryRetention
	if _, err := retention.Run(context.Background(), HistoryRetentionRequest{}); err == nil {
		t.Fatal("nil retention returned no error")
	}
}
