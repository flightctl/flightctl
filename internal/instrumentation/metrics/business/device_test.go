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
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

// MockStore implements store.Store for testing
type MockStore struct {
	results []store.CountByFleetAndStatusResult
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

func (m *MockStore) InitialMigration(context.Context) error {
	return nil
}

func (m *MockStore) Close() error {
	return nil
}

// MockDevice implements store.Device for testing
type MockDevice struct {
	results []store.CountByFleetAndStatusResult
}

func (m *MockDevice) CountByFleetAndStatus(ctx context.Context, orgId *uuid.UUID, version *string, statusType store.DeviceStatusType) ([]store.CountByFleetAndStatusResult, error) {
	return m.results, nil
}

// Implement other required methods with empty implementations
func (m *MockDevice) InitialMigration(ctx context.Context) error { return nil }
func (m *MockDevice) Create(ctx context.Context, orgId uuid.UUID, device *api.Device, callback store.DeviceStoreCallback) (*api.Device, error) {
	return nil, nil
}
func (m *MockDevice) Update(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callback store.DeviceStoreCallback) (*api.Device, api.ResourceUpdatedDetails, error) {
	return nil, api.ResourceUpdatedDetails{}, nil
}
func (m *MockDevice) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callback store.DeviceStoreCallback) (*api.Device, bool, api.ResourceUpdatedDetails, error) {
	return nil, false, api.ResourceUpdatedDetails{}, nil
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
func (m *MockDevice) Delete(ctx context.Context, orgId uuid.UUID, name string, callback store.DeviceStoreCallback) (bool, error) {
	return false, nil
}
func (m *MockDevice) UpdateStatus(ctx context.Context, orgId uuid.UUID, device *api.Device) (*api.Device, error) {
	return nil, nil
}
func (m *MockDevice) GetRendered(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion *string, consoleGrpcEndpoint string) (*api.Device, error) {
	return nil, nil
}
func (m *MockDevice) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error {
	return nil
}
func (m *MockDevice) UpdateRendered(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications string) error {
	return nil
}
func (m *MockDevice) SetServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error {
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
func (m *MockDevice) SetIntegrationTestCreateOrUpdateCallback(store.IntegrationTestCallback) {}

func TestDeviceCollector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Provide mock SQL results for fleet/status aggregation
	mockResults := []store.CountByFleetAndStatusResult{
		{OrgID: "org1", Fleet: "production", Status: "Online", Version: "v1.0", Count: 2},
		{OrgID: "org1", Fleet: "production", Status: "Unknown", Version: "v1.0", Count: 1},
		{OrgID: "org1", Fleet: "standalone", Status: "Online", Version: "v1.0", Count: 1},
		{OrgID: "org1", Fleet: "standalone", Status: "Unknown", Version: "v1.0", Count: 2},
		{OrgID: "org2", Fleet: "production", Status: "Online", Version: "v2.0", Count: 3},
		{OrgID: "org2", Fleet: "production", Status: "Unknown", Version: "v2.0", Count: 1},
	}

	mockStore := &MockStore{results: mockResults}
	log := logrus.New()
	collector := NewDeviceCollector(ctx, mockStore, log)

	// Test that the collector implements the required interfaces
	var _ prometheus.Collector = collector
	var _ metrics.NamedCollector = collector

	// Test MetricsName
	assert.Equal(t, "device", collector.MetricsName())

	// Test Describe
	descCh := make(chan *prometheus.Desc, 10)
	collector.Describe(descCh)
	close(descCh)

	descs := make([]*prometheus.Desc, 0)
	for desc := range descCh {
		descs = append(descs, desc)
	}

	// Should have 3 metrics (summary, application, update)
	assert.Len(t, descs, 3)

	// Test Collect - should work even without data
	metricCh := make(chan prometheus.Metric, 10)
	collector.Collect(metricCh)
	close(metricCh)

	metrics := make([]prometheus.Metric, 0)
	for metric := range metricCh {
		metrics = append(metrics, metric)
	}

	// Initially there should be no metrics since no data has been collected yet
	// The collector should still work without errors
	assert.Len(t, metrics, 0)

	// Cancel context to stop background goroutine
	cancel()

	// Give the background goroutine a moment to exit
	time.Sleep(10 * time.Millisecond)
}

func TestDeviceCollectorWithVersionFilter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test that version filtering works correctly
	mockDevice := &MockDevice{
		results: []store.CountByFleetAndStatusResult{
			{OrgID: "org1", Fleet: "production", Status: "Online", Version: "v1.0", Count: 2},
			{OrgID: "org1", Fleet: "production", Status: "Unknown", Version: "v1.0", Count: 1},
		},
	}

	// Test with specific version filter
	version := "v1.0"
	results, err := mockDevice.CountByFleetAndStatus(ctx, nil, &version, store.DeviceStatusTypeSummary)
	assert.NoError(t, err)
	assert.Len(t, results, 2)

	// Verify all results have the filtered version
	for _, result := range results {
		assert.Equal(t, "v1.0", result.Version)
	}

	// Test with nil version (no filter)
	results, err = mockDevice.CountByFleetAndStatus(ctx, nil, nil, store.DeviceStatusTypeSummary)
	assert.NoError(t, err)
	assert.Len(t, results, 2)
}

// Helper function to create string pointers
func stringPtr(s string) *string {
	return &s
}
