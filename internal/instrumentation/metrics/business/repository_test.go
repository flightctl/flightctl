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
type MockRepositoryStore struct {
	count int64
	err   error
}

func (m *MockRepositoryStore) Repository() store.Repository {
	return &MockRepository{count: m.count, err: m.err}
}

// Implement other required methods with empty implementations
func (m *MockRepositoryStore) Device() store.Device                                       { return nil }
func (m *MockRepositoryStore) EnrollmentRequest() store.EnrollmentRequest                 { return nil }
func (m *MockRepositoryStore) CertificateSigningRequest() store.CertificateSigningRequest { return nil }
func (m *MockRepositoryStore) Fleet() store.Fleet                                         { return nil }
func (m *MockRepositoryStore) TemplateVersion() store.TemplateVersion                     { return nil }
func (m *MockRepositoryStore) ResourceSync() store.ResourceSync                           { return nil }
func (m *MockRepositoryStore) Event() store.Event                                         { return nil }
func (m *MockRepositoryStore) InitialMigration(context.Context) error                     { return nil }
func (m *MockRepositoryStore) Close() error                                               { return nil }

type MockRepository struct {
	count   int64
	err     error
	results []store.CountByOrgAndVersionResult
}

func (m *MockRepository) Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error) {
	return m.count, m.err
}

func (m *MockRepository) CountByOrgAndVersion(ctx context.Context, orgId *uuid.UUID, version *string) ([]store.CountByOrgAndVersionResult, error) {
	return m.results, m.err
}

// Implement other required methods with empty implementations
func (m *MockRepository) InitialMigration(context.Context) error { return nil }
func (m *MockRepository) Create(context.Context, uuid.UUID, *api.Repository, store.RepositoryStoreCallback) (*api.Repository, error) {
	return nil, nil
}
func (m *MockRepository) Update(context.Context, uuid.UUID, *api.Repository, store.RepositoryStoreCallback) (*api.Repository, api.ResourceUpdatedDetails, error) {
	return nil, api.ResourceUpdatedDetails{}, nil
}
func (m *MockRepository) CreateOrUpdate(context.Context, uuid.UUID, *api.Repository, store.RepositoryStoreCallback) (*api.Repository, bool, api.ResourceUpdatedDetails, error) {
	return nil, false, api.ResourceUpdatedDetails{}, nil
}
func (m *MockRepository) Get(context.Context, uuid.UUID, string) (*api.Repository, error) {
	return nil, nil
}
func (m *MockRepository) List(context.Context, uuid.UUID, store.ListParams) (*api.RepositoryList, error) {
	return nil, nil
}
func (m *MockRepository) Delete(context.Context, uuid.UUID, string, store.RepositoryStoreCallback) (bool, error) {
	return false, nil
}
func (m *MockRepository) UpdateStatus(context.Context, uuid.UUID, *api.Repository) (*api.Repository, error) {
	return nil, nil
}
func (m *MockRepository) GetFleetRefs(context.Context, uuid.UUID, string) (*api.FleetList, error) {
	return nil, nil
}
func (m *MockRepository) GetDeviceRefs(context.Context, uuid.UUID, string) (*api.DeviceList, error) {
	return nil, nil
}

func TestRepositoryCollector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test with successful count by org and version
	mockResults := []store.CountByOrgAndVersionResult{
		{OrgID: "org1", Version: "v1.0", Count: 3},
		{OrgID: "org1", Version: "v2.0", Count: 2},
		{OrgID: "org2", Version: "v1.0", Count: 1},
		{OrgID: "org2", Version: "unknown", Count: 1},
	}
	mockStore := &MockRepositoryStore{count: 5, err: nil}
	mockStore.Repository().(*MockRepository).results = mockResults
	log := logrus.New()
	collector := NewRepositoryCollector(ctx, mockStore, log)

	// Test that the collector implements the required interfaces
	var _ prometheus.Collector = collector
	var _ metrics.NamedCollector = collector

	// Test MetricsName
	assert.Equal(t, "repository", collector.MetricsName())

	// Test Describe
	descCh := make(chan *prometheus.Desc, 10)
	collector.Describe(descCh)
	close(descCh)

	descs := make([]*prometheus.Desc, 0)
	for desc := range descCh {
		descs = append(descs, desc)
	}

	// Should have 1 metric (repositories total)
	assert.Len(t, descs, 1)

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

func TestRepositoryCollectorWithError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test with error
	mockStore := &MockRepositoryStore{count: 0, err: assert.AnError}
	log := logrus.New()
	collector := NewRepositoryCollector(ctx, mockStore, log)

	// Test that the collector handles errors gracefully
	// The collector should not panic and should continue running
	assert.NotNil(t, collector)

	// Cancel context to stop background goroutine
	cancel()

	// Give the background goroutine a moment to exit
	time.Sleep(10 * time.Millisecond)
}

func TestRepositoryCollectorCountByOrgAndVersion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test that the CountByOrgAndVersion method is used correctly
	mockResults := []store.CountByOrgAndVersionResult{
		{OrgID: "test-org", Version: "main", Count: 5},
		{OrgID: "test-org", Version: "develop", Count: 3},
	}

	mockStore := &MockRepositoryStore{count: 8, err: nil}
	mockStore.Repository().(*MockRepository).results = mockResults
	log := logrus.New()
	collector := NewRepositoryCollector(ctx, mockStore, log)

	// Verify the collector is created successfully
	assert.NotNil(t, collector)
	assert.Equal(t, "repository", collector.MetricsName())

	// Cancel context to stop background goroutine
	cancel()

	// Give the background goroutine a moment to exit
	time.Sleep(10 * time.Millisecond)
}
