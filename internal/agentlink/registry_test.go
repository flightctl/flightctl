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

func (f *fakeKV) SetNX(_ context.Context, key string, value []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.values[key]; exists {
		return false, nil
	}
	f.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (f *fakeKV) SetExpire(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func (f *fakeKV) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.values[key]
	return ok
}

func (f *fakeKV) deleteKey(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.values, key)
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

func TestKeepAliveRecoversAfterExpiry(t *testing.T) {
	// Regression: when the registration expired (e.g. Redis down > TTL),
	// KeepAlive exited and left the device unreachable. The fix re-registers
	// with SetNX when the key is absent.
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kv := newFakeKV()
	r := newTestRegistry(t, kv, "replica-a")

	lease, err := r.Register(ctx, testOrg, testDevice, "link-1")
	require.NoError(err)
	lease.refreshInterval = 5 * time.Millisecond

	// Simulate expiry: delete the key so Refresh returns false.
	key := (&kvstore.AgentLinkDispatchKey{OrgID: testOrg, DeviceName: testDevice}).ComposeKey()
	kv.deleteKey(key)

	// KeepAlive should re-register rather than exiting.
	done := make(chan struct{})
	go func() {
		defer close(done)
		lease.KeepAlive(ctx)
	}()

	// Give KeepAlive at least one tick to recover.
	time.Sleep(30 * time.Millisecond)

	// The registration should have been restored.
	got, err := r.Resolve(ctx, testOrg, testDevice)
	require.NoError(err)
	require.NotNil(got, "recovery must restore the registration")
	require.Equal("link-1", got.LinkID)

	// Clean up.
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("KeepAlive did not stop after context cancel")
	}
}

func TestRecoveryDoesNotOverwriteNewerRegistration(t *testing.T) {
	// Regression: tryRecover uses SetNX so it cannot overwrite a newer
	// link's registration that arrived between the Get and the SetNX.
	require := require.New(t)
	ctx := context.Background()

	kv := newFakeKV()
	r := newTestRegistry(t, kv, "replica-a")

	// First link registers, then its registration expires.
	lease, err := r.Register(ctx, testOrg, testDevice, "link-1")
	require.NoError(err)
	key := (&kvstore.AgentLinkDispatchKey{OrgID: testOrg, DeviceName: testDevice}).ComposeKey()
	kv.deleteKey(key)

	// A newer link registers while the old one's key is absent.
	r2 := newTestRegistry(t, kv, "replica-b")
	_, err = r2.Register(ctx, testOrg, testDevice, "link-2")
	require.NoError(err)

	// The old link's tryRecover must NOT overwrite link-2.
	recovered := lease.tryRecover(ctx)
	require.False(recovered, "recovery must fail when a newer link registered")

	got, err := r.Resolve(ctx, testOrg, testDevice)
	require.NoError(err)
	require.Equal("link-2", got.LinkID, "newer registration must be preserved")
}

func TestSupersededLinkDoesNotRecoverAfterObservingSupersession(t *testing.T) {
	// A link that sees a different registration (superseded, not expired)
	// must exit KeepAlive rather than attempting recovery.
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kv := newFakeKV()
	r := newTestRegistry(t, kv, "replica-a")

	lease, err := r.Register(ctx, testOrg, testDevice, "link-1")
	require.NoError(err)
	lease.refreshInterval = 5 * time.Millisecond

	// Supersede with a new registration.
	_, err = r.Register(ctx, testOrg, testDevice, "link-2")
	require.NoError(err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		lease.KeepAlive(ctx)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("KeepAlive did not stop after supersession")
	}

	// Registration must still be link-2, not resurrected link-1.
	got, err := r.Resolve(ctx, testOrg, testDevice)
	require.NoError(err)
	require.Equal("link-2", got.LinkID)
}

func TestDispatchKeyShape(t *testing.T) {
	require := require.New(t)

	key := (&kvstore.AgentLinkDispatchKey{OrgID: testOrg, DeviceName: testDevice}).ComposeKey()

	require.Equal("v1/agentlink/dispatch/"+testOrg.String()+"/"+testDevice, key)
}
