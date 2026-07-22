package rendered

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	renderedVersionDefaultTimeout = 120 * time.Second // Timeout for waiting for a new rendered version
)

type VersionManager struct {
	kvStore             kvstore.KVStore
	subscriber          Subscriber
	broadcaster         Publisher
	subscribers         sync.Map
	renderedWaitTimeout time.Duration
	log                 logrus.FieldLogger
}

type BusType struct {
	util.Singleton[VersionManager]
}

func (b *BusType) Initialize(ctx context.Context,
	kvStore kvstore.KVStore,
	provider queues.Provider,
	renderedWaitTimeout time.Duration,
	log logrus.FieldLogger) error {
	vm, err := newVersionManager(ctx, kvStore, provider, renderedWaitTimeout, log)
	if err != nil {
		return err
	}
	_ = b.GetOrInit(vm)
	return nil
}

var Bus BusType

func newVersionManager(ctx context.Context,
	kvStore kvstore.KVStore,
	provider queues.Provider,
	renderedWaitTimeout time.Duration,
	log logrus.FieldLogger) (*VersionManager, error) {
	broadcaster, err := NewBroadcaster(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher for rendered version: %v", err)
	}
	subscriber, err := NewSubscriber(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("failed to create subscriber for rendered version: %v", err)
	}
	return &VersionManager{
		kvStore:             kvStore,
		broadcaster:         broadcaster,
		subscriber:          subscriber,
		renderedWaitTimeout: lo.Ternary(renderedWaitTimeout > 0, renderedWaitTimeout, renderedVersionDefaultTimeout),
		log:                 log,
	}, nil
}

func (m *VersionManager) key(orgId uuid.UUID, name string) string {
	return fmt.Sprintf("v1/%s/device/%s/rendered", orgId.String(), name)
}

func (m *VersionManager) consolePendingKey(orgId uuid.UUID, name string) string {
	return fmt.Sprintf("v1/%s/device/%s/console-pending", orgId.String(), name)
}

func (m *VersionManager) subscribe(orgId uuid.UUID, name string, notifier chan Notification) {
	m.subscribers.Store(m.key(orgId, name), notifier)
}

func (m *VersionManager) unsubscribe(orgId uuid.UUID, name string) {
	m.subscribers.Delete(m.key(orgId, name))
}

// WaitForNotification blocks until a Notification occurs, then returns it.
// Returns (Notification{}, false, nil) on timeout — caller should return 204.
func (m *VersionManager) WaitForNotification(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion string) (Notification, bool, error) {
	ch := make(chan Notification, 1)
	m.subscribe(orgId, name, ch)
	defer m.unsubscribe(orgId, name)

	b, err := m.kvStore.Get(ctx, m.key(orgId, name))
	if err != nil {
		return Notification{}, false, fmt.Errorf("failed to get rendered version from kvstore: %v", err)
	}
	var currentRenderedVersion string
	if b != nil {
		currentRenderedVersion = string(b)
	}
	if currentRenderedVersion != knownRenderedVersion {
		return Notification{Type: NotificationTypeSpecUpdated, RenderedVersion: currentRenderedVersion}, true, nil
	}

	pending, err := m.kvStore.Get(ctx, m.consolePendingKey(orgId, name))
	if err != nil {
		m.log.Warnf("WaitForNotification: failed to check console notification for %s/%s: %v", orgId, name, err)
	} else if pending != nil {
		return Notification{Type: NotificationTypeConsole}, true, nil
	}

	timeout := time.NewTimer(m.renderedWaitTimeout)
	defer timeout.Stop()
	select {
	case <-ctx.Done():
		return Notification{}, false, ctx.Err()
	case <-timeout.C:
		return Notification{}, false, nil
	case n := <-ch:
		return n, true, nil
	}
}

func (m *VersionManager) StoreAndNotify(ctx context.Context, orgId uuid.UUID, name string, renderedVersion string) error {
	if name == "" {
		return fmt.Errorf("device name is required to store rendered version")
	}
	numericRenderedVersion, err := strconv.ParseInt(renderedVersion, 10, 64)
	if err != nil {
		return fmt.Errorf("rendered version must be a numeric value: %w", err)
	}
	if renderedVersion == "" {
		return fmt.Errorf("rendered version is required to store")
	}
	key := m.key(orgId, name)
	// The rendered version is stored only if a value is not already set or if the new version is greater than the existing one.
	// This allows us to avoid overwriting a newer version with an older one in case of race conditions.
	valueSet, err := m.kvStore.SetIfGreater(ctx, key, numericRenderedVersion)
	if err != nil {
		return fmt.Errorf("failed to store rendered version: %w", err)
	}
	if valueSet {
		if err := m.broadcaster.Publish(ctx, orgId, name, Notification{Type: NotificationTypeSpecUpdated, RenderedVersion: renderedVersion}); err != nil {
			return fmt.Errorf("failed to broadcast rendered version: %w", err)
		}
	}
	return nil
}

// NotifyConsole sets the console-pending KV flag and publishes a console notification
// so that a waiting GetRenderedDevice long-poll is unblocked immediately.
func (m *VersionManager) NotifyConsole(ctx context.Context, orgId uuid.UUID, name string) error {
	if _, err := m.kvStore.SetNX(ctx, m.consolePendingKey(orgId, name), []byte("1")); err != nil {
		return fmt.Errorf("failed to set console notification: %w", err)
	}
	return m.broadcaster.Publish(ctx, orgId, name, Notification{Type: NotificationTypeConsole})
}

// ClearConsoleNotification removes the console-pending KV flag after the agent has been woken.
func (m *VersionManager) ClearConsoleNotification(ctx context.Context, orgId uuid.UUID, name string) error {
	return m.kvStore.Delete(ctx, m.consolePendingKey(orgId, name))
}

func (m *VersionManager) consumeHandler(ctx context.Context, orgId uuid.UUID, name string, n Notification) error {
	notifier, ok := m.subscribers.Load(m.key(orgId, name))
	if !ok {
		return nil
	}
	ch, isChan := notifier.(chan Notification)
	if !isChan {
		m.log.Errorf("GetRenderedDevice: notifier for %s/%s is not a channel, skipping notification", orgId, name)
		return nil
	}
	select {
	case ch <- n:
	default:
		m.log.Warnf("GetRenderedDevice: channel for %s/%s is full, skipping notification", orgId, name)
	}
	return nil
}

func (m *VersionManager) Start(ctx context.Context) error {
	err := m.subscriber.Subscribe(ctx, m.consumeHandler)
	if err != nil {
		m.log.Errorf("failed to consume rendered version: %v", err)
		return err
	}
	return nil
}
