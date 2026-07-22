package rendered

import (
	"context"
	"encoding/json"

	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const queueName = "rendered_version_notifier"

// NotificationType distinguishes between different kinds of notifications that wake
// a device agent's long-poll. It is intentionally decoupled from the persisted
// auditing Event system (api/core/v1beta1).
type NotificationType string

const (
	NotificationTypeSpecUpdated NotificationType = "spec-updated"
	NotificationTypeConsole     NotificationType = "console"
)

// Notification is the internal signal sent over the pub/sub channel to unblock
// a waiting GetRenderedDevice long-poll on the API server.
type Notification struct {
	Type            NotificationType
	RenderedVersion string // only populated for NotificationTypeSpecUpdated
}

type Publisher interface {
	Publish(ctx context.Context, orgId uuid.UUID, name string, n Notification) error
}

type Subscriber interface {
	Subscribe(ctx context.Context, handler func(ctx context.Context, orgId uuid.UUID, name string, n Notification) error) error
}

func NewBroadcaster(ctx context.Context, queuesProvider queues.Provider) (Publisher, error) {
	queuesPublisher, err := queuesProvider.NewPubSubPublisher(ctx, queueName)
	if err != nil {
		return nil, err
	}
	return &publisher{
		broadcaster: queuesPublisher,
	}, nil
}

func NewSubscriber(ctx context.Context, queuesProvider queues.Provider) (Subscriber, error) {
	subscriber, err := queuesProvider.NewPubSubSubscriber(ctx, queueName)
	if err != nil {
		return nil, err
	}
	return &consumer{
		subscriber: subscriber,
	}, nil
}

type serializedResourceId struct {
	OrgId         uuid.UUID        `json:"org_id"`
	Name          string           `json:"name"`
	RenderVersion string           `json:"render_version,omitempty"`
	Type          NotificationType `json:"type,omitempty"`
}

type publisher struct {
	broadcaster queues.PubSubPublisher
}

func (p *publisher) Publish(ctx context.Context, orgId uuid.UUID, name string, n Notification) error {
	s := serializedResourceId{
		OrgId:         orgId,
		Name:          name,
		RenderVersion: n.RenderedVersion,
		Type:          n.Type,
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return p.broadcaster.Publish(ctx, b)
}

type consumer struct {
	subscriber    queues.PubSubSubscriber
	subscriptions []queues.Subscription
}

func (c *consumer) Subscribe(ctx context.Context, handler func(ctx context.Context, orgId uuid.UUID, name string, n Notification) error) error {
	queuesHandler := func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
		var s serializedResourceId
		if err := json.Unmarshal(payload, &s); err != nil {
			log.WithError(err).Error("failed to unmarshal payload")
			return err
		}
		return handler(ctx, s.OrgId, s.Name, Notification{
			Type:            s.Type,
			RenderedVersion: s.RenderVersion,
		})
	}

	if _, err := c.subscriber.Subscribe(ctx, queuesHandler); err != nil {
		return err
	}
	c.subscriptions = append(c.subscriptions, c.subscriber)
	return nil
}

func (c *consumer) Close() {
	for _, sub := range c.subscriptions {
		sub.Close()
	}
	c.subscriptions = nil
	c.subscriber.Close()
}
