package rendered_version

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
	"github.com/sirupsen/logrus"
)

const (
	renderedVersionTimeout = 120 * time.Second // Timeout for waiting for a new rendered version
)

type Manager struct {
	kvStore     kvstore.KVStore
	subscriber  Subscriber
	broadcaster Broadcaster
	subscribers sync.Map
	log         logrus.FieldLogger
}

var Bus util.Singleton[Manager]

func New(kvStore kvstore.KVStore, provider queues.Provider, log logrus.FieldLogger) *Manager {
	broadcaster, err := NewBroadcaster(provider)
	if err != nil {
		log.Fatalf("Failed to create broadcaster for rendered version: %v", err)
	}
	subscriber, err := NewSubscriber(provider)
	if err != nil {
		log.Fatalf("Failed to create subscriber for rendered version: %v", err)
	}
	return &Manager{
		kvStore:     kvStore,
		broadcaster: broadcaster,
		subscriber:  subscriber,
		log:         log,
	}
}

func (m *Manager) key(orgId uuid.UUID, name string) string {
	return fmt.Sprintf("v1/%s/device/%s/rendered", orgId.String(), name)
}

func (m *Manager) subscribe(orgId uuid.UUID, name string, notifier chan string) {
	m.subscribers.Store(m.key(orgId, name), notifier)
}

func (m *Manager) unsubscribe(orgId uuid.UUID, name string) {
	m.subscribers.Delete(m.key(orgId, name))
}

func (m *Manager) WaitForNewVersion(ctx context.Context, orgId uuid.UUID, name string, knownRenderedVersion string) (bool, string, error) {
	ch := make(chan string, 1)
	m.subscribe(orgId, name, ch)
	defer m.unsubscribe(orgId, name)
	b, err := m.kvStore.Get(ctx, m.key(orgId, name))
	if err != nil {
		return false, "", fmt.Errorf("failed to get rendered version from kvstore: %v", err)
	}
	var currentRenderedVersion string
	if b != nil {
		currentRenderedVersion = string(b)
	}
	if currentRenderedVersion == knownRenderedVersion {
		timeout := time.NewTimer(renderedVersionTimeout)
		defer timeout.Stop()
		select {
		case <-timeout.C:
			return false, currentRenderedVersion, nil
		case currentRenderedVersion = <-ch:
		}
	}
	return true, currentRenderedVersion, nil
}

func (m *Manager) StoreAndNotify(ctx context.Context, orgId uuid.UUID, name string, renderedVersion string) error {
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
		if err := m.broadcaster.Broadcast(ctx, orgId, name, renderedVersion); err != nil {
			return fmt.Errorf("failed to broadcast rendered version: %w", err)
		}
	}
	return nil
}

func (m *Manager) consumeHandler(ctx context.Context, orgId uuid.UUID, name string, renderedVersion string) error {
	notifier, ok := m.subscribers.Load(m.key(orgId, name))
	if !ok {
		return nil
	}
	ch, isChan := notifier.(chan string)
	if !isChan {
		m.log.Errorf("GetRenderedDevice: notifier for %s/%s is not a channel, skipping notification", orgId, name)
		return nil
	}
	select {
	case ch <- renderedVersion:
	default:
		m.log.Warnf("GetRenderedDevice: channel for %s/%s is full, skipping notification", orgId, name)
	}
	return nil
}

func (m *Manager) Start(ctx context.Context) error {
	err := m.subscriber.Subscribe(ctx, m.consumeHandler)
	if err != nil {
		m.log.Errorf("failed to consume rendered version: %v", err)
	}
	return nil
}
