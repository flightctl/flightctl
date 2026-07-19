package domain

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	resourcesyncstore "github.com/flightctl/flightctl/internal/store/resourcesync"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// MockResourceSync implements resourcesync.Store for testing
type MockResourceSync struct {
	results []resourcesyncstore.CountByResourceSyncOrgAndStatusResult
}

func (m *MockResourceSync) InitialMigration(ctx context.Context) error { return nil }
func (m *MockResourceSync) Create(ctx context.Context, orgId uuid.UUID, resourceSync *domain.ResourceSync) (*domain.ResourceSync, error) {
	return nil, nil
}
func (m *MockResourceSync) Update(ctx context.Context, orgId uuid.UUID, resourceSync *domain.ResourceSync) (*domain.ResourceSync, *domain.ResourceSync, error) {
	return nil, nil, nil
}
func (m *MockResourceSync) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resourceSync *domain.ResourceSync) (*domain.ResourceSync, *domain.ResourceSync, bool, error) {
	return nil, nil, false, nil
}
func (m *MockResourceSync) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.ResourceSync, error) {
	return nil, nil
}
func (m *MockResourceSync) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.ResourceSyncList, error) {
	return nil, nil
}
func (m *MockResourceSync) WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}
func (m *MockResourceSync) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	return nil
}
func (m *MockResourceSync) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.ResourceSync) (*domain.ResourceSync, *domain.ResourceSync, error) {
	return nil, nil, nil
}
func (m *MockResourceSync) Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error) {
	return 0, nil
}
func (m *MockResourceSync) CountByOrgAndStatus(ctx context.Context, orgId *uuid.UUID, status *string) ([]resourcesyncstore.CountByResourceSyncOrgAndStatusResult, error) {
	return m.results, nil
}

func TestResourceSyncCollectorGroupByOrgAndStatus(t *testing.T) {
	mockResults := []resourcesyncstore.CountByResourceSyncOrgAndStatusResult{
		{OrgID: "org1", Status: "Ready", Count: 2},
		{OrgID: "org1", Status: "Progressing", Count: 1},
		{OrgID: "org2", Status: "Ready", Count: 3},
	}

	mockResourceSync := &MockResourceSync{results: mockResults}
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	config := config.NewDefault()
	if config.Metrics == nil || config.Metrics.ResourceSyncCollector == nil {
		t.Fatal("expected default ResourceSyncCollector config to be initialized")
	}
	config.Metrics.ResourceSyncCollector.TickerInterval = util.Duration(1 * time.Millisecond)
	collector := NewResourceSyncCollector(ctx, mockResourceSync, log, config)
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
	mockResourceSync := &MockResourceSync{results: []resourcesyncstore.CountByResourceSyncOrgAndStatusResult{}} // Empty results
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
	collector := NewResourceSyncCollector(ctx, mockResourceSync, log, config)
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
	mockResourceSync := &MockResourceSync{results: []resourcesyncstore.CountByResourceSyncOrgAndStatusResult{}} // Empty results
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
	collector := NewResourceSyncCollector(ctx, mockResourceSync, log, config)

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
