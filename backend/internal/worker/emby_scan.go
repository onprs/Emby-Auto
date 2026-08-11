package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/onprs/emby-auto/backend/internal/domain"
	"github.com/onprs/emby-auto/backend/internal/platform/emby"
)

const (
	embyScanPageSize = 500
	embyScanMaxItems = 1_000_000
)

type EmbyCatalogClient interface {
	Libraries(context.Context) ([]domain.EmbyLibraryCatalog, error)
	LibraryItems(context.Context, string, int, int) (emby.ItemPage, error)
}

type EmbyCatalogClientFactory func(emby.ClientOptions) (EmbyCatalogClient, error)

type EmbyCatalogStore interface {
	BeginScan(context.Context, domain.Operation) (domain.EmbyScan, error)
	CompleteScan(context.Context, domain.Operation, []domain.EmbyLibrarySnapshot) error
}

type EmbyScanHandler struct {
	configuration DownloadConfiguration
	store         EmbyCatalogStore
	newClient     EmbyCatalogClientFactory
}

func NewEmbyScanHandler(
	configuration DownloadConfiguration,
	store EmbyCatalogStore,
	newClient EmbyCatalogClientFactory,
) *EmbyScanHandler {
	return &EmbyScanHandler{configuration: configuration, store: store, newClient: newClient}
}

func (handler *EmbyScanHandler) Handle(ctx context.Context, operation domain.Operation) error {
	if operation.ResourceType != "emby_scan" || operation.ResourceID == uuid.Nil {
		return permanentFailure("invalid_emby_scan_operation", "emby.scan requires an Emby scan resource", nil)
	}
	if handler.configuration == nil || handler.store == nil || handler.newClient == nil {
		return permanentFailure("emby_scan_not_configured", "Emby scan handler dependencies are unavailable", nil)
	}
	scan, err := handler.store.BeginScan(ctx, operation)
	if err != nil {
		return retryableFailure("emby_scan_storage_unavailable", "the Emby scan run could not be started", err)
	}
	if scan.Status == domain.EmbyScanSucceeded {
		return nil
	}
	if scan.Status == domain.EmbyScanFailed || scan.Status == domain.EmbyScanCancelled {
		return permanentFailure("emby_scan_not_runnable", "the Emby scan run is terminal", nil)
	}
	configuration, err := handler.configuration.Load(ctx)
	if err != nil {
		return retryableFailure("configuration_unavailable", "runtime configuration is unavailable", err)
	}
	if strings.TrimSpace(configuration.Settings.Emby.URL) == "" {
		return permanentFailure("emby_not_configured", "Emby is not configured", nil)
	}
	apiKey, err := handler.configuration.ResolveSecret(ctx, domain.SecretEmbyAPIKey)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return permanentFailure("emby_not_configured", "the Emby API key is not configured", err)
		}
		return retryableFailure("configuration_unavailable", "the Emby API key is unavailable", err)
	}
	client, err := handler.newClient(emby.ClientOptions{
		BaseURL:        configuration.Settings.Emby.URL,
		APIKey:         apiKey,
		RequestTimeout: embyRequestTimeout,
	})
	if err != nil {
		return permanentFailure("emby_configuration_invalid", "the Emby configuration is invalid", err)
	}
	libraries, err := client.Libraries(ctx)
	if err != nil {
		return embyScanRequestFailure("list Emby libraries", err)
	}
	snapshots := make([]domain.EmbyLibrarySnapshot, 0, len(libraries))
	totalItems := 0
	for _, library := range libraries {
		snapshot := domain.EmbyLibrarySnapshot{Library: library, Items: []domain.EmbyLibraryItemCatalog{}}
		startIndex := 0
		expectedTotal := -1
		for {
			page, err := client.LibraryItems(ctx, library.EmbyID, startIndex, embyScanPageSize)
			if err != nil {
				return embyScanRequestFailure("list Emby library items", err)
			}
			if expectedTotal < 0 {
				expectedTotal = page.TotalRecordCount
			} else if expectedTotal != page.TotalRecordCount {
				return retryableFailure("emby_catalog_changed", "the Emby catalog changed while it was being scanned", nil)
			}
			if startIndex+len(page.Items) > expectedTotal {
				return retryableFailure("emby_catalog_invalid", "Emby returned inconsistent item pagination", nil)
			}
			snapshot.Items = append(snapshot.Items, page.Items...)
			startIndex += len(page.Items)
			totalItems += len(page.Items)
			if totalItems > embyScanMaxItems {
				return permanentFailure("emby_catalog_too_large", fmt.Sprintf("the Emby catalog exceeds %d supported items", embyScanMaxItems), nil)
			}
			if startIndex >= expectedTotal {
				break
			}
			if len(page.Items) == 0 {
				return retryableFailure("emby_catalog_invalid", "Emby returned an empty page before the catalog ended", nil)
			}
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := handler.store.CompleteScan(ctx, operation, snapshots); err != nil {
		message := err.Error()
		if strings.Contains(message, "duplicate") || strings.Contains(message, "incomplete") || strings.Contains(message, "does not match") {
			return permanentFailure("emby_catalog_invalid", "the Emby catalog response is invalid", err)
		}
		return retryableFailure("emby_scan_storage_unavailable", "the Emby catalog scan could not be persisted", err)
	}
	return nil
}

func embyScanRequestFailure(action string, err error) error {
	var httpErr *emby.HTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden:
			return permanentFailure("emby_authentication_failed", "Emby rejected the configured API key", err)
		case httpErr.StatusCode == http.StatusNotFound:
			return permanentFailure("emby_catalog_not_found", "an Emby catalog endpoint was not found", err)
		case httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= http.StatusInternalServerError:
			return retryableFailure("emby_scan_request_failed", action+" failed", err)
		}
	}
	return retryableFailure("emby_scan_request_failed", action+" failed", err)
}
