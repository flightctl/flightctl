package tasks_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type queueMaintenanceRecordingProvider struct {
	timeoutCalls    []queueMaintenanceTimeoutCall
	timeoutHandlers map[string]func(string, []byte, time.Duration) error
	retryCalls      []string
}

type queueMaintenanceTimeoutCall struct {
	queueName string
	timeout   time.Duration
}

func (p *queueMaintenanceRecordingProvider) NewQueueConsumer(context.Context, string) (queues.QueueConsumer, error) {
	return nil, nil
}

func (p *queueMaintenanceRecordingProvider) NewQueueProducer(context.Context, string) (queues.QueueProducer, error) {
	return nil, nil
}

func (p *queueMaintenanceRecordingProvider) NewPubSubPublisher(context.Context, string) (queues.PubSubPublisher, error) {
	return nil, nil
}

func (p *queueMaintenanceRecordingProvider) NewPubSubSubscriber(context.Context, string) (queues.PubSubSubscriber, error) {
	return nil, nil
}

func (p *queueMaintenanceRecordingProvider) ProcessTimedOutMessages(_ context.Context, queueName string, timeout time.Duration, handler func(string, []byte, time.Duration) error) (int, error) {
	if p.timeoutHandlers == nil {
		p.timeoutHandlers = make(map[string]func(string, []byte, time.Duration) error)
	}
	p.timeoutCalls = append(p.timeoutCalls, queueMaintenanceTimeoutCall{queueName: queueName, timeout: timeout})
	p.timeoutHandlers[queueName] = handler
	return 0, nil
}

func (p *queueMaintenanceRecordingProvider) RetryFailedMessages(_ context.Context, queueName string, _ queues.RetryConfig, _ func(string, []byte, int) error) (int, error) {
	p.retryCalls = append(p.retryCalls, queueName)
	return 0, nil
}

func (p *queueMaintenanceRecordingProvider) Stop()                             {}
func (p *queueMaintenanceRecordingProvider) Wait()                             {}
func (p *queueMaintenanceRecordingProvider) CheckHealth(context.Context) error { return nil }
func (p *queueMaintenanceRecordingProvider) GetLatestProcessedTimestamp(context.Context) (time.Time, error) {
	return time.Time{}, nil
}
func (p *queueMaintenanceRecordingProvider) AdvanceCheckpointAndCleanup(context.Context) error {
	return nil
}
func (p *queueMaintenanceRecordingProvider) SetCheckpointTimestamp(context.Context, time.Time) error {
	return nil
}

func TestQueueMaintenanceTaskWhenMaintainingQueuesItShouldUseEachQueueLease(t *testing.T) {
	provider := &queueMaintenanceRecordingProvider{}
	task := tasks.NewQueueMaintenanceTask(
		logrus.New(),
		nil,
		nil,
		nil,
		15*time.Minute,
		provider,
		nil,
		nil,
	)

	require.NoError(t, task.Execute(context.Background()))
	require.Equal(t, []queueMaintenanceTimeoutCall{
		{queueName: consts.TaskQueue, timeout: tasks.EventProcessingTimeout},
		{queueName: consts.DeltaGenerationTaskQueue, timeout: tasks.EventProcessingTimeout},
		{queueName: consts.DeltaGenerationCompletionQueue, timeout: tasks.EventProcessingTimeout},
	}, provider.timeoutCalls)
	require.Equal(t, []string{
		consts.TaskQueue,
		consts.DeltaGenerationTaskQueue,
		consts.DeltaGenerationCompletionQueue,
	}, provider.retryCalls)
}

func TestQueueMaintenanceTaskWhenDeltaJobHasLongerFleetTimeoutItShouldLeaveMessagePending(t *testing.T) {
	provider := &queueMaintenanceRecordingProvider{}
	controller := gomock.NewController(t)
	eventSvc := eventservice.NewMockService(controller)
	eventSvc.EXPECT().CreateEvent(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	task := tasks.NewQueueMaintenanceTask(
		logrus.New(),
		nil,
		eventSvc,
		nil,
		15*time.Minute,
		provider,
		nil,
		nil,
	)
	require.NoError(t, task.Execute(context.Background()))

	payload, err := json.Marshal(worker_client.EventWithOrgId{
		OrgId: uuid.New(),
		Event: domain.Event{
			Reason:  domain.EventReasonGenerateDelta,
			Message: `{"imageRepository":"repo","sourceDigest":"sha256:source","targetDigest":"sha256:target","timeout":"1h"}`,
		},
	})
	require.NoError(t, err)

	err = provider.timeoutHandlers[consts.DeltaGenerationTaskQueue]("delta-entry", payload, 30*time.Minute)
	require.ErrorIs(t, err, queues.ErrMessageNotTimedOut)
}
