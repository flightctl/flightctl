package agentlink

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// fakeKV implements the compare-and-set semantics the dispatch key relies on.
type fakeKV struct {
	kvstore.KVStore

	mu     sync.Mutex
	values map[string][]byte
}

func newFakeKV() *fakeKV { return &fakeKV{values: map[string][]byte{}} }

func (f *fakeKV) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = append([]byte(nil), value...)
	return nil
}

func (f *fakeKV) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[key], nil
}

func (f *fakeKV) ExpireIfMatch(_ context.Context, key string, expected []byte, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.values[key]) == string(expected), nil
}

func (f *fakeKV) DeleteIfMatch(_ context.Context, key string, expected []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if string(f.values[key]) != string(expected) {
		return false, nil
	}
	delete(f.values, key)
	return true, nil
}

func (f *fakeKV) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.values[key]
	return ok
}

func newTestRegistry(t *testing.T, kv kvstore.KVStore, replica string) *Registry {
	t.Helper()
	log := logrus.New()
	log.SetOutput(io.Discard)
	return NewRegistry(kv, replica, log)
}

var (
	testOrg    = uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")
	testDevice = "device-1"
)

func TestRegisterAndResolve(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	kv := newFakeKV()
	r := newTestRegistry(t, kv, "replica-a")

	lease, err := r.Register(ctx, testOrg, testDevice, "link-1")
	require.NoError(err)
	require.Equal("link-1", lease.LinkID())

	got, err := r.Resolve(ctx, testOrg, testDevice)
	require.NoError(err)
	require.NotNil(got)
	require.Equal("replica-a", got.ReplicaID)
	require.Equal("link-1", got.LinkID)
}

func TestResolveReportsNoLinkWhenNoneIsRegistered(t *testing.T) {
	require := require.New(t)

	r := newTestRegistry(t, newFakeKV(), "replica-a")

	got, err := r.Resolve(context.Background(), testOrg, "never-connected")

	require.NoError(err)
	require.Nil(got, "absence is not an error; it also cannot distinguish offline from reconnecting")
}

// TestReplacementSupersedesWithoutDisturbingThePreviousLink is the property
// that keeps a running request alive across a reconnect elsewhere: the
// registration gates new starts only.
func TestReplacementSupersedesWithoutDisturbingThePreviousLink(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	kv := newFakeKV()
	first := newTestRegistry(t, kv, "replica-a")
	second := newTestRegistry(t, kv, "replica-b")

	firstLease, err := first.Register(ctx, testOrg, testDevice, "link-1")
	require.NoError(err)

	// The newest link wins.
	secondLease, err := second.Register(ctx, testOrg, testDevice, "link-2")
	require.NoError(err)

	got, err := first.Resolve(ctx, testOrg, testDevice)
	require.NoError(err)
	require.Equal("link-2", got.LinkID)
	require.Equal("replica-b", got.ReplicaID)

	// The superseded holder can no longer refresh: it has lost the
	// registration, which is how it learns to stop.
	held, err := firstLease.Refresh(ctx)
	require.NoError(err)
	require.False(held)

	// The current holder still can.
	held, err = secondLease.Refresh(ctx)
	require.NoError(err)
	require.True(held)
}

// TestSupersededLeaseCannotDeleteItsReplacement is why deletion compares the
// stored value: a link closing late must not remove the registration that
// replaced it.
func TestSupersededLeaseCannotDeleteItsReplacement(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	kv := newFakeKV()
	r := newTestRegistry(t, kv, "replica-a")

	firstLease, err := r.Register(ctx, testOrg, testDevice, "link-1")
	require.NoError(err)
	_, err = r.Register(ctx, testOrg, testDevice, "link-2")
	require.NoError(err)

	require.NoError(firstLease.Release(ctx))

	got, err := r.Resolve(ctx, testOrg, testDevice)
	require.NoError(err)
	require.NotNil(got, "the replacement's registration must survive")
	require.Equal("link-2", got.LinkID)
}

func TestReleaseRemovesTheCurrentRegistration(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

	kv := newFakeKV()
	r := newTestRegistry(t, kv, "replica-a")

	lease, err := r.Register(ctx, testOrg, testDevice, "link-1")
	require.NoError(err)

	key := (&kvstore.AgentLinkDispatchKey{OrgID: testOrg, DeviceName: testDevice}).ComposeKey()
	require.True(kv.has(key))

	require.NoError(lease.Release(ctx))
	require.False(kv.has(key))
}

func TestKeepAliveStopsOnceSuperseded(t *testing.T) {
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kv := newFakeKV()
	r := newTestRegistry(t, kv, "replica-a")

	lease, err := r.Register(ctx, testOrg, testDevice, "link-1")
	require.NoError(err)

	// Supersede it before the keepalive runs.
	_, err = r.Register(ctx, testOrg, testDevice, "link-2")
	require.NoError(err)

	lease.refreshInterval = 5 * time.Millisecond

	done := make(chan struct{})
	go func() {
		defer close(done)
		lease.KeepAlive(ctx)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("KeepAlive did not stop after losing the registration")
	}
}

func TestRefreshIntervalIsComfortablyBelowTheTTL(t *testing.T) {
	require := require.New(t)
	// A single missed or slow refresh must not expire a healthy link.
	require.Less(DispatchRefreshInterval*2, DispatchTTL)
}

func TestDispatchKeyShape(t *testing.T) {
	require := require.New(t)

	key := (&kvstore.AgentLinkDispatchKey{OrgID: testOrg, DeviceName: testDevice}).ComposeKey()

	require.Equal("v1/agentlink/dispatch/"+testOrg.String()+"/"+testDevice, key)
}
