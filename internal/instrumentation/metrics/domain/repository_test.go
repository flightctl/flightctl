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
type MockRepositoryStore struct {
	count   int64
	err     error
	results []store.CountByOrgResult
}

func (m *MockRepositoryStore) Repository() store.Repository {
	return &MockRepository{count: m.count, err: m.err, results: m.results}
}

// Implement other required methods with empty implementations
func (m *MockRepositoryStore) Device() store.Device                                       { return nil }
func (m *MockRepositoryStore) EnrollmentRequest() store.EnrollmentRequest                 { return nil }
func (m *MockRepositoryStore) CertificateSigningRequest() store.CertificateSigningRequest { return nil }
func (m *MockRepositoryStore) Fleet() store.Fleet                                         { return nil }
func (m *MockRepositoryStore) TemplateVersion() store.TemplateVersion                     { return nil }
func (m *MockRepositoryStore) ResourceSync() store.ResourceSync                           { return nil }
func (m *MockRepositoryStore) Event() store.Event                                         { return nil }
func (m *MockRepositoryStore) Checkpoint() store.Checkpoint                               { return nil }
func (m *MockRepositoryStore) Organization() store.Organization                           { return nil }
func (m *MockRepositoryStore) RunMigrations(context.Context) error                        { return nil }
func (m *MockRepositoryStore) Close() error                                               { return nil }

type MockRepository struct {
	count   int64
	err     error
	results []store.CountByOrgResult
}

func (m *MockRepository) Count(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (int64, error) {
	return m.count, m.err
}

func (m *MockRepository) CountByOrg(ctx context.Context, orgId *uuid.UUID) ([]store.CountByOrgResult, error) {
	return m.results, m.err
}

// Implement other required methods with empty implementations
func (m *MockRepository) InitialMigration(context.Context) error { return nil }
func (m *MockRepository) Create(context.Context, uuid.UUID, *api.Repository, store.EventCallback) (*api.Repository, error) {
	return nil, nil
}
func (m *MockRepository) Update(context.Context, uuid.UUID, *api.Repository, store.EventCallback) (*api.Repository, error) {
	return nil, nil
}
func (m *MockRepository) CreateOrUpdate(context.Context, uuid.UUID, *api.Repository, store.EventCallback) (*api.Repository, bool, error) {
	return nil, false, nil
}
func (m *MockRepository) Get(context.Context, uuid.UUID, string) (*api.Repository, error) {
	return nil, nil
}
func (m *MockRepository) List(context.Context, uuid.UUID, store.ListParams) (*api.RepositoryList, error) {
	return nil, nil
}
func (m *MockRepository) Delete(context.Context, uuid.UUID, string, store.EventCallback) error {
	return nil
}
func (m *MockRepository) UpdateStatus(context.Context, uuid.UUID, *api.Repository, store.EventCallback) (*api.Repository, error) {
	return nil, nil
}
func (m *MockRepository) GetFleetRefs(context.Context, uuid.UUID, string) (*api.FleetList, error) {
	return nil, nil
}
func (m *MockRepository) GetDeviceRefs(context.Context, uuid.UUID, string) (*api.DeviceList, error) {
	return nil, nil
}

func TestRepositoryCollector(t *testing.T) {
	// Provide mock SQL results for org aggregation
	mockResults := []store.CountByOrgResult{
		{OrgID: "org1", Count: 2},
		{OrgID: "org2", Count: 3},
	}

	mockStore := &MockRepositoryStore{results: mockResults}
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create collector with 1ms interval for fast testing
	config := config.NewDefault()
	collector := NewRepositoryCollector(ctx, mockStore, log, config)

	// Wait a bit for the collector to start and collect metrics
	time.Sleep(10 * time.Millisecond)

	// Test that the collector implements the required interfaces
	var _ prometheus.Collector = collector

	// Test MetricsName
	assert.Equal(t, "repository", collector.MetricsName())

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

func TestRepositoryCollectorWithError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test with error
	mockStore := &MockRepositoryStore{count: 0, err: assert.AnError}
	log := logrus.New()
	config := config.NewDefault()
	collector := NewRepositoryCollector(ctx, mockStore, log, config)

	// Test that the collector handles errors gracefully
	// The collector should not panic and should continue running
	assert.NotNil(t, collector)

	// Cancel context to stop background goroutine
	cancel()
}
