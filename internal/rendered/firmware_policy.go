package rendered

import (
	"context"
	"errors"
	"sync"

	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
)

const FirmwarePolicyChannel = "firmware_policy_notifier"

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
