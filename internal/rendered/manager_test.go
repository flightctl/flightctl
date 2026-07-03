package rendered

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKVStore is a minimal in-memory KVStore for unit testing.
type fakeKVStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newFakeKVStore() *fakeKVStore {
	return &fakeKVStore{data: make(map[string][]byte)}
}

func (f *fakeKVStore) preset(key string, value []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = value
}

func (f *fakeKVStore) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.data[key]
	return ok
}

func (f *fakeKVStore) Close() {}

func (f *fakeKVStore) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (f *fakeKVStore) SetNX(_ context.Context, key string, value []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.data[key]; exists {
		return false, nil
	}
	f.data[key] = value
	return true, nil
}

func (f *fakeKVStore) SetIfGreater(_ context.Context, key string, newVal int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.data[key]; ok {
		cur, err := strconv.ParseInt(string(existing), 10, 64)
		if err == nil && cur >= newVal {
			return false, nil
		}
	}
	f.data[key] = []byte(fmt.Sprintf("%d", newVal))
	return true, nil
}

func (f *fakeKVStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}

func (f *fakeKVStore) GetOrSetNX(_ context.Context, key string, value []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.data[key]; ok {
		return v, nil
	}
	f.data[key] = value
	return value, nil
}

func (f *fakeKVStore) DeleteKeysForTemplateVersion(_ context.Context, _ string) error { return nil }
func (f *fakeKVStore) DeleteAllKeys(_ context.Context) error                          { return nil }
func (f *fakeKVStore) PrintAllKeys(_ context.Context)                                 {}
func (f *fakeKVStore) StreamAdd(_ context.Context, _ string, _ []byte) (string, error) {
	return "", nil
}
func (f *fakeKVStore) StreamRange(_ context.Context, _, _, _ string) ([]kvstore.StreamEntry, error) {
	return nil, nil
}
func (f *fakeKVStore) StreamRead(_ context.Context, _ string, _ string, _ time.Duration, _ int64) ([]kvstore.StreamEntry, error) {
	return nil, nil
}
func (f *fakeKVStore) SetExpire(_ context.Context, _ string, _ time.Duration) error { return nil }

// recordingPublisher records the last Publish call.
type recordingPublisher struct {
	mu   sync.Mutex
	last *Notification
}

func (r *recordingPublisher) Publish(_ context.Context, _ uuid.UUID, _ string, n Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := n
	r.last = &cp
	return nil
}

func (r *recordingPublisher) lastNotification() *Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last == nil {
		return nil
	}
	cp := *r.last
	return &cp
}

// noopSubscriber satisfies the Subscriber interface but does nothing.
type noopSubscriber struct{}

func (noopSubscriber) Subscribe(_ context.Context, _ func(context.Context, uuid.UUID, string, Notification) error) error {
	return nil
}

// newTestVersionManager builds a VersionManager with fake dependencies.
func newTestVersionManager(kv *fakeKVStore, pub *recordingPublisher) *VersionManager {
	return &VersionManager{
		kvStore:             kv,
		broadcaster:         pub,
		subscriber:          noopSubscriber{},
		renderedWaitTimeout: 50 * time.Millisecond,
		log:                 logrus.NewEntry(logrus.New()),
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// WaitForNotification tests
// ──────────────────────────────────────────────────────────────────────────────

func TestWaitForNotification_ReturnsSpecUpdatedWhenKVHasNewerVersion(t *testing.T) {
	kv := newFakeKVStore()
	vm := newTestVersionManager(kv, &recordingPublisher{})

	orgId := uuid.New()
	kv.preset(vm.key(orgId, "dev"), []byte("2"))

	n, got, err := vm.WaitForNotification(context.Background(), orgId, "dev", "1")

	require.NoError(t, err)
	assert.True(t, got)
	assert.Equal(t, NotificationTypeSpecUpdated, n.Type)
	assert.Equal(t, "2", n.RenderedVersion)
}

func TestWaitForNotification_ReturnsConsoleWhenKVFlagSet(t *testing.T) {
	kv := newFakeKVStore()
	vm := newTestVersionManager(kv, &recordingPublisher{})

	orgId := uuid.New()
	kv.preset(vm.key(orgId, "dev"), []byte("5"))
	kv.preset(vm.consolePendingKey(orgId, "dev"), []byte("1"))

	// knownRenderedVersion matches KV — spec is unchanged, so console path fires.
	n, got, err := vm.WaitForNotification(context.Background(), orgId, "dev", "5")

	require.NoError(t, err)
	assert.True(t, got)
	assert.Equal(t, NotificationTypeConsole, n.Type)
	assert.Empty(t, n.RenderedVersion, "console notification carries no rendered version")
}

func TestWaitForNotification_SpecHasPriorityOverConsolePendingKey(t *testing.T) {
	// When both spec version and console-pending are set, SpecUpdated is returned first.
	kv := newFakeKVStore()
	vm := newTestVersionManager(kv, &recordingPublisher{})

	orgId := uuid.New()
	kv.preset(vm.key(orgId, "dev"), []byte("2"))
	kv.preset(vm.consolePendingKey(orgId, "dev"), []byte("1"))

	n, got, err := vm.WaitForNotification(context.Background(), orgId, "dev", "1")

	require.NoError(t, err)
	assert.True(t, got)
	assert.Equal(t, NotificationTypeSpecUpdated, n.Type)
	assert.Equal(t, "2", n.RenderedVersion)
}

func TestWaitForNotification_UnblocksViaConsumeHandlerConsole(t *testing.T) {
	kv := newFakeKVStore()
	vm := newTestVersionManager(kv, &recordingPublisher{})

	orgId := uuid.New()
	kv.preset(vm.key(orgId, "dev"), []byte("3"))

	type result struct {
		n   Notification
		got bool
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, got, err := vm.WaitForNotification(context.Background(), orgId, "dev", "3")
		ch <- result{n, got, err}
	}()

	time.Sleep(5 * time.Millisecond)
	// Simulate pub/sub delivery of a console notification via consumeHandler.
	_ = vm.consumeHandler(context.Background(), orgId, "dev", Notification{Type: NotificationTypeConsole})

	r := <-ch
	require.NoError(t, r.err)
	assert.True(t, r.got)
	assert.Equal(t, NotificationTypeConsole, r.n.Type)
}

func TestWaitForNotification_UnblocksViaConsumeHandlerSpecUpdated(t *testing.T) {
	kv := newFakeKVStore()
	vm := newTestVersionManager(kv, &recordingPublisher{})

	orgId := uuid.New()
	kv.preset(vm.key(orgId, "dev"), []byte("3"))

	type result struct {
		n   Notification
		got bool
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, got, err := vm.WaitForNotification(context.Background(), orgId, "dev", "3")
		ch <- result{n, got, err}
	}()

	time.Sleep(5 * time.Millisecond)
	_ = vm.consumeHandler(context.Background(), orgId, "dev", Notification{Type: NotificationTypeSpecUpdated, RenderedVersion: "4"})

	r := <-ch
	require.NoError(t, r.err)
	assert.True(t, r.got)
	assert.Equal(t, NotificationTypeSpecUpdated, r.n.Type)
	assert.Equal(t, "4", r.n.RenderedVersion)
}

func TestWaitForNotification_ReturnsTimeoutWhenNoEvent(t *testing.T) {
	kv := newFakeKVStore()
	vm := newTestVersionManager(kv, &recordingPublisher{})

	orgId := uuid.New()
	kv.preset(vm.key(orgId, "dev"), []byte("5"))

	n, got, err := vm.WaitForNotification(context.Background(), orgId, "dev", "5")

	require.NoError(t, err)
	assert.False(t, got)
	assert.Empty(t, n.Type)
}

func TestWaitForNotification_ReturnsErrorOnContextCancel(t *testing.T) {
	kv := newFakeKVStore()
	vm := newTestVersionManager(kv, &recordingPublisher{})

	orgId := uuid.New()
	kv.preset(vm.key(orgId, "dev"), []byte("5"))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, _, err := vm.WaitForNotification(ctx, orgId, "dev", "5")
		errCh <- err
	}()

	time.Sleep(5 * time.Millisecond)
	cancel()

	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)
}

// ──────────────────────────────────────────────────────────────────────────────
// NotifyConsole tests
// ──────────────────────────────────────────────────────────────────────────────

func TestNotifyConsole_SetsKVFlagAndPublishesConsoleNotification(t *testing.T) {
	kv := newFakeKVStore()
	pub := &recordingPublisher{}
	vm := newTestVersionManager(kv, pub)

	orgId := uuid.New()
	err := vm.NotifyConsole(context.Background(), orgId, "dev")

	require.NoError(t, err)
	assert.True(t, kv.has(vm.consolePendingKey(orgId, "dev")), "console-pending KV flag should be set")

	last := pub.lastNotification()
	require.NotNil(t, last)
	assert.Equal(t, NotificationTypeConsole, last.Type)
	assert.Empty(t, last.RenderedVersion)
}

func TestNotifyConsole_SetNXIsIdempotent(t *testing.T) {
	// Calling NotifyConsole twice should not fail; SetNX is a no-op on the second call.
	kv := newFakeKVStore()
	vm := newTestVersionManager(kv, &recordingPublisher{})

	orgId := uuid.New()
	require.NoError(t, vm.NotifyConsole(context.Background(), orgId, "dev"))
	require.NoError(t, vm.NotifyConsole(context.Background(), orgId, "dev"))
	assert.True(t, kv.has(vm.consolePendingKey(orgId, "dev")))
}

// ──────────────────────────────────────────────────────────────────────────────
// ClearConsoleNotification tests
// ──────────────────────────────────────────────────────────────────────────────

func TestClearConsoleNotification_DeletesKVFlag(t *testing.T) {
	kv := newFakeKVStore()
	vm := newTestVersionManager(kv, &recordingPublisher{})

	orgId := uuid.New()
	kv.preset(vm.consolePendingKey(orgId, "dev"), []byte("1"))

	require.NoError(t, vm.ClearConsoleNotification(context.Background(), orgId, "dev"))
	assert.False(t, kv.has(vm.consolePendingKey(orgId, "dev")), "console-pending KV flag should be deleted")
}

func TestClearConsoleNotification_NoopWhenFlagAbsent(t *testing.T) {
	kv := newFakeKVStore()
	vm := newTestVersionManager(kv, &recordingPublisher{})

	orgId := uuid.New()
	err := vm.ClearConsoleNotification(context.Background(), orgId, "dev")
	require.NoError(t, err)
}

// ──────────────────────────────────────────────────────────────────────────────
// StoreAndNotify tests
// ──────────────────────────────────────────────────────────────────────────────

func TestStoreAndNotify_PublishesSpecUpdatedNotification(t *testing.T) {
	kv := newFakeKVStore()
	pub := &recordingPublisher{}
	vm := newTestVersionManager(kv, pub)

	orgId := uuid.New()
	require.NoError(t, vm.StoreAndNotify(context.Background(), orgId, "dev", "7"))

	last := pub.lastNotification()
	require.NotNil(t, last)
	assert.Equal(t, NotificationTypeSpecUpdated, last.Type)
	assert.Equal(t, "7", last.RenderedVersion)
}

func TestStoreAndNotify_DoesNotPublishWhenVersionNotGreater(t *testing.T) {
	kv := newFakeKVStore()
	pub := &recordingPublisher{}
	vm := newTestVersionManager(kv, pub)

	orgId := uuid.New()
	require.NoError(t, vm.StoreAndNotify(context.Background(), orgId, "dev", "10"))
	pub.mu.Lock()
	pub.last = nil
	pub.mu.Unlock()

	// Try to store a lower version — SetIfGreater returns false, no publish.
	require.NoError(t, vm.StoreAndNotify(context.Background(), orgId, "dev", "5"))
	assert.Nil(t, pub.lastNotification(), "should not publish when new version is not greater than existing")
}

// ──────────────────────────────────────────────────────────────────────────────
// consolePendingKey format
// ──────────────────────────────────────────────────────────────────────────────

func TestConsolePendingKey_Format(t *testing.T) {
	vm := &VersionManager{}
	orgId := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	assert.Equal(t,
		"v1/00000000-0000-0000-0000-000000000001/device/my-device/console-pending",
		vm.consolePendingKey(orgId, "my-device"),
	)
}
