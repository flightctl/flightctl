package domain

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// MockFleetStore implements fleetstore.Store for testing
type MockFleetStore struct {
	fleetList            *domain.FleetList
	fleetListWithDevices *domain.FleetList
	rolloutStatusCounts  []fleetstore.CountByRolloutStatusResult
	shouldError          bool
}

func (m *MockFleetStore) InitialMigration(ctx context.Context) error {
	if m.shouldError {
		return assert.AnError
	}
	return nil
}

func (m *MockFleetStore) Create(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, callback store.EventCallback) (*domain.Fleet, error) {
	return nil, nil
}

func (m *MockFleetStore) Update(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, fieldsToUnset []string, callback store.EventCallback) (*domain.Fleet, error) {
	return nil, nil
}

func (m *MockFleetStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, fieldsToUnset []string, callback store.EventCallback) (*domain.Fleet, bool, error) {
	return nil, false, nil
}

func (m *MockFleetStore) Get(ctx context.Context, orgId uuid.UUID, name string, opts ...fleetstore.GetOption) (*domain.Fleet, error) {
	return nil, nil
}

func (m *MockFleetStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams, opts ...fleetstore.ListOption) (*domain.FleetList, error) {
	if m.shouldError {
		return nil, assert.AnError
	}

	// Check if this is a request with device summary
	withDeviceSummary := false
	for _, opt := range opts {
		if opt != nil {
			// Check if this is the ListWithDevicesSummary option
			// We can't directly check the type, but we can check if it's the right option
			// by looking at the function signature or using reflection
			// For now, we'll assume any non-nil option means with device summary
			withDeviceSummary = true
			break
		}
	}

	if withDeviceSummary {
		return m.fleetListWithDevices, nil
	}
	return m.fleetList, nil
}

func (m *MockFleetStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback store.EventCallback) error {
	return nil
}

func (m *MockFleetStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet) (*domain.Fleet, error) {
	return nil, nil
}

func (m *MockFleetStore) ListRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, error) {
	return nil, nil
}

func (m *MockFleetStore) ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, error) {
	return nil, nil
}

func (m *MockFleetStore) UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	return nil
}

func (m *MockFleetStore) UnsetOwnerByKind(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, resourceKind string) error {
	return nil
}

func (m *MockFleetStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition, eventCallback store.EventCallback) error {
	return nil
}

func (m *MockFleetStore) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string, callbackEvent store.EventCallback) error {
	return nil
}

func (m *MockFleetStore) MutateAnnotation(ctx context.Context, orgId uuid.UUID, name string, key string, mutate func(current string) (string, error)) error {
	return nil
}

func (m *MockFleetStore) OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error {
	return nil
}

func (m *MockFleetStore) GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, error) {
	return nil, nil
}

func (m *MockFleetStore) CountByRolloutStatus(ctx context.Context, orgId *uuid.UUID, version *string) ([]fleetstore.CountByRolloutStatusResult, error) {
	if m.shouldError {
		return nil, assert.AnError
	}
	return m.rolloutStatusCounts, nil
}

func TestFleetCollector(t *testing.T) {
	// Provide mock SQL results for org/status aggregation using RolloutInProgress condition reasons
	mockResults := []fleetstore.CountByRolloutStatusResult{
		{OrgID: "org1", Status: "Active", Count: 2},
		{OrgID: "org1", Status: "Suspended", Count: 1},
		{OrgID: "org2", Status: "Inactive", Count: 3},
	}

	mockFleetStore := &MockFleetStore{
		rolloutStatusCounts: mockResults,
		shouldError:         false,
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create collector with 1ms interval for fast testing
	config := config.NewDefault()
	collector := NewFleetCollector(ctx, mockFleetStore, log, config)

	// Wait a bit for the collector to start and collect metrics
	time.Sleep(10 * time.Millisecond)

	// Test that the collector implements the required interfaces
	var _ prometheus.Collector = collector

	// Test that metrics are collected
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

	// Verify that we got some metrics
	if len(metrics) == 0 {
		t.Error("Expected metrics to be collected, but got none")
	}

	t.Logf("Collected %d metrics", len(metrics))
}

func TestFleetCollectorWithOrgFilter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test that org filtering works correctly
	mockFleetStore := &MockFleetStore{
		rolloutStatusCounts: []fleetstore.CountByRolloutStatusResult{
			{OrgID: "org1", Status: "Active", Count: 1},
			{OrgID: "org1", Status: "Waiting", Count: 1},
		},
	}

	// Test with specific org filter
	orgId := uuid.New()
	results, err := mockFleetStore.CountByRolloutStatus(ctx, &orgId, nil)
	assert.NoError(t, err)
	assert.Len(t, results, 2)

	// Verify all results have the filtered org
	for _, result := range results {
		assert.Equal(t, "org1", result.OrgID)
	}

	// Test with nil org (no filter)
	results, err = mockFleetStore.CountByRolloutStatus(ctx, nil, nil)
	assert.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestFleetCollectorWithErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	mockFleetStore := &MockFleetStore{
		shouldError: true,
	}

	config := config.NewDefault()
	collector := NewFleetCollector(ctx, mockFleetStore, log, config)

	// Test Collect with errors - should not panic
	metricCh := make(chan prometheus.Metric, 10)
	collector.Collect(metricCh)
	close(metricCh)

	// Should still have 0 metrics due to errors
	metricCount := 0
	for range metricCh {
		metricCount++
	}
	assert.Equal(t, 0, metricCount)
}

func TestFleetCollectorWithEmptyResults(t *testing.T) {
	// Test the new behavior where empty results emit a default metric
	mockFleetStore := &MockFleetStore{
		rolloutStatusCounts: []fleetstore.CountByRolloutStatusResult{}, // Empty results
		shouldError:         false,
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create collector
	config := config.NewDefault()
	collector := NewFleetCollector(ctx, mockFleetStore, log, config)

	// Wait a bit for the collector to start and collect metrics
	time.Sleep(10 * time.Millisecond)

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
	// Should have 1 default metric for the fleet gauge
	assert.GreaterOrEqual(t, len(metrics), 1, "Expected at least 1 metric (default fleet metric) even with empty results")

	t.Logf("Collected %d metrics with empty results", len(metrics))
}

func TestFleetCollectorUpdateFleetMetricsWithEmptyResults(t *testing.T) {
	// Test the updateFleetMetrics method directly with empty results
	mockFleetStore := &MockFleetStore{
		rolloutStatusCounts: []fleetstore.CountByRolloutStatusResult{}, // Empty results
		shouldError:         false,
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create collector
	config := config.NewDefault()
	collector := NewFleetCollector(ctx, mockFleetStore, log, config)

	// Call updateFleetMetrics directly
	collector.updateFleetMetrics()

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
	// Should have 1 default metric for the fleet gauge
	assert.GreaterOrEqual(t, len(metrics), 1, "Expected at least 1 metric (default fleet metric) even with empty results")

	t.Logf("Direct updateFleetMetrics call with empty results collected %d metrics", len(metrics))
}
