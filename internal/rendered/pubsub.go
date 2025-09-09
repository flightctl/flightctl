package rendered

import (
	"context"
	"encoding/json"

	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const queueName = "rendered_version_notifier"

type Publisher interface {
	Publish(ctx context.Context, orgId uuid.UUID, name string, renderedVersion string) error
}

type Subscriber interface {
	Subscribe(ctx context.Context, handler func(ctx context.Context, orgId uuid.UUID, name string, renderedVersion string) error) error
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
	OrgId         uuid.UUID `json:"org_id"`
	Name          string    `json:"name"`
	RenderVersion string    `json:"render_version,omitempty"`
}
type publisher struct {
	broadcaster queues.PubSubPublisher
}

func (p *publisher) Publish(ctx context.Context, orgId uuid.UUID, name string, renderVersion string) error {
	s := serializedResourceId{
		OrgId:         orgId,
		Name:          name,
		RenderVersion: renderVersion,
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

func (c *consumer) Subscribe(ctx context.Context, handler func(ctx context.Context, orgId uuid.UUID, name string, renderedVersion string) error) error {
	queuesHandler := func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
		var s serializedResourceId
		if err := json.Unmarshal(payload, &s); err != nil {
			log.WithError(err).Error("failed to unmarshal payload")
			return err
		}
		return handler(ctx, s.OrgId, s.Name, s.RenderVersion)
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
