package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

type systemMetricsStub struct {
	snapshot domain.SystemMetricsSnapshot
	paths    []string
}

func (stub *systemMetricsStub) Snapshot() domain.SystemMetricsSnapshot {
	return stub.snapshot
}

func (stub *systemMetricsStub) SetDiskPaths(paths []string) {
	stub.paths = append([]string(nil), paths...)
}

func TestGetDashboardSystemMetricsMapsAvailableAndMissingSamples(t *testing.T) {
	firstAt := time.Date(2026, time.July, 26, 4, 0, 0, 0, time.UTC)
	secondAt := firstAt.Add(2 * time.Second)
	cpu := 42.5
	memory := 50.25
	receive := 4096.0
	stub := &systemMetricsStub{snapshot: domain.SystemMetricsSnapshot{
		SampledAt:             secondAt,
		SampleIntervalSeconds: 2,
		HistoryWindowSeconds:  120,
		Availability: domain.SystemMetricsAvailability{
			CPU: true, Memory: true, Network: true, DiskIO: false, DiskCapacity: true,
		},
		Samples: []domain.SystemMetricSample{
			{SampledAt: firstAt, CPUUsedPercent: &cpu},
			{SampledAt: secondAt, CPUUsedPercent: &cpu, MemoryUsedPercent: &memory, NetworkReceiveBytesPerSecond: &receive},
		},
		Memory: &domain.SystemMemoryUsage{UsedBytes: 8_000, TotalBytes: 16_000},
		Disks:  []domain.SystemDiskUsage{{Path: "D:", UsedBytes: 60_000, TotalBytes: 100_000, UsedPercent: 60}},
	}}
	authentication := &authenticationStub{authenticated: domain.Session{User: domain.AdminUser{Username: "admin"}}}
	handler := NewHandler(NewServer(readinessStub{}, WithAuthentication(authentication, false), WithSystemMetrics(stub)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/system-metrics", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid-token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body SystemMetricsSnapshot
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.SampleIntervalSeconds != 2 || body.HistoryWindowSeconds != 120 || len(body.Samples) != 2 {
		t.Fatalf("response = %#v", body)
	}
	if body.Samples[0].MemoryUsedPercent != nil || body.Samples[0].NetworkReceiveBytesPerSecond != nil {
		t.Fatalf("missing first-sample metrics were fabricated: %#v", body.Samples[0])
	}
	if body.Samples[1].CpuUsedPercent == nil || *body.Samples[1].CpuUsedPercent != 42.5 || body.Samples[1].NetworkReceiveBytesPerSecond == nil || *body.Samples[1].NetworkReceiveBytesPerSecond != 4096 {
		t.Fatalf("second sample = %#v", body.Samples[1])
	}
	if body.Availability.DiskIO || !body.Availability.DiskCapacity || len(body.Disks) != 1 || body.Disks[0].Path != "D:" {
		t.Fatalf("availability/disks = %#v/%#v", body.Availability, body.Disks)
	}
}

func TestGetDashboardSystemMetricsRequiresSession(t *testing.T) {
	stub := &systemMetricsStub{}
	handler := NewHandler(NewServer(
		readinessStub{},
		WithAuthentication(&authenticationStub{}, false),
		WithSystemMetrics(stub),
	))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/system-metrics", nil)
	request.Header.Set("X-Request-Id", "metrics-auth-required")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body ApiError
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "unauthorized" || body.RequestId != "metrics-auth-required" {
		t.Fatalf("body = %#v", body)
	}
}

func TestGetDashboardSystemMetricsReturnsStructuredUnavailableResponseWithoutCollector(t *testing.T) {
	handler := NewHandler(NewServer(readinessStub{}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/system-metrics", nil)
	request.Header.Set("X-Request-Id", "metrics-unavailable")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body ApiError
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "service_unavailable" || body.Details["dependency"] != "system_metrics" {
		t.Fatalf("body = %#v", body)
	}
}

var _ SystemMetricsService = (*systemMetricsStub)(nil)
