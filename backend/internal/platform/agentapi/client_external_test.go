//go:build external

package agentapi

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestExternalConnectivity(t *testing.T) {
	baseURL := os.Getenv("AGENT_TEST_BASE_URL")
	apiKey := os.Getenv("AGENT_TEST_API_KEY")
	model := os.Getenv("AGENT_TEST_MODEL")
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("Agent external test configuration is not available")
	}
	client, err := NewClient(ClientOptions{
		BaseURL: baseURL, APIKey: apiKey, Model: model, RequestTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.ConnectivityTest(ctx); err != nil {
		t.Fatalf("ConnectivityTest() error = %v", err)
	}
}
