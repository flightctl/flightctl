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
)

// MockStore implements store.Store for testing
type MockStore struct {
	results []store.CountByOrgAndStatusResult
}

func (m *MockStore) Device() store.Device {
	return &MockDevice{
		results: m.results,
	}
}

func (m *MockStore) EnrollmentRequest() store.EnrollmentRequest {
	return nil
}

func (m *MockStore) CertificateSigningRequest() store.CertificateSigningRequest {
	return nil
}

func (m *MockStore) Fleet() store.Fleet {
	return nil
}

func (m *MockStore) TemplateVersion() store.TemplateVersion {
	return nil
}

func (m *MockStore) Repository() store.Repository {
	return nil
}

func (m *MockStore) ResourceSync() store.ResourceSync {
	return nil
}

func (m *MockStore) Event() store.Event {
	return nil
}

func (m *MockStore) Checkpoint() store.Checkpoint {
	return nil
}

func (m *MockStore) Organization() store.Organization {
	return nil
}

func (m *MockStore) RunMigrations(context.Context) error {
	return nil
}

func (m *MockStore) Close() error {
	return nil
}

func (m *MockStore) CheckHealth(context.Context) error {
	return nil
}

// MockDevice implements store.Device for testing
type MockDevice struct {
	results []store.CountByOrgAndStatusResult
}

func (m *MockDevice) GetWithoutServiceConditions(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error) {
	return nil, nil
}

func (m *MockDevice) GetLastSeen(ctx context.Context, orgId uuid.UUID, name string) (*time.Time, error) {
	return nil, nil
}

func (m *MockDevice) Healthcheck(ctx context.Context, orgId uuid.UUID, names []string) error {
	return nil
}

func (m *MockDevice) CountByOrgAndStatus(ctx context.Context, orgId *uuid.UUID, statusType store.DeviceStatusType, groupByFleet bool) ([]store.CountByOrgAndStatusResult, error) {
	return m.results, nil
}

// Implement other required methods with empty implementations
func (m *MockDevice) InitialMigration(ctx context.Context) error { return nil }
func (m *MockDevice) Create(ctx context.Context, orgId uuid.UUID, device *api.Device, callback store.EventCallback) (*api.Device, error) {
	return nil, nil
}
func (m *MockDevice) Update(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callback store.EventCallback) (*api.Device, error) {
	return nil, nil
}
func (m *MockDevice) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callback store.EventCallback) (*api.Device, bool, error) {
	return nil, false, nil
}
func (m *MockDevice) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error) {
	return nil, nil
}
func (m *MockDevice) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*api.DeviceList, error) {
	return nil, nil
}
func (m *MockDevice) Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error) {
	return 0, nil
}
func (m *MockDevice) Summary(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*api.DevicesSummary, error) {
	return nil, nil
}
func (m *MockDevice) Labels(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (api.LabelList, error) {
	return nil, nil
}
func (m *MockDevice) Delete(ctx context.Context, orgId uuid.UUID, name string, callback store.EventCallback) (bool, error) {
	return true, nil
}
func (m *MockDevice) UpdateStatus(ctx context.Context, orgId uuid.UUID, device *api.Device, callbackEvent store.EventCallback) (*api.Device, error) {
	return nil, nil
}
func (m *MockDevice) GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string, consoleGrpcEndpoint string) (*api.Device, error) {
	return nil, nil
}
func (m *MockDevice) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error {
	return nil
}
func (m *MockDevice) UpdateRendered(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications, specHash string) (string, error) {
	return "", nil
}
func (m *MockDevice) SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition, callback store.ServiceConditionsCallback) error {
	return nil
}
func (m *MockDevice) OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error {
	return nil
}
func (m *MockDevice) GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, error) {
	return nil, nil
}
func (m *MockDevice) UnmarkRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) error {
	return nil
}
func (m *MockDevice) MarkRolloutSelection(ctx context.Context, orgId uuid.UUID, listParams store.ListParams, limit *int) error {
	return nil
}
func (m *MockDevice) CompletionCounts(ctx context.Context, orgId uuid.UUID, owner string, templateVersion string, updateTimeout *time.Duration) ([]api.DeviceCompletionCount, error) {
	return nil, nil
}
func (m *MockDevice) CountByLabels(ctx context.Context, orgId uuid.UUID, listParams store.ListParams, groupBy []string) ([]map[string]any, error) {
	return nil, nil
}
func (m *MockDevice) UpdateSummaryStatusBatch(ctx context.Context, orgId uuid.UUID, deviceNames []string, status api.DeviceSummaryStatusType, statusInfo string) error {
	return nil
}
func (m *MockDevice) ListDevicesByServiceCondition(ctx context.Context, orgId uuid.UUID, conditionType string, conditionStatus string, listParams store.ListParams) (*api.DeviceList, error) {
	return nil, nil
}
func (m *MockDevice) SetIntegrationTestCreateOrUpdateCallback(store.IntegrationTestCallback) {}
func (m *MockDevice) PrepareDevicesAfterRestore(ctx context.Context) (int64, error) {
	return 0, nil
}
func (m *MockDevice) RemoveConflictPausedAnnotation(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, []string, error) {
	return 0, nil, nil
}

func (m *MockDevice) SetOutOfDate(ctx context.Context, orgId uuid.UUID, owner string) error {
	return nil
}

func TestDeviceCollectorWithGroupByFleet(t *testing.T) {
	// Provide mock SQL results for org/status aggregation
	mockResults := []store.CountByOrgAndStatusResult{
		{OrgID: "org1", Fleet: "fleet1", Status: "Online", Count: 3},
		{OrgID: "org1", Fleet: "fleet1", Status: "Unknown", Count: 3},
		{OrgID: "org2", Fleet: "fleet2", Status: "Online", Count: 3},
		{OrgID: "org2", Fleet: "fleet2", Status: "Unknown", Count: 1},
	}

	mockStore := &MockStore{results: mockResults}
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create collector with 1ms interval for fast testing
	config := config.NewDefault()
	config.Metrics.DeviceCollector.GroupByFleet = true
	collector := NewDeviceCollector(ctx, mockStore, log, config)

	// Wait a bit for the collector to start and collect metrics
	time.Sleep(10 * time.Millisecond)

	// Test that the collector implements the required interfaces
	var _ prometheus.Collector = collector

	// Test MetricsName
	assert.Equal(t, "device", collector.MetricsName())

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
	mockResults := []store.CountByOrgAndStatusResult{
		{OrgID: "org1", Status: "Online", Count: 3},
		{OrgID: "org1", Status: "Unknown", Count: 3},
		{OrgID: "org2", Status: "Online", Count: 3},
		{OrgID: "org2", Status: "Unknown", Count: 1},
	}

	mockStore := &MockStore{results: mockResults}
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create collector with 1ms interval for fast testing
	config := config.NewDefault()
	config.Metrics.DeviceCollector.GroupByFleet = false
	collector := NewDeviceCollector(ctx, mockStore, log, config)

	// Wait a bit for the collector to start and collect metrics
	time.Sleep(10 * time.Millisecond)

	// Test that the collector implements the required interfaces
	var _ prometheus.Collector = collector

	// Test MetricsName
	assert.Equal(t, "device", collector.MetricsName())

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
		results: []store.CountByOrgAndStatusResult{
			{OrgID: "org1", Status: "Online", Count: 2},
			{OrgID: "org1", Status: "Unknown", Count: 1},
		},
	}

	// Test with specific org filter
	orgId := uuid.New()
	results, err := mockDevice.CountByOrgAndStatus(ctx, &orgId, store.DeviceStatusTypeSummary, false)
	assert.NoError(t, err)
	assert.Len(t, results, 2)

	// Verify all results have the filtered org
	for _, result := range results {
		assert.Equal(t, "org1", result.OrgID)
	}

	// Test with nil org (no filter)
	results, err = mockDevice.CountByOrgAndStatus(ctx, nil, store.DeviceStatusTypeSummary, false)
	assert.NoError(t, err)
	assert.Len(t, results, 2)
}
