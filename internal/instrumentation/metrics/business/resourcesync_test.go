package business

import (
	"context"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
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
func (m *MockResourceSyncStore) Event() store.Event                     { return nil }
func (m *MockResourceSyncStore) InitialMigration(context.Context) error { return nil }
func (m *MockResourceSyncStore) Close() error                           { return nil }

type MockResourceSync struct {
	results []store.CountByResourceSyncOrgAndStatusResult
}

func (m *MockResourceSync) InitialMigration(ctx context.Context) error { return nil }
func (m *MockResourceSync) Create(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, error) {
	return nil, nil
}
func (m *MockResourceSync) Update(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, api.ResourceUpdatedDetails, error) {
	return nil, api.ResourceUpdatedDetails{}, nil
}
func (m *MockResourceSync) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, bool, api.ResourceUpdatedDetails, error) {
	return nil, false, api.ResourceUpdatedDetails{}, nil
}
func (m *MockResourceSync) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error) {
	return nil, nil
}
func (m *MockResourceSync) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams) (*api.ResourceSyncList, error) {
	return nil, nil
}
func (m *MockResourceSync) Delete(ctx context.Context, orgId uuid.UUID, name string, callback store.RemoveOwnerCallback) error {
	return nil
}
func (m *MockResourceSync) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.ResourceSync) (*api.ResourceSync, error) {
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

	collector := NewResourceSyncCollector(ctx, mockStore, log, 1*time.Millisecond)
	time.Sleep(10 * time.Millisecond)

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

	t.Logf("Collected %d metrics", len(metrics))
}
