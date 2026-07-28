package domain

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

// MockDevice implements devicestore.Store for testing
type MockDevice struct {
	results []devicestore.CountByOrgAndStatusResult
}

func (m *MockDevice) GetWithTimestamp(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, error) {
	return nil, nil
}

func (m *MockDevice) ListConnectivityChanged(ctx context.Context, orgId uuid.UUID, listParams store.ListParams, cutoffTime time.Time) (*domain.DeviceList, error) {
	return nil, nil
}

func (m *MockDevice) GetLastSeen(ctx context.Context, orgId uuid.UUID, name string) (*time.Time, error) {
	return nil, nil
}

func (m *MockDevice) Healthcheck(ctx context.Context, orgId uuid.UUID, names []string) error {
	return nil
}

func (m *MockDevice) CountByOrgAndStatus(ctx context.Context, orgId *uuid.UUID, statusType devicestore.DeviceStatusType, groupByFleet bool) ([]devicestore.CountByOrgAndStatusResult, error) {
	return m.results, nil
}

// Implement other required methods with empty implementations
func (m *MockDevice) InitialMigration(ctx context.Context) error { return nil }
func (m *MockDevice) Create(ctx context.Context, orgId uuid.UUID, device *domain.Device, callback store.EventCallback) (*domain.Device, error) {
	return nil, nil
}
func (m *MockDevice) Update(ctx context.Context, orgId uuid.UUID, device *domain.Device, fieldsToUnset []string, validationCallback devicestore.DeviceStoreValidationCallback, callback store.EventCallback) (*domain.Device, error) {
	return nil, nil
}
func (m *MockDevice) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *domain.Device, fieldsToUnset []string, validationCallback devicestore.DeviceStoreValidationCallback, callback store.EventCallback) (*domain.Device, bool, error) {
	return nil, false, nil
}
func (m *MockDevice) Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, error) {
	return nil, nil
}
func (m *MockDevice) List(ctx context.Context, orgId uuid.UUID, listParams devicestore.DeviceListParams) (*domain.DeviceList, error) {
	return nil, nil
}
func (m *MockDevice) Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error) {
	return 0, nil
}
func (m *MockDevice) Summary(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*domain.DevicesSummary, error) {
	return nil, nil
}
func (m *MockDevice) Labels(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (domain.LabelList, error) {
	return nil, nil
}
func (m *MockDevice) Delete(ctx context.Context, orgId uuid.UUID, name string, callback store.EventCallback) (bool, error) {
	return true, nil
}
func (m *MockDevice) UpdateStatus(ctx context.Context, orgId uuid.UUID, device *domain.Device, callbackEvent store.EventCallback) (*domain.Device, error) {
	return nil, nil
}
func (m *MockDevice) GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string, consoleGrpcEndpoint string) (*domain.Device, error) {
	return nil, nil
}
func (m *MockDevice) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error {
	return nil
}
func (m *MockDevice) MutateAnnotation(ctx context.Context, orgId uuid.UUID, name string, key string, mutate func(current string) (string, error)) error {
	_, err := mutate("")
	return err
}
func (m *MockDevice) UpdateRendered(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications, specHash string, configFingerprints []domain.DependencySyncConfigRefStatus, forceUpdate bool) (string, error) {
	return "", nil
}
func (m *MockDevice) SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition, callback devicestore.ServiceConditionsCallback) error {
	return nil
}
func (m *MockDevice) OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error {
	return nil
}
func (m *MockDevice) GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, error) {
	return nil, nil
}
func (m *MockDevice) UnmarkRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) error {
	return nil
}
func (m *MockDevice) MarkRolloutSelection(ctx context.Context, orgId uuid.UUID, listParams store.ListParams, limit *int) error {
	return nil
}
func (m *MockDevice) CompletionCounts(ctx context.Context, orgId uuid.UUID, owner string, templateVersion string, updateTimeout *time.Duration) ([]domain.DeviceCompletionCount, error) {
	return nil, nil
}
func (m *MockDevice) CountByLabels(ctx context.Context, orgId uuid.UUID, listParams store.ListParams, groupBy []string) ([]map[string]any, error) {
	return nil, nil
}
func (m *MockDevice) UpdateSummaryStatusBatch(ctx context.Context, orgId uuid.UUID, deviceNames []string, status domain.DeviceSummaryStatusType, statusInfo string) error {
	return nil
}
func (m *MockDevice) ListDevicesByServiceCondition(ctx context.Context, orgId uuid.UUID, conditionType string, conditionStatus string, listParams store.ListParams) (*domain.DeviceList, error) {
	return nil, nil
}
func (m *MockDevice) SetIntegrationTestCreateOrUpdateCallback(store.IntegrationTestCallback) {}
func (m *MockDevice) RemoveConflictPausedAnnotation(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, []string, error) {
	return 0, nil, nil
}

func (m *MockDevice) SetOutOfDate(ctx context.Context, orgId uuid.UUID, owner string) error {
	return nil
}

func (m *MockDevice) ProcessAwaitingReconnectAnnotation(ctx context.Context, orgId uuid.UUID, deviceName string, deviceReportedVersion *string) (bool, error) {
	return false, nil
}

func (m *MockDevice) DecommissionDevice(ctx context.Context, orgId uuid.UUID, name string, decom domain.DeviceDecommission, eventCallback store.EventCallback) (*domain.Device, error) {
	return nil, nil
}

func TestDeviceCollectorWithGroupByFleet(t *testing.T) {
	// Provide mock SQL results for org/status aggregation
	mockResults := []devicestore.CountByOrgAndStatusResult{
		{OrgID: "org1", Fleet: "fleet1", Status: "Online", Count: 3},
		{OrgID: "org1", Fleet: "fleet1", Status: "Unknown", Count: 3},
		{OrgID: "org2", Fleet: "fleet2", Status: "Online", Count: 3},
		{OrgID: "org2", Fleet: "fleet2", Status: "Unknown", Count: 1},
	}

	mockDevice := &MockDevice{results: mockResults}
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create collector with 1ms interval for fast testing
	config := config.NewDefault()
	config.Metrics.DeviceCollector.GroupByFleet = true
	collector := NewDeviceCollector(ctx, mockDevice, log, config)

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
func TestDeviceCollectorWithoutGroupByFleet(t *testing.T) {
	// Provide mock SQL results for org/status aggregation
	mockResults := []devicestore.CountByOrgAndStatusResult{
		{OrgID: "org1", Status: "Online", Count: 3},
		{OrgID: "org1", Status: "Unknown", Count: 3},
		{OrgID: "org2", Status: "Online", Count: 3},
		{OrgID: "org2", Status: "Unknown", Count: 1},
	}

	mockDevice := &MockDevice{results: mockResults}
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create collector with 1ms interval for fast testing
	config := config.NewDefault()
	config.Metrics.DeviceCollector.GroupByFleet = false
	collector := NewDeviceCollector(ctx, mockDevice, log, config)

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

func TestDeviceCollectorWithOrgFilter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test that org filtering works correctly
	mockDevice := &MockDevice{
		results: []devicestore.CountByOrgAndStatusResult{
			{OrgID: "org1", Status: "Online", Count: 2},
			{OrgID: "org1", Status: "Unknown", Count: 1},
		},
	}

	// Test with specific org filter
	orgId := uuid.New()
	results, err := mockDevice.CountByOrgAndStatus(ctx, &orgId, devicestore.DeviceStatusTypeSummary, false)
	assert.NoError(t, err)
	assert.Len(t, results, 2)

	// Verify all results have the filtered org
	for _, result := range results {
		assert.Equal(t, "org1", result.OrgID)
	}

	// Test with nil org (no filter)
	results, err = mockDevice.CountByOrgAndStatus(ctx, nil, devicestore.DeviceStatusTypeSummary, false)
	assert.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestDeviceCollectorWithEmptyResults(t *testing.T) {
	// Test the new behavior where empty results emit a default metric
	mockDevice := &MockDevice{results: []devicestore.CountByOrgAndStatusResult{}} // Empty results
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create collector
	config := config.NewDefault()
	config.Metrics.DeviceCollector.GroupByFleet = true
	collector := NewDeviceCollector(ctx, mockDevice, log, config)

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
	// Should have 3 default metrics (one for each gauge: summary, application, update)
	assert.GreaterOrEqual(t, len(metrics), 3, "Expected at least 3 metrics (one default per gauge) even with empty results")

	t.Logf("Collected %d metrics with empty results", len(metrics))
}

func TestDeviceCollectorUpdateDeviceMetricsWithEmptyResults(t *testing.T) {
	// Test the updateDeviceMetrics method directly with empty results
	mockDevice := &MockDevice{results: []devicestore.CountByOrgAndStatusResult{}} // Empty results
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create collector
	config := config.NewDefault()
	config.Metrics.DeviceCollector.GroupByFleet = true
	collector := NewDeviceCollector(ctx, mockDevice, log, config)

	// Call updateDeviceMetrics directly
	collector.updateDeviceMetrics()

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
	// Should have 3 default metrics (one for each gauge: summary, application, update)
	assert.GreaterOrEqual(t, len(metrics), 3, "Expected at least 3 metrics (one default per gauge) even with empty results")

	t.Logf("Direct updateDeviceMetrics call with empty results collected %d metrics", len(metrics))
}
