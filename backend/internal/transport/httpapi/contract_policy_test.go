package httpapi

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

type contractOperation struct {
	method    string
	path      string
	operation *openapi3.Operation
	item      *openapi3.PathItem
}

func TestContractPolicy(t *testing.T) {
	document, err := GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger() error = %v", err)
	}
	operations := contractOperations(document)

	t.Run("operation tags are declared", func(t *testing.T) {
		declared := make(map[string]struct{}, len(document.Tags))
		for _, tag := range document.Tags {
			declared[tag.Name] = struct{}{}
		}
		for _, entry := range operations {
			if len(entry.operation.Tags) == 0 {
				t.Errorf("%s has no tag", entry)
			}
			for _, tag := range entry.operation.Tags {
				if _, ok := declared[tag]; !ok {
					t.Errorf("%s uses undeclared tag %q", entry, tag)
				}
			}
		}
	})

	t.Run("session operations declare unauthorized response", func(t *testing.T) {
		publicWhitelist := map[string]struct{}{
			"GetHealthLive": {}, "GetHealthReady": {}, "GetSetupStatus": {},
			"InitializeSetup": {}, "Login": {},
		}
		seenPublic := make(map[string]struct{}, len(publicWhitelist))
		for _, entry := range operations {
			public := entry.operation.Security != nil && len(*entry.operation.Security) == 0
			if public {
				if _, ok := publicWhitelist[entry.operation.OperationID]; !ok {
					t.Errorf("%s is public but is not whitelisted", entry)
				}
				seenPublic[entry.operation.OperationID] = struct{}{}
				continue
			}
			if entry.operation.Responses == nil || entry.operation.Responses.Status(http.StatusUnauthorized) == nil {
				t.Errorf("%s inherits session security without a 401 response", entry)
			}
		}
		for operationID := range publicWhitelist {
			if _, ok := seenPublic[operationID]; !ok {
				t.Errorf("public operation %q no longer explicitly declares security: []", operationID)
			}
		}
	})

	t.Run("accepted commands reference standard idempotency key", func(t *testing.T) {
		const idempotencyRef = "#/components/parameters/IdempotencyKey"
		for _, entry := range operations {
			if entry.operation.Responses == nil || entry.operation.Responses.Status(http.StatusAccepted) == nil {
				continue
			}
			found := false
			for _, parameter := range append(entry.item.Parameters, entry.operation.Parameters...) {
				if parameter != nil && parameter.Ref == idempotencyRef {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s returns 202 without %s", entry, idempotencyRef)
			}
			if entry.operation.Responses.Status(http.StatusConflict) == nil {
				t.Errorf("%s returns 202 without an idempotency conflict 409 response", entry)
			}
		}
	})

	t.Run("removed Agent commands stay absent", func(t *testing.T) {
		removed := []string{
			"/api/v1/agent/resolutions/{resolutionId}/accept",
			"/api/v1/agent/resolutions/{resolutionId}/reject",
			"/api/v1/agent/resolutions/{resolutionId}/retry",
			"/api/v1/rss/entries/{entryId}/agent-resolution",
			"/api/v1/rss/adjudication-batches/{batchId}/agent-resolution",
			"/api/v1/downloads/{downloadId}/agent-resolution",
			"/api/v1/acquisitions/{acquisitionId}/episode-mapping/agent-resolution",
			"/api/v1/acquisitions/{acquisitionId}/catalog/agent-resolution",
		}
		for _, path := range removed {
			if document.Paths.Value(path) != nil {
				t.Errorf("removed Agent command path is present: %s", path)
			}
		}
	})

	t.Run("plaintext secrets have one no-store response surface", func(t *testing.T) {
		const revealOperationID = "RevealConfigurationSecrets"
		for _, entry := range operations {
			if entry.operation.Responses == nil {
				continue
			}
			for status, responseRef := range entry.operation.Responses.Map() {
				if responseRef == nil || responseRef.Value == nil {
					continue
				}
				for _, mediaType := range responseRef.Value.Content {
					if mediaType != nil && containsPlaintextSecret(mediaType.Schema, map[*openapi3.Schema]bool{}) && entry.operation.OperationID != revealOperationID {
						t.Errorf("%s response %s exposes a plaintext configuration secret", entry, status)
					}
				}
			}
		}

		reveal := document.Paths.Value("/api/v1/config/secrets/reveal")
		if reveal == nil || reveal.Post == nil || reveal.Post.OperationID != revealOperationID {
			t.Fatal("the dedicated secret reveal operation is missing")
		}
		if reveal.Post.Security != nil && len(*reveal.Post.Security) == 0 {
			t.Fatal("secret reveal must inherit authenticated session security")
		}
		if !strings.Contains(reveal.Post.Description, "Authenticated-administrator exception") {
			t.Fatal("secret reveal must document its dedicated authenticated-administrator purpose")
		}
		for status, responseRef := range reveal.Post.Responses.Map() {
			if !responseHasNoStore(responseRef) {
				t.Errorf("secret reveal response %s does not require Cache-Control: no-store, max-age=0", status)
			}
		}
	})
}

func contractOperations(document *openapi3.T) []contractOperation {
	operations := make([]contractOperation, 0)
	for path, item := range document.Paths.Map() {
		for method, operation := range item.Operations() {
			operations = append(operations, contractOperation{
				method: method, path: path, operation: operation, item: item,
			})
		}
	}
	sort.Slice(operations, func(left, right int) bool {
		if operations[left].path == operations[right].path {
			return operations[left].method < operations[right].method
		}
		return operations[left].path < operations[right].path
	})
	return operations
}

func (entry contractOperation) String() string {
	return fmt.Sprintf("%s %s (%s)", entry.method, entry.path, entry.operation.OperationID)
}

func containsPlaintextSecret(ref *openapi3.SchemaRef, seen map[*openapi3.Schema]bool) bool {
	if ref == nil || ref.Value == nil || seen[ref.Value] {
		return false
	}
	if ref.Ref == "#/components/schemas/RevealedSecrets" {
		return true
	}
	seen[ref.Value] = true
	schema := ref.Value
	secretProperties := map[string]struct{}{
		"qbPassword": {}, "embyApiKey": {}, "tmdbApiToken": {}, "agentApiKey": {},
	}
	for name, property := range schema.Properties {
		if _, secret := secretProperties[name]; secret && property != nil && property.Value != nil && property.Value.Type.Includes("string") {
			return true
		}
		if containsPlaintextSecret(property, seen) {
			return true
		}
	}
	for _, nested := range append(append(schema.OneOf, schema.AnyOf...), schema.AllOf...) {
		if containsPlaintextSecret(nested, seen) {
			return true
		}
	}
	return containsPlaintextSecret(schema.Items, seen) || containsPlaintextSecret(schema.AdditionalProperties.Schema, seen)
}

func responseHasNoStore(responseRef *openapi3.ResponseRef) bool {
	if responseRef == nil || responseRef.Value == nil {
		return false
	}
	headerRef := responseRef.Value.Headers["Cache-Control"]
	if headerRef == nil || headerRef.Value == nil || headerRef.Value.Schema == nil || headerRef.Value.Schema.Value == nil {
		return false
	}
	for _, value := range headerRef.Value.Schema.Value.Enum {
		if value == "no-store, max-age=0" {
			return true
		}
	}
	return false
}
