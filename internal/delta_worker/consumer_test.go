package delta_worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type recordingConsumer struct {
	handler     queues.ConsumeHandler
	completeN   int
	completeErr error
}

func (c *recordingConsumer) Consume(_ context.Context, handler queues.ConsumeHandler) error {
	c.handler = handler
	return nil
}

func (c *recordingConsumer) Complete(_ context.Context, _ string, _ []byte, processingErr error) error {
	c.completeN++
	c.completeErr = processingErr
	return nil
}

func (c *recordingConsumer) Close() {}

type recordingProvider struct {
	queueNames []string
	consumers  []*recordingConsumer
}

func (p *recordingProvider) NewQueueConsumer(_ context.Context, queueName string) (queues.QueueConsumer, error) {
	p.queueNames = append(p.queueNames, queueName)
	c := &recordingConsumer{}
	p.consumers = append(p.consumers, c)
	return c, nil
}

func (p *recordingProvider) NewQueueProducer(_ context.Context, _ string) (queues.QueueProducer, error) {
	return nil, nil
}
func (p *recordingProvider) NewPubSubPublisher(_ context.Context, _ string) (queues.PubSubPublisher, error) {
	return nil, nil
}
func (p *recordingProvider) NewPubSubSubscriber(_ context.Context, _ string) (queues.PubSubSubscriber, error) {
	return nil, nil
}
func (p *recordingProvider) ProcessTimedOutMessages(_ context.Context, _ string, _ time.Duration, _ func(string, []byte) error) (int, error) {
	return 0, nil
}
func (p *recordingProvider) RetryFailedMessages(_ context.Context, _ string, _ queues.RetryConfig, _ func(string, []byte, int) error) (int, error) {
	return 0, nil
}
func (p *recordingProvider) Stop()                               {}
func (p *recordingProvider) Wait()                               {}
func (p *recordingProvider) CheckHealth(_ context.Context) error { return nil }
func (p *recordingProvider) GetLatestProcessedTimestamp(_ context.Context) (time.Time, error) {
	return time.Time{}, nil
}
func (p *recordingProvider) AdvanceCheckpointAndCleanup(_ context.Context) error { return nil }
func (p *recordingProvider) SetCheckpointTimestamp(_ context.Context, _ time.Time) error {
	return nil
}

func TestLaunchConsumers(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	ctx := context.Background()

	t.Run("When default config it should open DeltaGenerationTaskQueue twice", func(t *testing.T) {
		provider := &recordingProvider{}
		metrics := worker.NewWorkerCollector(ctx, log, config.NewDefault(), nil)
		err := LaunchConsumers(ctx, provider, config.NewDefault(), nil, metrics, log)
		require.NoError(t, err)
		require.Equal(t, []string{consts.DeltaGenerationTaskQueue, consts.DeltaGenerationTaskQueue}, provider.queueNames)
		require.NoError(t, testutil.CollectAndCompare(metrics, strings.NewReader(`
# HELP flightctl_worker_consumers_active Number of active consumer goroutines
# TYPE flightctl_worker_consumers_active gauge
flightctl_worker_consumers_active 2
`), "flightctl_worker_consumers_active"))
	})

	t.Run("When maxConcurrentDeltaGenerations is 3 it should open three consumers", func(t *testing.T) {
		provider := &recordingProvider{}
		cfg := &config.Config{DeltaGeneration: &config.DeltaGenerationConfig{MaxConcurrentDeltaGenerations: 3}}
		err := LaunchConsumers(ctx, provider, cfg, nil, nil, log)
		require.NoError(t, err)
		require.Len(t, provider.queueNames, 3)
		for _, name := range provider.queueNames {
			require.Equal(t, consts.DeltaGenerationTaskQueue, name)
		}
	})

	t.Run("When PrepareDeltas payload it should ack with nil error", func(t *testing.T) {
		provider := &recordingProvider{}
		metrics := worker.NewWorkerCollector(ctx, log, config.NewDefault(), nil)
		require.NoError(t, LaunchConsumers(ctx, provider, config.NewDefault(), nil, metrics, log))
		payload, err := json.Marshal(worker_client.EventWithOrgId{
			OrgId: uuid.New(),
			Event: domain.Event{Reason: domain.EventReasonPrepareDeltas},
		})
		require.NoError(t, err)
		require.NoError(t, provider.consumers[0].handler(ctx, payload, "1", provider.consumers[0], log))
		require.Equal(t, 1, provider.consumers[0].completeN)
		require.NoError(t, provider.consumers[0].completeErr)
		require.Equal(t, 1, testutil.CollectAndCount(metrics, "flightctl_worker_tasks_by_type_total"))
	})

	t.Run("When garbage payload it should ack with nil error", func(t *testing.T) {
		provider := &recordingProvider{}
		require.NoError(t, LaunchConsumers(ctx, provider, config.NewDefault(), nil, nil, log))
		require.NoError(t, provider.consumers[0].handler(ctx, []byte("not-json"), "2", provider.consumers[0], log))
		require.Equal(t, 1, provider.consumers[0].completeN)
		require.NoError(t, provider.consumers[0].completeErr)
	})
}
