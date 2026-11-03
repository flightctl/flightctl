package delta_worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	"github.com/flightctl/flightctl/internal/delta_worker/service"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltapreparegeneration"
	preparetask "github.com/flightctl/flightctl/internal/delta_worker/tasks/prepare"
	"github.com/flightctl/flightctl/internal/domain"
	device "github.com/flightctl/flightctl/internal/service/device"
	fleet "github.com/flightctl/flightctl/internal/service/fleet"
	repository "github.com/flightctl/flightctl/internal/service/repository"
	templateversion "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type deltaWorkerTestProducer struct {
	err      error
	payloads [][]byte
}

func (p *deltaWorkerTestProducer) Enqueue(_ context.Context, payload []byte, _ int64) error {
	if p.err != nil {
		return p.err
	}
	p.payloads = append(p.payloads, append([]byte(nil), payload...))
	return nil
}

func (p *deltaWorkerTestProducer) Close() {}

type deltaWorkerTestConsumer struct{}

func (c *deltaWorkerTestConsumer) Consume(context.Context, queues.ConsumeHandler) error {
	return nil
}

func (c *deltaWorkerTestConsumer) Complete(context.Context, string, []byte, error) error {
	return nil
}

func (c *deltaWorkerTestConsumer) Close() {}

type deltaWorkerTestProvider struct {
	producerNames []string
	consumerNames []string
}

func (p *deltaWorkerTestProvider) NewQueueProducer(_ context.Context, queueName string) (queues.QueueProducer, error) {
	p.producerNames = append(p.producerNames, queueName)
	return &deltaWorkerTestProducer{}, nil
}

func (p *deltaWorkerTestProvider) NewQueueConsumer(_ context.Context, queueName string) (queues.QueueConsumer, error) {
	p.consumerNames = append(p.consumerNames, queueName)
	return &deltaWorkerTestConsumer{}, nil
}

func (p *deltaWorkerTestProvider) NewPubSubPublisher(context.Context, string) (queues.PubSubPublisher, error) {
	return nil, nil
}

func (p *deltaWorkerTestProvider) NewPubSubSubscriber(context.Context, string) (queues.PubSubSubscriber, error) {
	return nil, nil
}

func (p *deltaWorkerTestProvider) ProcessTimedOutMessages(context.Context, string, time.Duration, func(string, []byte, time.Duration) error) (int, error) {
	return 0, nil
}

func (p *deltaWorkerTestProvider) RetryFailedMessages(context.Context, string, queues.RetryConfig, func(string, []byte, int) error) (int, error) {
	return 0, nil
}

func (p *deltaWorkerTestProvider) Stop() {}

func (p *deltaWorkerTestProvider) Wait() {}

func (p *deltaWorkerTestProvider) CheckHealth(context.Context) error {
	return nil
}

func (p *deltaWorkerTestProvider) GetLatestProcessedTimestamp(context.Context) (time.Time, error) {
	return time.Time{}, nil
}

func (p *deltaWorkerTestProvider) AdvanceCheckpointAndCleanup(context.Context) error {
	return nil
}

func (p *deltaWorkerTestProvider) SetCheckpointTimestamp(context.Context, time.Time) error {
	return nil
}

func TestEnqueueDeltaWorkerEvent(t *testing.T) {
	orgID := uuid.New()
	event := &domain.Event{Reason: domain.EventReasonGenerateDelta}

	t.Run("When producer is nil it should return an error", func(t *testing.T) {
		err := enqueueDeltaWorkerEvent(context.Background(), nil, orgID, event)
		require.ErrorContains(t, err, "queue producer is nil")
	})

	t.Run("When enqueue fails it should return the producer error", func(t *testing.T) {
		producer := &deltaWorkerTestProducer{err: errors.New("redis down")}
		err := enqueueDeltaWorkerEvent(context.Background(), producer, orgID, event)
		require.ErrorContains(t, err, "redis down")
	})

	t.Run("When enqueue succeeds it should write the worker event payload", func(t *testing.T) {
		producer := &deltaWorkerTestProducer{}
		require.NoError(t, enqueueDeltaWorkerEvent(context.Background(), producer, orgID, event))
		require.Len(t, producer.payloads, 1)

		var got worker_client.EventWithOrgId
		require.NoError(t, json.Unmarshal(producer.payloads[0], &got))
		require.Equal(t, orgID, got.OrgId)
		require.Equal(t, event.Reason, got.Event.Reason)
	})
}

func TestRun_WhenDeltaWorkerStartsItShouldOpenDedicatedCompletionQueue(t *testing.T) {
	controller := gomock.NewController(t)
	provider := &deltaWorkerTestProvider{}
	cfg := &deltaconfig.DeltaGenerationConfig{MaxConcurrentDeltaGenerations: 1}
	server := &Server{
		cfg:            cfg,
		log:            logrus.New(),
		queuesProvider: provider,
		generationSvc:  deltageneration.NewMockService(controller),
		prepareSvc:     deltaprepare.NewMockService(controller),
		prepareGenSvc:  deltapreparegeneration.NewMockService(controller),
		progressSvc:    deltapreparegeneration.NewProgressHandler(nil, nil, nil),
		prepareHandler: deltaprepare.NewServiceHandler(nil, nil),
		status:         service.NewStorePreparingStatus(nil, nil),
		repositorySvc:  repository.NewMockService(controller),
		resolver: &preparetask.Resolver{
			FleetService:           fleet.NewMockService(controller),
			DeviceService:          device.NewMockService(controller),
			RepositoryService:      repository.NewMockService(controller),
			TemplateVersionService: templateversion.NewMockService(controller),
			Config:                 cfg,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, server.Run(ctx))
	require.Equal(t, []string{
		consts.TaskQueue,
		consts.DeltaGenerationTaskQueue,
		consts.DeltaGenerationCompletionQueue,
	}, provider.producerNames)
	require.Equal(t, []string{
		consts.DeltaGenerationTaskQueue,
		consts.DeltaGenerationCompletionQueue,
	}, provider.consumerNames)
}
