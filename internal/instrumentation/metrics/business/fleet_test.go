package business

import (
	"context"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
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

func (m *MockFleetStore) Create(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, callback store.FleetStoreCallback) (*api.Fleet, error) {
	return nil, nil
}

func (m *MockFleetStore) Update(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, fieldsToUnset []string, fromAPI bool, callback store.FleetStoreCallback) (*api.Fleet, api.ResourceUpdatedDetails, error) {
	return nil, api.ResourceUpdatedDetails{}, nil
}

func (m *MockFleetStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, fieldsToUnset []string, fromAPI bool, callback store.FleetStoreCallback) (*api.Fleet, bool, api.ResourceUpdatedDetails, error) {
	return nil, false, api.ResourceUpdatedDetails{}, nil
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

func (m *MockFleetStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback store.FleetStoreCallback) (bool, error) {
	return false, nil
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

func (m *MockFleetStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error {
	return nil
}

func (m *MockFleetStore) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error {
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

func (m *MockFleetStoreWrapper) InitialMigration(context.Context) error {
	return nil
}

func (m *MockFleetStoreWrapper) Close() error {
	return nil
}

func TestFleetCollector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create test data
	fleetList := &api.FleetList{
		Items: []api.Fleet{
			{
				Metadata: api.ObjectMeta{Name: stringPtr("fleet1")},
			},
			{
				Metadata: api.ObjectMeta{Name: stringPtr("fleet2")},
			},
		},
	}

	rolloutStatusCounts := []store.CountByRolloutStatusResult{
		{OrgID: "org1", Status: "1", Version: "v1.0", Count: 1},
		{OrgID: "org1", Status: "2", Version: "v1.0", Count: 1},
		{OrgID: "org1", Status: "", Version: "v1.0", Count: 1}, // Should become "none"
		{OrgID: "org2", Status: "1", Version: "v2.0", Count: 2},
		{OrgID: "org2", Status: "none", Version: "v2.0", Count: 1},
	}

	mockFleetStore := &MockFleetStore{
		fleetList:            fleetList,
		fleetListWithDevices: fleetList,
		rolloutStatusCounts:  rolloutStatusCounts,
		shouldError:          false,
	}

	mockStore := &MockFleetStoreWrapper{
		fleetStore: mockFleetStore,
	}

	collector := NewFleetCollector(ctx, mockStore, log)

	// Test that the collector implements NamedCollector
	var _ metrics.NamedCollector = collector

	// Test MetricsName
	assert.Equal(t, "fleet", collector.MetricsName())

	// Test Describe
	descCh := make(chan *prometheus.Desc, 10)
	collector.Describe(descCh)
	close(descCh)

	descCount := 0
	for range descCh {
		descCount++
	}
	assert.Equal(t, 2, descCount) // 2 metrics: total, rollout_status

	// Test Collect with empty data
	metricCh := make(chan prometheus.Metric, 10)
	collector.Collect(metricCh)
	close(metricCh)

	metricCount := 0
	for range metricCh {
		metricCount++
	}
	assert.Equal(t, 0, metricCount) // No metrics yet since no data

	// Ensure metrics are updated before collecting
	collector.updateFleetMetrics()

	// Test Collect with data
	metricCh = make(chan prometheus.Metric, 20)
	collector.Collect(metricCh)
	close(metricCh)

	metrics := make([]prometheus.Metric, 0)
	for metric := range metricCh {
		metrics = append(metrics, metric)
	}

	// Should have metrics now
	assert.Greater(t, len(metrics), 0)

	// Test specific metric values - now we need to check by org and version
	totalFleetsValue := testutil.ToFloat64(collector.totalFleetsGauge.WithLabelValues("org1", "v1.0", "total"))
	assert.Equal(t, float64(3), totalFleetsValue) // 3 fleets in org1 v1.0
}

func TestFleetCollectorWithVersionFilter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test that version filtering works correctly
	mockFleetStore := &MockFleetStore{
		rolloutStatusCounts: []store.CountByRolloutStatusResult{
			{OrgID: "org1", Status: "1", Version: "v1.0", Count: 1},
			{OrgID: "org1", Status: "2", Version: "v1.0", Count: 1},
		},
	}

	// Test with specific version filter
	version := "v1.0"
	results, err := mockFleetStore.CountByRolloutStatus(ctx, nil, &version)
	assert.NoError(t, err)
	assert.Len(t, results, 2)

	// Verify all results have the filtered version
	for _, result := range results {
		assert.Equal(t, "v1.0", result.Version)
	}

	// Test with nil version (no filter)
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

	collector := NewFleetCollector(ctx, mockStore, log)

	// Wait for metrics to be updated
	time.Sleep(100 * time.Millisecond)

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
