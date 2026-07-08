package dependencyref

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// fakeDependencyRefStore is a small in-memory/recording implementation of
// internal/store/dependencyref.Store.
type fakeDependencyRefStore struct {
	// recorded calls, keyed by method name, for assertions on forwarded arguments.
	deletedFleet                   string
	deletedDevice                  string
	replacedFleet                  string
	replacedFleetRefs              []model.DependencyRef
	replacedDeviceRefsByFleetFleet string
	replacedDeviceRefsByFleetRefs  []model.DependencyRef
	replacedFleetDeviceFleet       string
	replacedFleetDeviceDevice      string
	replacedFleetScopedDevice      string
	replacedStandaloneDevice       string
	bulkUpsertedRefs               []model.DependencyRef
	listedRefType                  string
	pollInterval                   time.Duration
	secretNamespace                string
	secretName                     string
	secretFingerprint              string

	refsByType    []model.DependencyRef
	gitProbes     []model.GitDependencyProbe
	httpProbes    []model.HttpDependencyProbe
	secretTargets []model.SecretDependencyRef

	err error
}

func (f *fakeDependencyRefStore) InitialMigration(_ context.Context) error { return nil }

func (f *fakeDependencyRefStore) Upsert(_ context.Context, _ uuid.UUID, _ *model.DependencyRef) error {
	return f.err
}

func (f *fakeDependencyRefStore) ListByRefType(_ context.Context, _ uuid.UUID, refType string) ([]model.DependencyRef, error) {
	f.listedRefType = refType
	if f.err != nil {
		return nil, f.err
	}
	return f.refsByType, nil
}

func (f *fakeDependencyRefStore) DeleteByFleet(_ context.Context, _ uuid.UUID, fleetName string) error {
	f.deletedFleet = fleetName
	return f.err
}

func (f *fakeDependencyRefStore) DeleteByDevice(_ context.Context, _ uuid.UUID, deviceName string) error {
	f.deletedDevice = deviceName
	return f.err
}

func (f *fakeDependencyRefStore) ReplaceByFleet(_ context.Context, _ uuid.UUID, fleetName string, refs []model.DependencyRef) error {
	f.replacedFleet = fleetName
	f.replacedFleetRefs = refs
	return f.err
}

func (f *fakeDependencyRefStore) ReplaceDeviceRefsByFleet(_ context.Context, _ uuid.UUID, fleetName string, refs []model.DependencyRef) error {
	f.replacedDeviceRefsByFleetFleet = fleetName
	f.replacedDeviceRefsByFleetRefs = refs
	return f.err
}

func (f *fakeDependencyRefStore) ReplaceByFleetDevice(_ context.Context, _ uuid.UUID, fleetName, deviceName string, _ []model.DependencyRef) error {
	f.replacedFleetDeviceFleet = fleetName
	f.replacedFleetDeviceDevice = deviceName
	return f.err
}

func (f *fakeDependencyRefStore) ReplaceFleetScopedDeviceRefs(_ context.Context, _ uuid.UUID, deviceName string, _ []model.DependencyRef) error {
	f.replacedFleetScopedDevice = deviceName
	return f.err
}

func (f *fakeDependencyRefStore) ReplaceByStandaloneDevice(_ context.Context, _ uuid.UUID, deviceName string, _ []model.DependencyRef) error {
	f.replacedStandaloneDevice = deviceName
	return f.err
}

func (f *fakeDependencyRefStore) BulkUpsertDeviceRefs(_ context.Context, _ uuid.UUID, refs []model.DependencyRef) error {
	f.bulkUpsertedRefs = refs
	return f.err
}

func (f *fakeDependencyRefStore) ListDueGitDependencies(_ context.Context, _ uuid.UUID, pollInterval time.Duration) ([]model.GitDependencyProbe, error) {
	f.pollInterval = pollInterval
	if f.err != nil {
		return nil, f.err
	}
	return f.gitProbes, nil
}

func (f *fakeDependencyRefStore) ListDueHttpDependencies(_ context.Context, _ uuid.UUID, pollInterval time.Duration) ([]model.HttpDependencyProbe, error) {
	f.pollInterval = pollInterval
	if f.err != nil {
		return nil, f.err
	}
	return f.httpProbes, nil
}

func (f *fakeDependencyRefStore) ListSecretDependencyTargets(_ context.Context, secretNamespace, secretName, newFingerprint string) ([]model.SecretDependencyRef, error) {
	f.secretNamespace = secretNamespace
	f.secretName = secretName
	f.secretFingerprint = newFingerprint
	if f.err != nil {
		return nil, f.err
	}
	return f.secretTargets, nil
}

func newTestHandler() (*ServiceHandler, *fakeDependencyRefStore) {
	store := &fakeDependencyRefStore{}
	return NewServiceHandler(store, logrus.New()), store
}

func TestDeleteDependencyRefsByFleet(t *testing.T) {
	t.Run("When the store succeeds it should return 200 and forward the fleet name", func(t *testing.T) {
		h, store := newTestHandler()
		status := h.DeleteDependencyRefsByFleet(context.Background(), uuid.New(), "fleet1")
		require.Equal(t, int32(200), status.Code)
		require.Equal(t, "fleet1", store.deletedFleet)
	})

	t.Run("When the store fails it should return an error status", func(t *testing.T) {
		h, store := newTestHandler()
		store.err = errors.New("boom")
		status := h.DeleteDependencyRefsByFleet(context.Background(), uuid.New(), "fleet1")
		require.Equal(t, int32(500), status.Code)
	})
}

func TestDeleteDependencyRefsByDevice(t *testing.T) {
	h, store := newTestHandler()
	status := h.DeleteDependencyRefsByDevice(context.Background(), uuid.New(), "dev1")
	require.Equal(t, int32(200), status.Code)
	require.Equal(t, "dev1", store.deletedDevice)
}

func TestReplaceDependencyRefsByFleet(t *testing.T) {
	h, store := newTestHandler()
	refs := []model.DependencyRef{{ResourceKey: "git:repo/main"}}
	status := h.ReplaceDependencyRefsByFleet(context.Background(), uuid.New(), "fleet1", refs)
	require.Equal(t, int32(200), status.Code)
	require.Equal(t, "fleet1", store.replacedFleet)
	require.Equal(t, refs, store.replacedFleetRefs)
}

func TestReplaceDeviceDependencyRefsByFleet(t *testing.T) {
	h, store := newTestHandler()
	refs := []model.DependencyRef{{ResourceKey: "git:repo/main"}}
	status := h.ReplaceDeviceDependencyRefsByFleet(context.Background(), uuid.New(), "fleet1", refs)
	require.Equal(t, int32(200), status.Code)
	require.Equal(t, "fleet1", store.replacedDeviceRefsByFleetFleet)
	require.Equal(t, refs, store.replacedDeviceRefsByFleetRefs)
}

func TestReplaceFleetDeviceDependencyRefs(t *testing.T) {
	h, store := newTestHandler()
	status := h.ReplaceFleetDeviceDependencyRefs(context.Background(), uuid.New(), "fleet1", "dev1", nil)
	require.Equal(t, int32(200), status.Code)
	require.Equal(t, "fleet1", store.replacedFleetDeviceFleet)
	require.Equal(t, "dev1", store.replacedFleetDeviceDevice)
}

func TestReplaceFleetScopedDeviceDependencyRefs(t *testing.T) {
	h, store := newTestHandler()
	status := h.ReplaceFleetScopedDeviceDependencyRefs(context.Background(), uuid.New(), "dev1", nil)
	require.Equal(t, int32(200), status.Code)
	require.Equal(t, "dev1", store.replacedFleetScopedDevice)
}

func TestReplaceStandaloneDeviceDependencyRefs(t *testing.T) {
	h, store := newTestHandler()
	status := h.ReplaceStandaloneDeviceDependencyRefs(context.Background(), uuid.New(), "dev1", nil)
	require.Equal(t, int32(200), status.Code)
	require.Equal(t, "dev1", store.replacedStandaloneDevice)
}

func TestBulkUpsertDeviceDependencyRefs(t *testing.T) {
	h, store := newTestHandler()
	refs := []model.DependencyRef{{ResourceKey: "git:repo/main"}}
	status := h.BulkUpsertDeviceDependencyRefs(context.Background(), uuid.New(), refs)
	require.Equal(t, int32(200), status.Code)
	require.Equal(t, refs, store.bulkUpsertedRefs)
}

func TestListDependencyRefsByRefType(t *testing.T) {
	t.Run("When the store succeeds it should return the refs", func(t *testing.T) {
		h, store := newTestHandler()
		store.refsByType = []model.DependencyRef{{ResourceKey: "git:repo/main"}}
		refs, status := h.ListDependencyRefsByRefType(context.Background(), uuid.New(), "git")
		require.Equal(t, int32(200), status.Code)
		require.Equal(t, "git", store.listedRefType)
		require.Equal(t, store.refsByType, refs)
	})

	t.Run("When the store returns not-found it should return 404", func(t *testing.T) {
		h, store := newTestHandler()
		store.err = flterrors.ErrResourceNotFound
		_, status := h.ListDependencyRefsByRefType(context.Background(), uuid.New(), "git")
		require.Equal(t, int32(404), status.Code)
	})
}

func TestListDueGitDependencies(t *testing.T) {
	h, store := newTestHandler()
	store.gitProbes = []model.GitDependencyProbe{{RepositoryName: "repo1", Revision: "main"}}
	probes, status := h.ListDueGitDependencies(context.Background(), uuid.New(), 30*time.Second)
	require.Equal(t, int32(200), status.Code)
	require.Equal(t, 30*time.Second, store.pollInterval)
	require.Equal(t, store.gitProbes, probes)
}

func TestListDueHttpDependencies(t *testing.T) {
	h, store := newTestHandler()
	store.httpProbes = []model.HttpDependencyProbe{{RepositoryName: "repo1", HTTPSuffix: "/path"}}
	probes, status := h.ListDueHttpDependencies(context.Background(), uuid.New(), 45*time.Second)
	require.Equal(t, int32(200), status.Code)
	require.Equal(t, 45*time.Second, store.pollInterval)
	require.Equal(t, store.httpProbes, probes)
}

func TestListSecretDependencyTargets(t *testing.T) {
	h, store := newTestHandler()
	store.secretTargets = []model.SecretDependencyRef{{FleetName: "fleet1", DeviceName: "dev1"}}
	targets, status := h.ListSecretDependencyTargets(context.Background(), "ns1", "secret1", "fingerprint1")
	require.Equal(t, int32(200), status.Code)
	require.Equal(t, "ns1", store.secretNamespace)
	require.Equal(t, "secret1", store.secretName)
	require.Equal(t, "fingerprint1", store.secretFingerprint)
	require.Equal(t, store.secretTargets, targets)
}
