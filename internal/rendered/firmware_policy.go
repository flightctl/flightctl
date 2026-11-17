package rendered

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const FirmwarePolicyChannel = "firmware_policy_notifier"
const defaultFirmwarePolicyWatchLimit = 16

var ErrTooManyFirmwarePolicyWatchers = errors.New("too many firmware policy watchers")

// FirmwarePolicyNotifier publishes hints only. Durable revision and delivery
// state live in the database; publishing never acknowledges agent receipt.
type FirmwarePolicyNotifier struct {
	provider  queues.Provider
	mu        sync.Mutex
	publisher Publisher
}

func NewFirmwarePolicyNotifier(provider queues.Provider) (*FirmwarePolicyNotifier, error) {
	if provider == nil {
		return nil, errors.New("firmware policy notifier requires a queue provider")
	}
	return &FirmwarePolicyNotifier{provider: provider}, nil
}

func (n *FirmwarePolicyNotifier) Notify(ctx context.Context, orgID uuid.UUID, name string, revision int64) error {
	if name == "" || revision <= 0 {
		return errors.New("firmware policy requires a device name and positive revision")
	}
	pub, err := n.getPublisher(ctx)
	if err != nil {
		return err
	}
	// Repeated publication is required when delivery succeeded but recording it failed.
	return pub.Publish(ctx, orgID, name, Notification{Type: NotificationTypeFirmwarePolicy, PolicyRevision: revision})
}

func (n *FirmwarePolicyNotifier) getPublisher(ctx context.Context) (Publisher, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.publisher != nil {
		return n.publisher, nil
	}
	channel, err := n.provider.NewPubSubPublisher(ctx, FirmwarePolicyChannel)
	if err != nil {
		return nil, err
	}
	if channel == nil {
		return nil, errors.New("firmware policy publisher is unavailable")
	}
	n.publisher = &publisher{broadcaster: channel}
	return n.publisher, nil
}

type policyDeviceKey struct {
	org  uuid.UUID
	name string
}

type FirmwarePolicyHub struct {
	mu           sync.Mutex
	watchers     map[policyDeviceKey]map[chan struct{}]struct{}
	watchLimit   int
	subscriber   queues.PubSubSubscriber
	subscription queues.Subscription
	cancel       context.CancelFunc
	closeOnce    sync.Once
}

type FirmwarePolicyHubOption func(*FirmwarePolicyHub)

func WithFirmwarePolicyWatchLimit(limit int) FirmwarePolicyHubOption {
	return func(h *FirmwarePolicyHub) {
		h.watchLimit = limit
	}
}

func NewFirmwarePolicyHub(ctx context.Context, provider queues.Provider, opts ...FirmwarePolicyHubOption) (*FirmwarePolicyHub, error) {
	if provider == nil {
		return nil, errors.New("firmware policy hub requires queue provider")
	}
	ctx, cancel := context.WithCancel(ctx)
	subscriber, err := provider.NewPubSubSubscriber(ctx, FirmwarePolicyChannel)
	if err != nil {
		cancel()
		return nil, err
	}
	if subscriber == nil {
		cancel()
		return nil, errors.New("firmware policy subscriber is unavailable")
	}
	h := &FirmwarePolicyHub{
		watchers:   map[policyDeviceKey]map[chan struct{}]struct{}{},
		watchLimit: defaultFirmwarePolicyWatchLimit,
		subscriber: subscriber,
		cancel:     cancel,
	}
	for _, opt := range opts {
		opt(h)
	}
	if h.watchLimit < 1 {
		cancel()
		subscriber.Close()
		return nil, errors.New("firmware policy watch limit must be positive")
	}
	subscription, err := subscriber.Subscribe(ctx, func(_ context.Context, payload []byte, _ logrus.FieldLogger) error {
		var n serializedResourceId
		if err := json.Unmarshal(payload, &n); err != nil {
			return err
		}
		if n.Type != NotificationTypeFirmwarePolicy || n.PolicyRevision <= 0 || n.Name == "" {
			return nil
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		for ch := range h.watchers[policyDeviceKey{n.OrgId, n.Name}] {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		return nil
	})
	if err != nil {
		cancel()
		subscriber.Close()
		return nil, err
	}
	if subscription == nil {
		cancel()
		subscriber.Close()
		return nil, errors.New("firmware policy subscription is unavailable")
	}
	h.subscription = subscription
	return h, nil
}

// Watch registers before the caller reads the authoritative policy head.
func (h *FirmwarePolicyHub) Watch(org uuid.UUID, name string) (<-chan struct{}, func(), error) {
	ch := make(chan struct{}, 1)
	key := policyDeviceKey{org, name}
	h.mu.Lock()
	if len(h.watchers[key]) >= h.watchLimit {
		h.mu.Unlock()
		return nil, nil, ErrTooManyFirmwarePolicyWatchers
	}
	if h.watchers[key] == nil {
		h.watchers[key] = map[chan struct{}]struct{}{}
	}
	h.watchers[key][ch] = struct{}{}
	h.mu.Unlock()
	unwatch := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.watchers[key], ch)
		if len(h.watchers[key]) == 0 {
			delete(h.watchers, key)
		}
	}
	return ch, unwatch, nil
}

func (h *FirmwarePolicyHub) Close() {
	h.closeOnce.Do(func() { h.cancel(); h.subscription.Close(); h.subscriber.Close() })
}
