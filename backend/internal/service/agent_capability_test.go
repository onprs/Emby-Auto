package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestAgentCapabilityAutomaticUsesBinaryCapabilitySemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		settings   domain.AgentSettings
		capability domain.AgentCapability
		want       bool
	}{
		{
			name:       "missing RSS mode defaults off",
			settings:   domain.AgentSettings{},
			capability: domain.AgentCapabilityRSSReleaseAdjudication, want: false,
		},
		{
			name: "RSS off", settings: domain.AgentSettings{RSSCoordinateMode: domain.AgentResolutionOff},
			capability: domain.AgentCapabilityRSSReleaseAdjudication, want: false,
		},
		{
			name: "legacy RSS suggest is enabled", settings: domain.AgentSettings{RSSCoordinateMode: domain.AgentResolutionSuggest},
			capability: domain.AgentCapabilityRSSReleaseAdjudication, want: true,
		},
		{
			name: "download enabled", settings: domain.AgentSettings{DownloadFileSelectionMode: domain.AgentResolutionValidatedAuto},
			capability: domain.AgentCapabilityDownloadFileResolution, want: true,
		},
		{
			name: "legacy download suggest is enabled", settings: domain.AgentSettings{DownloadFileSelectionMode: domain.AgentResolutionSuggest},
			capability: domain.AgentCapabilityDownloadFileResolution, want: true,
		},
		{
			name: "mapping capability is authoritative", settings: domain.AgentSettings{EpisodeMappingEnabled: true, AllowAutomaticEpisodeMapping: false},
			capability: domain.AgentCapabilityEpisodeMapping, want: true,
		},
		{
			name: "catalog remains advisory", settings: domain.AgentSettings{CatalogMatchEnabled: true},
			capability: domain.AgentCapabilityCatalogCandidate, want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := agentCapabilityAutomatic(test.settings, test.capability); got != test.want {
				t.Fatalf("agentCapabilityAutomatic() = %t, want %t", got, test.want)
			}
		})
	}
}

type deterministicAgentConfigurationStub struct {
	configuration domain.Configuration
}

func (stub deterministicAgentConfigurationStub) Load(context.Context) (domain.Configuration, error) {
	return stub.configuration, nil
}

func (deterministicAgentConfigurationStub) ResolveSecret(context.Context, string) (string, error) {
	return "", domain.ErrNotFound
}

type deterministicAgentCatalogStub struct {
	automaticEnabled bool
	resolved         bool
	policyCalls      int
	calls            int
}

func (stub *deterministicAgentCatalogStub) AutomaticEpisodeMappingEnabled(context.Context, uuid.UUID) (bool, error) {
	stub.policyCalls++
	return stub.automaticEnabled, nil
}

func (stub *deterministicAgentCatalogStub) TryDeterministicEpisodeMapping(context.Context, uuid.UUID) (bool, error) {
	stub.calls++
	return stub.resolved, nil
}

func (*deterministicAgentCatalogStub) PreviewEpisodeMapping(context.Context, domain.EpisodeMappingPlanInput) (domain.EpisodeMappingPreview, error) {
	return domain.EpisodeMappingPreview{}, nil
}

func (*deterministicAgentCatalogStub) ApplyAgentEpisodeMapping(context.Context, domain.AgentResolution, domain.AgentEpisodeMappingProposal, domain.AgentProposalValidation) (domain.SavedEpisodeMapping, error) {
	return domain.SavedEpisodeMapping{}, nil
}

func TestAutomaticRSSPreacquisitionMappingUsesDeterministicScopeBeforeAgentConfiguration(t *testing.T) {
	mapping := &rssPreacquisitionMappingAgentStub{deterministic: true}
	service := NewAgentResolutionService(nil, nil, nil, deterministicAgentConfigurationStub{}, nil, nil).
		WithRSSPreacquisitionMappingAgent(mapping)

	_, err := service.CreateAutomatic(context.Background(), AutomaticAgentResolutionRequest{
		Capability: domain.AgentCapabilityRSSPreacquisitionMapping,
		ResourceID: uuid.MustParse("53000000-0000-4000-8000-000000000022"),
	})
	var serviceErr *Error
	if !errors.Is(err, ErrStateConflict) || !errors.As(err, &serviceErr) || serviceErr.Code != "agent_resolution_not_required" {
		t.Fatalf("CreateAutomatic() error = %#v, want deterministic no-Agent conflict", err)
	}
	if mapping.deterministicCalls != 1 {
		t.Fatalf("deterministic RSS Mapping calls = %d, want 1", mapping.deterministicCalls)
	}
}

func TestAutomaticEpisodeMappingUsesDeterministicLogicBeforeAgentConfiguration(t *testing.T) {
	catalog := &deterministicAgentCatalogStub{automaticEnabled: true, resolved: true}
	configuration := deterministicAgentConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Agent: domain.AgentSettings{
		Enabled: false, EpisodeMappingEnabled: false,
	}}}}
	service := NewAgentResolutionService(nil, nil, nil, configuration, catalog, nil)

	_, err := service.CreateAutomatic(context.Background(), AutomaticAgentResolutionRequest{
		Capability: domain.AgentCapabilityEpisodeMapping,
		ResourceID: uuid.MustParse("53000000-0000-4000-8000-000000000021"),
	})
	var serviceErr *Error
	if !errors.Is(err, ErrStateConflict) || !errors.As(err, &serviceErr) || serviceErr.Code != "agent_resolution_not_required" {
		t.Fatalf("CreateAutomatic() error = %#v, want deterministic no-Agent conflict", err)
	}
	if catalog.policyCalls != 1 || catalog.calls != 1 {
		t.Fatalf("automatic policy/deterministic Mapping calls = %d/%d, want 1/1", catalog.policyCalls, catalog.calls)
	}
}

func TestAutomaticEpisodeMappingFallsBackOnlyWhenAgentCapabilityIsEnabled(t *testing.T) {
	catalog := &deterministicAgentCatalogStub{automaticEnabled: true, resolved: false}
	configuration := deterministicAgentConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Agent: domain.AgentSettings{
		Enabled: false, EpisodeMappingEnabled: false,
	}}}}
	service := NewAgentResolutionService(nil, nil, nil, configuration, catalog, nil)

	_, err := service.CreateAutomatic(context.Background(), AutomaticAgentResolutionRequest{
		Capability: domain.AgentCapabilityEpisodeMapping,
		ResourceID: uuid.New(),
	})
	var serviceErr *Error
	if !errors.Is(err, ErrStateConflict) || !errors.As(err, &serviceErr) || serviceErr.Code != "agent_disabled" {
		t.Fatalf("CreateAutomatic() error = %#v, want Agent disabled conflict after deterministic miss", err)
	}
	if catalog.policyCalls != 1 || catalog.calls != 1 {
		t.Fatalf("automatic policy/deterministic Mapping calls = %d/%d, want 1/1", catalog.policyCalls, catalog.calls)
	}
}

func TestAutomaticEpisodeMappingStopsBeforeLogicAndAgentWhenSubscriptionPolicyIsDisabled(t *testing.T) {
	catalog := &deterministicAgentCatalogStub{automaticEnabled: false, resolved: true}
	configuration := deterministicAgentConfigurationStub{configuration: domain.Configuration{Settings: domain.RuntimeSettings{Agent: domain.AgentSettings{
		Enabled: true, EpisodeMappingEnabled: true,
	}}}}
	service := NewAgentResolutionService(nil, nil, nil, configuration, catalog, nil)

	_, err := service.CreateAutomatic(context.Background(), AutomaticAgentResolutionRequest{
		Capability: domain.AgentCapabilityEpisodeMapping,
		ResourceID: uuid.New(),
	})
	var serviceErr *Error
	if !errors.Is(err, ErrStateConflict) || !errors.As(err, &serviceErr) || serviceErr.Code != "automatic_episode_mapping_disabled" {
		t.Fatalf("CreateAutomatic() error = %#v, want disabled subscription conflict", err)
	}
	if catalog.policyCalls != 1 || catalog.calls != 0 {
		t.Fatalf("automatic policy/deterministic Mapping calls = %d/%d, want 1/0", catalog.policyCalls, catalog.calls)
	}
}
