package rendered_version

import (
	"context"
	"encoding/json"

	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const queueName = "rendered_version_notifier"

type Broadcaster interface {
	Broadcast(ctx context.Context, orgId uuid.UUID, name string, renderedVersion string) error
}

type Subscriber interface {
	Subscribe(ctx context.Context, handler func(ctx context.Context, orgId uuid.UUID, name string, renderedVersion string) error) error
}

func NewBroadcaster(queuesProvider queues.Provider) (Broadcaster, error) {
	queuesPublisher, err := queuesProvider.NewBroadcaster(queueName)
	if err != nil {
		return nil, err
	}
	return &broadcaster{
		broadcaster: queuesPublisher,
	}, nil
}

func NewSubscriber(queuesProvider queues.Provider) (Subscriber, error) {
	subscriber, err := queuesProvider.NewSubscriber(queueName)
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
type broadcaster struct {
	broadcaster queues.Broadcaster
}

func (p *broadcaster) Broadcast(ctx context.Context, orgId uuid.UUID, name string, renderVersion string) error {
	s := serializedResourceId{
		OrgId:         orgId,
		Name:          name,
		RenderVersion: renderVersion,
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return p.broadcaster.Broadcast(ctx, b)
}

type consumer struct {
	subscriber queues.Subscriber
}

func (c *consumer) Subscribe(ctx context.Context, handler func(ctx context.Context, orgId uuid.UUID, name string, renderedVersion string) error) error {
	var s serializedResourceId
	queuesHandler := func(ctx context.Context, payload []byte, log logrus.FieldLogger) error {
		if err := json.Unmarshal(payload, &s); err != nil {
			log.WithError(err).Error("failed to unmarshal payload")
			return err
		}
		return handler(ctx, s.OrgId, s.Name, s.RenderVersion)
	}

	_, err := c.subscriber.Subscribe(ctx, queuesHandler)
	return err
}
