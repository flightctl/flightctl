package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	"github.com/flightctl/flightctl/internal/delta_worker/model"
	workerservice "github.com/flightctl/flightctl/internal/delta_worker/service"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	generationcomplete "github.com/flightctl/flightctl/internal/delta_worker/tasks/generationcomplete"
	preparecomplete "github.com/flightctl/flightctl/internal/delta_worker/tasks/preparecomplete"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
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

type failingPreparer struct{}

func (failingPreparer) Prepare(context.Context, worker_client.EventWithOrgId) error {
	return errors.New("prepare failed")
}

type recordingCompletionStore struct {
	keys    []deltastore.GenerationKey
	prepare *model.DeltaPrepare
	gets    []*model.DeltaPrepare
}

func (r *recordingCompletionStore) CreateDeltaPrepare(context.Context, *model.DeltaPrepare) error {
	return nil
}

func (r *recordingCompletionStore) CreateOrReplaceWaitingDeltaPrepare(context.Context, *model.DeltaPrepare) (deltapreparestore.PrepareAdmission, error) {
	return deltapreparestore.PrepareAdmission{}, nil
}

func (r *recordingCompletionStore) GetDeltaPrepareByID(context.Context, uuid.UUID, ...deltapreparestore.PrepareGetOption) (*model.DeltaPrepare, error) {
	return r.getPrepare()
}

func (r *recordingCompletionStore) GetLatestDeltaPrepareForResource(context.Context, uuid.UUID, string, string, ...deltapreparestore.PrepareGetOption) (*model.DeltaPrepare, error) {
	return r.getPrepare()
}

func (r *recordingCompletionStore) getPrepare() (*model.DeltaPrepare, error) {
	if r.prepare == nil {
		return nil, nil
	}
	r.gets = append(r.gets, r.prepare)
	return r.prepare, nil
}

func (r *recordingCompletionStore) ListDeltaPrepares(context.Context, []uuid.UUID) ([]model.DeltaPrepare, error) {
	return nil, nil
}

func (r *recordingCompletionStore) UpdateDeltaPrepare(context.Context, int64, *model.DeltaPrepare) (*model.DeltaPrepare, error) {
	return nil, nil
}

func (r *recordingCompletionStore) DecrementPendingGenerationsForGeneration(_ context.Context, key deltastore.GenerationKey, _ string) ([]deltapreparestore.PrepareProgress, error) {
	r.keys = append(r.keys, key)
	return nil, nil
}

var _ deltapreparestore.Store = (*recordingCompletionStore)(nil)

type completionStatusStore struct{}

func (completionStatusStore) ResumeDeltaIfCurrent(context.Context, uuid.UUID, string, string) (bool, error) {
	return true, nil
}

func (completionStatusStore) SetOutOfDate(context.Context, uuid.UUID, string) error { return nil }

func (completionStatusStore) Mutate(_ context.Context, _ uuid.UUID, _ string, _ *domain.Device, apply devicestore.DeviceApplyFunc, _ ...devicestore.MutateOption) (*domain.Device, *domain.Device, bool, error) {
	resourceVersion := "3"
	device := &domain.Device{
		Metadata: domain.ObjectMeta{ResourceVersion: &resourceVersion},
		Status:   &domain.DeviceStatus{DeltaGeneration: &domain.DeltaGenerationStatus{}},
	}
	mutation := &devicestore.DeviceMutation{Device: device}
	if err := apply(mutation); err != nil {
		return nil, nil, false, err
	}
	return mutation.Device, device, false, nil
}

type completionEventServiceStub struct{}

func (completionEventServiceStub) CreateEvent(context.Context, uuid.UUID, *domain.Event) {}

func (completionEventServiceStub) HandleGenericResourceDeletedEvents(context.Context, domain.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {
}

func newCompletionServiceForTest(t *testing.T, store deltapreparestore.Store) *deltaprepare.ServiceHandler {
	handler, err := deltaprepare.NewCompletionService(
		store,
		workerservice.NewStorePreparingStatus(nil, completionStatusStore{}),
		completionEventServiceStub{},
	)
	require.NoError(t, err)
	return handler
}

func TestLaunchConsumers(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)
	ctx := context.Background()

	t.Run("When default config it should open DeltaGenerationTaskQueue twice", func(t *testing.T) {
		provider := &recordingProvider{}
		metrics := worker.NewWorkerCollector(ctx, log, config.NewDefault(), nil)
		err := LaunchConsumers(ctx, provider, &deltaconfig.DeltaGenerationConfig{}, metrics, log, nil)
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
		cfg := &deltaconfig.DeltaGenerationConfig{MaxConcurrentDeltaGenerations: 3}
		err := LaunchConsumers(ctx, provider, cfg, nil, log, nil)
		require.NoError(t, err)
		require.Len(t, provider.queueNames, 3)
		for _, name := range provider.queueNames {
			require.Equal(t, consts.DeltaGenerationTaskQueue, name)
		}
	})

	t.Run("When PrepareDeltas payload it should ack with nil error", func(t *testing.T) {
		provider := &recordingProvider{}
		metrics := worker.NewWorkerCollector(ctx, log, config.NewDefault(), nil)
		require.NoError(t, LaunchConsumers(ctx, provider, &deltaconfig.DeltaGenerationConfig{}, metrics, log, nil))
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

	t.Run("When PrepareDeltas handler fails it should not ack", func(t *testing.T) {
		provider := &recordingProvider{}
		require.NoError(t, LaunchConsumers(ctx, provider, &deltaconfig.DeltaGenerationConfig{}, nil, log, &ConsumerWiring{
			Preparer: failingPreparer{},
		}))
		payload, err := json.Marshal(worker_client.EventWithOrgId{
			OrgId: uuid.New(),
			Event: domain.Event{Reason: domain.EventReasonPrepareDeltas},
		})
		require.NoError(t, err)
		require.Error(t, provider.consumers[0].handler(ctx, payload, "3", provider.consumers[0], log))
		require.Equal(t, 0, provider.consumers[0].completeN)
	})

	t.Run("When DeltaGenerationComplete payload it should complete waiting prepares", func(t *testing.T) {
		provider := &recordingProvider{}
		completion := &recordingCompletionStore{}
		completionHandler, err := generationcomplete.NewHandler(newCompletionServiceForTest(t, completion), workerservice.NewStorePreparingStatus(nil, completionStatusStore{}))
		require.NoError(t, err)
		require.NoError(t, LaunchConsumers(ctx, provider, &deltaconfig.DeltaGenerationConfig{}, nil, log, &ConsumerWiring{
			Completion: completionHandler,
		}))
		orgID := uuid.New()
		generation := &model.DeltaGeneration{
			OrgID:           orgID,
			ImageRepository: "quay.io/example/os",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
			Status:          model.DeltaGenerationSucceeded,
		}
		event, err := deltageneration.NewGenerationCompleteEvent(generation)
		require.NoError(t, err)
		payload, err := json.Marshal(worker_client.EventWithOrgId{OrgId: orgID, Event: *event})
		require.NoError(t, err)

		require.NoError(t, provider.consumers[0].handler(ctx, payload, "5", provider.consumers[0], log))
		require.Equal(t, 1, provider.consumers[0].completeN)
		require.NoError(t, provider.consumers[0].completeErr)
		require.Equal(t, []deltastore.GenerationKey{{
			OrgID:           orgID,
			ImageRepository: generation.ImageRepository,
			SourceDigest:    generation.SourceDigest,
			TargetDigest:    generation.TargetDigest,
		}}, completion.keys)
	})

	t.Run("When DeltaPrepareComplete payload it should resume the prepare", func(t *testing.T) {
		provider := &recordingProvider{}
		completion := &recordingCompletionStore{}
		completionHandler, err := preparecomplete.NewHandler(newCompletionServiceForTest(t, completion))
		require.NoError(t, err)
		require.NoError(t, LaunchConsumers(ctx, provider, &deltaconfig.DeltaGenerationConfig{}, nil, log, &ConsumerWiring{
			PrepareComplete: completionHandler,
		}))
		orgID := uuid.New()
		prepare := &model.DeltaPrepare{
			ID:                    uuid.New(),
			OrgID:                 orgID,
			Kind:                  domain.DeviceKind,
			Name:                  "device-1",
			SourceResourceVersion: 3,
			ResourceVersion:       2,
			Status:                model.DeltaPrepareComplete,
		}
		completion.prepare = prepare
		event, err := deltaprepare.NewPrepareCompletionEvent(prepare)
		require.NoError(t, err)
		payload, err := json.Marshal(worker_client.EventWithOrgId{OrgId: orgID, Event: *event})
		require.NoError(t, err)

		require.NoError(t, provider.consumers[0].handler(ctx, payload, "6", provider.consumers[0], log))
		require.Equal(t, 1, provider.consumers[0].completeN)
		require.NoError(t, provider.consumers[0].completeErr)
		require.Empty(t, completion.gets)
	})

	t.Run("When garbage payload it should ack as permanent failure", func(t *testing.T) {
		provider := &recordingProvider{}
		metrics := worker.NewWorkerCollector(ctx, log, config.NewDefault(), nil)
		require.NoError(t, LaunchConsumers(ctx, provider, &deltaconfig.DeltaGenerationConfig{}, metrics, log, nil))
		require.NoError(t, provider.consumers[0].handler(ctx, []byte("not-json"), "2", provider.consumers[0], log))
		require.Equal(t, 1, provider.consumers[0].completeN)
		require.NoError(t, provider.consumers[0].completeErr)
		require.Equal(t, 1, testutil.CollectAndCount(metrics, "flightctl_worker_permanent_failures_total"))
	})

	t.Run("When invalid GenerateDelta payload it should ack as permanent failure", func(t *testing.T) {
		provider := &recordingProvider{}
		metrics := worker.NewWorkerCollector(ctx, log, config.NewDefault(), nil)
		require.NoError(t, LaunchConsumers(ctx, provider, &deltaconfig.DeltaGenerationConfig{}, metrics, log, nil))
		payload, err := json.Marshal(worker_client.EventWithOrgId{
			OrgId: uuid.New(),
			Event: domain.Event{Reason: domain.EventReasonGenerateDelta, Message: "{\"imageRepository\":\"not-valid\"}"},
		})
		require.NoError(t, err)
		require.NoError(t, provider.consumers[0].handler(ctx, payload, "4", provider.consumers[0], log))
		require.Equal(t, 1, provider.consumers[0].completeN)
		require.NoError(t, provider.consumers[0].completeErr)
		require.Equal(t, 1, testutil.CollectAndCount(metrics, "flightctl_worker_permanent_failures_total"))
		require.Equal(t, 0, testutil.CollectAndCount(metrics, "flightctl_worker_tasks_by_type_total"))
	})

	t.Run("When mis-routed task-queue payload it should ack as permanent failure", func(t *testing.T) {
		provider := &recordingProvider{}
		metrics := worker.NewWorkerCollector(ctx, log, config.NewDefault(), nil)
		require.NoError(t, LaunchConsumers(ctx, provider, &deltaconfig.DeltaGenerationConfig{}, metrics, log, nil))
		payload, err := json.Marshal(worker_client.EventWithOrgId{
			OrgId: uuid.New(),
			Event: domain.Event{Reason: domain.EventReasonFleetRolloutStarted},
		})
		require.NoError(t, err)
		require.NoError(t, provider.consumers[0].handler(ctx, payload, "4", provider.consumers[0], log))
		require.Equal(t, 1, provider.consumers[0].completeN)
		require.NoError(t, provider.consumers[0].completeErr)
		require.Equal(t, 1, testutil.CollectAndCount(metrics, "flightctl_worker_permanent_failures_total"))
		require.Equal(t, 0, testutil.CollectAndCount(metrics, "flightctl_worker_tasks_by_type_total"))
	})
}
