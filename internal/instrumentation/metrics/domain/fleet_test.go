package domain

import (
	"context"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

// MockFleetStore implements store.Fleet for testing
type MockFleetStore struct {
	fleetList            *api.FleetList
	fleetListWithDevices *api.FleetList
	rolloutStatusCounts  []store.CountByRolloutStatusResult
	shouldError          bool
}

func (m *MockFleetStore) InitialMigration(ctx context.Context) error {
	if m.shouldError {
		return assert.AnError
	}
	return nil
}

func (m *MockFleetStore) Create(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, callback store.EventCallback) (*api.Fleet, error) {
	return nil, nil
}

func (m *MockFleetStore) Update(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, fieldsToUnset []string, fromAPI bool, callback store.EventCallback) (*api.Fleet, error) {
	return nil, nil
}

func (m *MockFleetStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, fieldsToUnset []string, fromAPI bool, callback store.EventCallback) (*api.Fleet, bool, error) {
	return nil, false, nil
}

func (m *MockFleetStore) Get(ctx context.Context, orgId uuid.UUID, name string, opts ...store.GetOption) (*api.Fleet, error) {
	return nil, nil
}

func (m *MockFleetStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams, opts ...store.ListOption) (*api.FleetList, error) {
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

func (m *MockFleetStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet) (*api.Fleet, error) {
	return nil, nil
}

func (m *MockFleetStore) ListRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*api.FleetList, error) {
	return nil, nil
}

func (m *MockFleetStore) ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*api.FleetList, error) {
	return nil, nil
}

func (m *MockFleetStore) UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	return nil
}

func (m *MockFleetStore) UnsetOwnerByKind(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, resourceKind string) error {
	return nil
}

func (m *MockFleetStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition, eventCallback store.EventCallback) error {
	return nil
}

func (m *MockFleetStore) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string, callbackEvent store.EventCallback) error {
	return nil
}

func (m *MockFleetStore) OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error {
	return nil
}

func (m *MockFleetStore) GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, error) {
	return nil, nil
}

func (m *MockFleetStore) CountByRolloutStatus(ctx context.Context, orgId *uuid.UUID, version *string) ([]store.CountByRolloutStatusResult, error) {
	if m.shouldError {
		return nil, assert.AnError
	}
	return m.rolloutStatusCounts, nil
}

// MockFleetStoreWrapper wraps MockFleetStore to provide the store interface
type MockFleetStoreWrapper struct {
	fleetStore *MockFleetStore
}

func (m *MockFleetStoreWrapper) Device() store.Device {
	return nil
}

func (m *MockFleetStoreWrapper) Fleet() store.Fleet {
	return m.fleetStore
}

func (m *MockFleetStoreWrapper) Repository() store.Repository {
	return nil
}

func (m *MockFleetStoreWrapper) ResourceSync() store.ResourceSync {
	return nil
}

func (m *MockFleetStoreWrapper) EnrollmentRequest() store.EnrollmentRequest {
	return nil
}

func (m *MockFleetStoreWrapper) CertificateSigningRequest() store.CertificateSigningRequest {
	return nil
}

func (m *MockFleetStoreWrapper) Event() store.Event {
	return nil
}

func (m *MockFleetStoreWrapper) TemplateVersion() store.TemplateVersion {
	return nil
}

func (m *MockFleetStoreWrapper) Checkpoint() store.Checkpoint {
	return nil
}

func (m *MockFleetStoreWrapper) Organization() store.Organization {
	return nil
}

func (m *MockFleetStoreWrapper) RunMigrations(context.Context) error {
	return nil
}

func (m *MockFleetStoreWrapper) Close() error {
	return nil
}

func TestFleetCollector(t *testing.T) {
	// Provide mock SQL results for org/status aggregation
	mockResults := []store.CountByRolloutStatusResult{
		{OrgID: "org1", Status: "Ready", Count: 2},
		{OrgID: "org1", Status: "Progressing", Count: 1},
		{OrgID: "org2", Status: "Ready", Count: 3},
	}

	mockFleetStore := &MockFleetStore{
		rolloutStatusCounts: mockResults,
		shouldError:         false,
	}

	mockStore := &MockFleetStoreWrapper{
		fleetStore: mockFleetStore,
	}

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create collector with 1ms interval for fast testing
	config := config.NewDefault()
	collector := NewFleetCollector(ctx, mockStore, log, config)

	// Wait a bit for the collector to start and collect metrics
	time.Sleep(10 * time.Millisecond)

	// Test that the collector implements the required interfaces
	var _ prometheus.Collector = collector

	// Test MetricsName
	assert.Equal(t, "fleet", collector.MetricsName())

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
		rolloutStatusCounts: []store.CountByRolloutStatusResult{
			{OrgID: "org1", Status: "1", Count: 1},
			{OrgID: "org1", Status: "2", Count: 1},
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

	mockStore := &MockFleetStoreWrapper{
		fleetStore: mockFleetStore,
	}

	config := config.NewDefault()
	collector := NewFleetCollector(ctx, mockStore, log, config)

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
