package domain

import (
	"context"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// Minimal mock store and ResourceSync for testing

type MockResourceSyncStore struct {
	results []store.CountByResourceSyncOrgAndStatusResult
}

func (m *MockResourceSyncStore) Device() store.Device                       { return nil }
func (m *MockResourceSyncStore) EnrollmentRequest() store.EnrollmentRequest { return nil }
func (m *MockResourceSyncStore) CertificateSigningRequest() store.CertificateSigningRequest {
	return nil
}
func (m *MockResourceSyncStore) Fleet() store.Fleet                     { return nil }
func (m *MockResourceSyncStore) TemplateVersion() store.TemplateVersion { return nil }
func (m *MockResourceSyncStore) Repository() store.Repository           { return nil }
func (m *MockResourceSyncStore) ResourceSync() store.ResourceSync {
	return &MockResourceSync{results: m.results}
}
func (m *MockResourceSyncStore) Event() store.Event                  { return nil }
func (m *MockResourceSyncStore) Checkpoint() store.Checkpoint        { return nil }
func (m *MockResourceSyncStore) Organization() store.Organization    { return nil }
func (m *MockResourceSyncStore) AuthProvider() store.AuthProvider    { return nil }
func (m *MockResourceSyncStore) RunMigrations(context.Context) error { return nil }
func (m *MockResourceSyncStore) Close() error                        { return nil }
func (m *MockResourceSyncStore) CheckHealth(context.Context) error   { return nil }

type MockResourceSync struct {
	results []store.CountByResourceSyncOrgAndStatusResult
}

func (m *MockResourceSync) InitialMigration(ctx context.Context) error { return nil }
func (m *MockResourceSync) Create(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync, callbackEvent store.EventCallback) (*api.ResourceSync, error) {
	return nil, nil
}
func (m *MockResourceSync) Update(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync, callbackEvent store.EventCallback) (*api.ResourceSync, error) {
	return nil, nil
}
func (m *MockResourceSync) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync, callbackEvent store.EventCallback) (*api.ResourceSync, bool, error) {
	return nil, false, nil
}
func (m *MockResourceSync) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error) {
	return nil, nil
}
func (m *MockResourceSync) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*api.ResourceSyncList, error) {
	return nil, nil
}
func (m *MockResourceSync) Delete(ctx context.Context, orgId uuid.UUID, name string, callback store.RemoveOwnerCallback, callbackEvent store.EventCallback) error {
	return nil
}
func (m *MockResourceSync) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync, callbackEvent store.EventCallback) (*api.ResourceSync, error) {
	return nil, nil
}
func (m *MockResourceSync) Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error) {
	return 0, nil
}
func (m *MockResourceSync) CountByOrgAndStatus(ctx context.Context, orgId *uuid.UUID, status *string) ([]store.CountByResourceSyncOrgAndStatusResult, error) {
	return m.results, nil
}

func TestResourceSyncCollectorGroupByOrgAndStatus(t *testing.T) {
	mockResults := []store.CountByResourceSyncOrgAndStatusResult{
		{OrgID: "org1", Status: "Ready", Count: 2},
		{OrgID: "org1", Status: "Progressing", Count: 1},
		{OrgID: "org2", Status: "Ready", Count: 3},
	}

	mockStore := &MockResourceSyncStore{results: mockResults}
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	config := config.NewDefault()
	if config.Metrics == nil || config.Metrics.ResourceSyncCollector == nil {
		t.Fatal("expected default ResourceSyncCollector config to be initialized")
	}
	config.Metrics.ResourceSyncCollector.TickerInterval = util.Duration(1 * time.Millisecond)
	collector := NewResourceSyncCollector(ctx, mockStore, log, config)
	time.Sleep(5 * time.Millisecond)

	ch := make(chan prometheus.Metric, 100)
	go func() {
		collector.Collect(ch)
		close(ch)
	}()

	var metrics []prometheus.Metric
	for metric := range ch {
		metrics = append(metrics, metric)
	}

	if len(metrics) == 0 {
		t.Error("Expected metrics to be collected, but got none")
	}

	// Verify we got the expected number of metrics (one per mock result)
	if len(metrics) != len(mockResults) {
		t.Errorf("Expected %d metrics, got %d", len(mockResults), len(metrics))
	}

	t.Logf("Collected %d metrics", len(metrics))
}

func TestResourceSyncCollectorWithEmptyResults(t *testing.T) {
	// Test the new behavior where empty results emit a default metric
	mockStore := &MockResourceSyncStore{results: []store.CountByResourceSyncOrgAndStatusResult{}} // Empty results
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create collector
	config := config.NewDefault()
	if config.Metrics == nil || config.Metrics.ResourceSyncCollector == nil {
		t.Fatal("expected default ResourceSyncCollector config to be initialized")
	}
	config.Metrics.ResourceSyncCollector.TickerInterval = util.Duration(1 * time.Millisecond)
	collector := NewResourceSyncCollector(ctx, mockStore, log, config)
	time.Sleep(5 * time.Millisecond)

	// Test that metrics are collected even with empty results
	ch := make(chan prometheus.Metric, 100)
	go func() {
		collector.Collect(ch)
		close(ch)
	}()

	// Collect metrics
	var metrics []prometheus.Metric
	for metric := range ch {
		metrics = append(metrics, metric)
	}

	// Verify that we got metrics even with empty results
	// Should have 1 default metric for the resourcesync gauge
	if len(metrics) == 0 {
		t.Error("Expected metrics to be collected even with empty results, but got none")
	}

	t.Logf("Collected %d metrics with empty results", len(metrics))
}

func TestResourceSyncCollectorUpdateResourceSyncMetricsWithEmptyResults(t *testing.T) {
	// Test the updateResourceSyncMetrics method directly with empty results
	mockStore := &MockResourceSyncStore{results: []store.CountByResourceSyncOrgAndStatusResult{}} // Empty results
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create collector
	config := config.NewDefault()
	if config.Metrics == nil || config.Metrics.ResourceSyncCollector == nil {
		t.Fatal("expected default ResourceSyncCollector config to be initialized")
	}
	config.Metrics.ResourceSyncCollector.TickerInterval = util.Duration(1 * time.Millisecond)
	collector := NewResourceSyncCollector(ctx, mockStore, log, config)

	// Call updateResourceSyncMetrics directly
	collector.updateResourceSyncMetrics()

	// Test that metrics are collected even with empty results
	ch := make(chan prometheus.Metric, 100)
	go func() {
		collector.Collect(ch)
		close(ch)
	}()

	// Collect metrics
	var metrics []prometheus.Metric
	for metric := range ch {
		metrics = append(metrics, metric)
	}

	// Verify that we got metrics even with empty results
	// Should have 1 default metric for the resourcesync gauge
	if len(metrics) == 0 {
		t.Error("Expected metrics to be collected even with empty results, but got none")
	}

	t.Logf("Direct updateResourceSyncMetrics call with empty results collected %d metrics", len(metrics))
}
