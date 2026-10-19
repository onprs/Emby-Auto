//go:build integration

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	db "github.com/onprs/emby-auto/backend/db/sqlc"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/database"
	"github.com/onprs/emby-auto/backend/internal/testutil"
)

type agentLifecycleConfiguration struct {
	load func() (domain.Configuration, error)
}

func (configuration agentLifecycleConfiguration) Load(context.Context) (domain.Configuration, error) {
	return configuration.load()
}

func (agentLifecycleConfiguration) ResolveSecret(context.Context, string) (string, error) {
	return "test-key", nil
}

func TestAgentResolutionRechecksConfigurationAfterProviderCallIntegration(t *testing.T) {
	for _, test := range []struct {
		name            string
		change          func(*domain.Configuration)
		status          domain.AgentResolutionStatus
		loadError       bool
		resourceChanged bool
	}{
		{name: "unchanged", status: domain.AgentResolutionApplied},
		{name: "disabled", change: func(configuration *domain.Configuration) { configuration.Settings.Agent.Enabled = false }, status: domain.AgentResolutionCancelled},
		{name: "capability_disabled", change: func(configuration *domain.Configuration) {
			configuration.Settings.Agent.RSSCoordinateMode = domain.AgentResolutionOff
		}, status: domain.AgentResolutionCancelled},
		{name: "model_changed", change: func(configuration *domain.Configuration) { configuration.Settings.Agent.Model = "updated-model" }, status: domain.AgentResolutionExpired},
		{name: "configuration_version_changed", change: func(*domain.Configuration) {}, status: domain.AgentResolutionExpired},
		{name: "configuration_read_failure", loadError: true, status: domain.AgentResolutionProposed},
		{name: "resource_changed", resourceChanged: true, status: domain.AgentResolutionExpired},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, pool := testutil.NewMigratedPostgres(t)
			transactor := database.NewTransactor(pool)
			scheduler := NewOperationScheduler(transactor, &integrationJobInserter{})
			seriesID, subscriptionID, entryID := uuid.New(), uuid.New(), uuid.New()
			if _, err := testutil.ExecFixture(ctx, pool, `
INSERT INTO media_series (id, title) VALUES ($1, 'Agent lifecycle');
INSERT INTO rss_subscriptions (id, series_id, name, feed_url, enabled, poll_interval_seconds, source_season)
VALUES ($2, $1, 'Agent lifecycle', 'https://example.test/lifecycle.xml', true, 900, 1);
INSERT INTO rss_entries (id, subscription_id, identity_key, title, download_uri, downloadable, rejection_reasons)
VALUES ($3, $2, 'lifecycle-entry', 'Unnumbered release', 'https://example.test/release.torrent', false, ARRAY['episode_not_detected']::text[]);
`, seriesID, subscriptionID, entryID); err != nil {
				t.Fatal(err)
			}
			var providerDone atomic.Bool
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Error(err)
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				calls.Add(1)
				if test.resourceChanged {
					if _, err := pool.Exec(ctx, `UPDATE rss_entries SET title = 'Changed unnumbered release' WHERE id = $1`, entryID); err != nil {
						t.Error(err)
						writer.WriteHeader(http.StatusInternalServerError)
						return
					}
				}
				providerDone.Store(true)
				proposal, _ := json.Marshal(domain.AgentRSSCoordinateProposal{
					EntryID: entryID, SourceSeason: 1, SourceEpisode: 13, EvidenceCodes: []string{"scoped_evidence"}, Decision: "resolved",
				})
				_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []any{map[string]any{
					"finish_reason": "tool_calls", "message": map[string]any{
						"role": "assistant", "tool_calls": []any{map[string]any{
							"id": "submit-1", "type": "function", "function": map[string]any{"name": "submit_rss_coordinate", "arguments": string(proposal)},
						}},
					},
				}}})
			}))
			defer server.Close()
			configuration := domain.Configuration{Version: 1}
			configuration.Settings.Agent = domain.DefaultAgentSettings()
			configuration.Settings.Agent.Enabled = true
			configuration.Settings.Agent.UseNetworkProxy = false
			configuration.Settings.Agent.RSSCoordinateMode = domain.AgentResolutionValidatedAuto
			configuration.Settings.Agent.BaseURL = server.URL + "/v1"
			configuration.Settings.Agent.Model = "test-model"
			loadFailure := errors.New("temporary configuration read failure")
			resolver := agentLifecycleConfiguration{load: func() (domain.Configuration, error) {
				if providerDone.Load() && test.loadError {
					return domain.Configuration{}, loadFailure
				}
				current := configuration
				if providerDone.Load() && test.change != nil {
					current.Version++
					test.change(&current)
				}
				return current, nil
			}}
			service := NewAgentResolutionService(db.New(pool), transactor, scheduler, resolver, nil, nil)
			created, err := service.CreateAutomatic(ctx, AutomaticAgentResolutionRequest{Capability: domain.AgentCapabilityRSSCoordinate, ResourceID: entryID})
			if err != nil {
				t.Fatal(err)
			}
			runErr := service.Run(ctx, created.Operation)
			if (test.loadError && !errors.Is(runErr, loadFailure)) || (!test.loadError && runErr != nil) {
				t.Fatalf("Run() error = %v", runErr)
			}
			resolution, err := service.Get(ctx, created.Resolution.ID)
			if err != nil {
				t.Fatal(err)
			}
			var episode *int32
			if err := pool.QueryRow(ctx, `SELECT source_episode FROM rss_entries WHERE id = $1`, entryID).Scan(&episode); err != nil {
				t.Fatal(err)
			}
			if resolution.Status != test.status || calls.Load() != 1 {
				t.Fatalf("resolution status = %s, provider calls = %d, want %s and one call", resolution.Status, calls.Load(), test.status)
			}
			if test.status == domain.AgentResolutionApplied {
				if episode == nil || *episode != 13 {
					t.Fatalf("applied source episode = %v", episode)
				}
			} else if episode != nil {
				t.Fatalf("configuration change still applied source episode %d", *episode)
			}
			if test.loadError {
				providerDone.Store(false)
				if err := service.Run(ctx, created.Operation); err != nil {
					t.Fatalf("retry persisted proposal: %v", err)
				}
				retried, err := service.Get(ctx, created.Resolution.ID)
				if err != nil || retried.Status != domain.AgentResolutionApplied || calls.Load() != 1 {
					t.Fatalf("retry status = %s, provider calls = %d, error = %v", retried.Status, calls.Load(), err)
				}
			}
		})
	}
}

var _ AgentResolutionConfiguration = agentLifecycleConfiguration{}
