package periodic

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"runtime"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type publisherTestFixture struct {
	publisher      *PeriodicTaskPublisher
	orgService     *mockOrganizationService
	channelManager *mockChannelManager
	tasksMetadata  map[PeriodicTaskType]PeriodicTaskMetadata
	taskBackoff    *poll.Config
}

func newPublisherTestFixture(t *testing.T) *publisherTestFixture {
	f := &publisherTestFixture{
		orgService:     &mockOrganizationService{},
		channelManager: &mockChannelManager{},
		tasksMetadata:  createTestTaskMetadata(),
		taskBackoff: &poll.Config{
			BaseDelay: 100 * time.Millisecond,
			Factor:    2,
			MaxDelay:  10 * time.Second,
		},
	}

	config := PeriodicTaskPublisherConfig{
		Log:            createPublisherTestLogger(),
		OrgService:     f.orgService,
		ChannelManager: f.channelManager,
		TasksMetadata:  f.tasksMetadata,
		TaskBackoff:    f.taskBackoff,
	}

	var err error
	f.publisher, err = NewPeriodicTaskPublisher(config)
	require.NoError(t, err)

	return f
}

func (f *publisherTestFixture) withOrganizations(count int) *publisherTestFixture {
	f.orgService.organizations = createTestOrganizations(count)
	f.orgService.status = api.Status{Code: 200}
	return f
}

func (f *publisherTestFixture) withChannelError(err error) *publisherTestFixture {
	f.channelManager.setShouldError(true, err)
	return f
}

func (f *publisherTestFixture) syncOrganizations(ctx context.Context) {
	f.publisher.syncOrganizations(ctx)
}

func (f *publisherTestFixture) addTaskToHeap(task *ScheduledTask) {
	f.publisher.mu.Lock()
	heap.Push(f.publisher.taskHeap, task)
	f.publisher.mu.Unlock()
}

func (f *publisherTestFixture) clearHeap() {
	f.publisher.clearHeap()
}

type mockOrganizationService struct {
	organizations *api.OrganizationList
	status        api.Status
	callCount     int
}

func (m *mockOrganizationService) ListOrganizations(ctx context.Context) (*api.OrganizationList, api.Status) {
	m.callCount++
	return m.organizations, m.status
}

func (m *mockOrganizationService) getCallCount() int {
	return m.callCount
}

type mockChannelManager struct {
	publishedTasks []PeriodicTaskReference
	shouldError    bool
	errorToReturn  error
}

func (m *mockChannelManager) PublishTask(ctx context.Context, taskRef PeriodicTaskReference) error {
	if m.shouldError {
		return m.errorToReturn
	}
	m.publishedTasks = append(m.publishedTasks, taskRef)
	return nil
}

func (m *mockChannelManager) getPublishedTaskCount() int {
	return len(m.publishedTasks)
}

func (m *mockChannelManager) setShouldError(shouldError bool, err error) {
	m.shouldError = shouldError
	m.errorToReturn = err
}

func createPublisherTestLogger() logrus.FieldLogger {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)
	return logger
}

func createTestOrganizations(count int) *api.OrganizationList {
	organizations := &api.OrganizationList{
		Items: make([]api.Organization, count),
	}

	for i := 0; i < count; i++ {
		orgID := uuid.New()
		orgIDStr := orgID.String()
		organizations.Items[i] = api.Organization{
			Metadata: api.ObjectMeta{
				Name: &orgIDStr,
			},
		}
	}

	return organizations
}

func createTestTaskMetadata() map[PeriodicTaskType]PeriodicTaskMetadata {
	return map[PeriodicTaskType]PeriodicTaskMetadata{
		PeriodicTaskTypeRepositoryTester: {Interval: 100 * time.Millisecond},
		PeriodicTaskTypeResourceSync:     {Interval: 200 * time.Millisecond},
	}
}

func createTestTask(orgID uuid.UUID, taskType PeriodicTaskType, nextRunOffset time.Duration, retries int) *ScheduledTask {
	return &ScheduledTask{
		NextRun:  time.Now().Add(nextRunOffset),
		OrgID:    orgID,
		TaskType: taskType,
		Interval: 1 * time.Hour,
		Retries:  retries,
	}
}

func withTestContext(t *testing.T, timeout time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(func() {
		cancel()
	})
	return ctx, cancel
}

func runSchedulingLoopWithTimeout(t *testing.T, publisher *PeriodicTaskPublisher, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Drain the wakeup channel before starting
	select {
	case <-publisher.wakeup:
	default:
	}

	publisher.wg.Add(1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		publisher.schedulingLoop(ctx)
	}()

	select {
	case <-done:
	case <-time.After(timeout * 2):
		t.Fatal("Scheduling loop did not exit within expected timeout")
	}
}

func assertTaskRescheduledWithInterval(t *testing.T, task *ScheduledTask, expectedInterval time.Duration) {
	t.Helper()
	now := time.Now()
	require.True(t, task.NextRun.After(now), "NextRun should be in the future")
	require.Equal(t, 0, task.Retries, "Retries should be reset")

	timeDiff := task.NextRun.Sub(now)
	tolerance := expectedInterval / 10 // 10% tolerance
	minExpected := expectedInterval - tolerance
	maxExpected := expectedInterval + tolerance
	if minExpected < 0 {
		minExpected = 0
	}

	require.True(t, timeDiff >= minExpected && timeDiff <= maxExpected,
		"NextRun should be approximately now + %v, got difference of %v", expectedInterval, timeDiff)
}

func assertTaskRescheduledWithBackoff(t *testing.T, task *ScheduledTask, pollConfig *poll.Config, expectedRetries int) {
	t.Helper()
	expectedBackoff := poll.CalculateBackoffDelay(pollConfig, expectedRetries)
	now := time.Now()

	require.Equal(t, expectedRetries, task.Retries)
	require.True(t, task.NextRun.After(now), "NextRun should be in the future")

	timeDiff := task.NextRun.Sub(now)
	require.True(t, timeDiff < expectedBackoff+100*time.Millisecond,
		"NextRun should use backoff delay of %v, got %v", expectedBackoff, timeDiff)
}

func TestPeriodicTaskPublisher_syncOrganizations(t *testing.T) {
	tests := []struct {
		name              string
		orgCount          int
		statusCode        int32
		expectedHeapSize  int
		expectedCallCount int
	}{
		{
			name:              "Success",
			orgCount:          2,
			statusCode:        200,
			expectedHeapSize:  4, // 2 orgs * 2 task types
			expectedCallCount: 1,
		},
		{
			name:              "ServerError",
			orgCount:          0,
			statusCode:        500,
			expectedHeapSize:  0, // No tasks due to failure
			expectedCallCount: 1,
		},
		{
			name:              "NotFoundError",
			orgCount:          0,
			statusCode:        404,
			expectedHeapSize:  0, // No tasks due to failure
			expectedCallCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPublisherTestFixture(t)
			f.orgService.organizations = createTestOrganizations(tt.orgCount)
			f.orgService.status = api.Status{Code: tt.statusCode}

			ctx := context.Background()
			f.syncOrganizations(ctx)

			require.Equal(t, tt.expectedCallCount, f.orgService.getCallCount())
			require.Equal(t, tt.expectedHeapSize, f.publisher.taskHeap.Len())
		})
	}
}

func TestPeriodicTaskPublisher_syncOrganizations_AddAndRemoveOrgs(t *testing.T) {
	f := newPublisherTestFixture(t)

	// First sync with 2 organizations
	orgs1 := createTestOrganizations(2)
	f.orgService.organizations = orgs1
	f.orgService.status = api.Status{Code: 200}

	ctx, cancel := withTestContext(t, 2*time.Second)
	defer cancel()

	f.syncOrganizations(ctx)
	require.Equal(t, 4, f.publisher.taskHeap.Len()) // 2 orgs * 2 task types

	// Second sync with different organizations (1 removed, 1 new)
	orgs2 := createTestOrganizations(2)
	orgs2.Items[0] = orgs1.Items[0] // Keep first org
	f.orgService.organizations = orgs2
	f.syncOrganizations(ctx)

	require.Equal(t, 4, f.publisher.taskHeap.Len())
	require.Equal(t, 2, f.orgService.getCallCount())

	for _, task := range *f.publisher.taskHeap {
		require.NotEqual(t, *orgs1.Items[1].Metadata.Name, task.OrgID.String())
	}
}

func TestPeriodicTaskPublisher_schedulingLoopTimer(t *testing.T) {
	tests := []struct {
		name               string
		setupTasks         func() []*ScheduledTask
		channelShouldError bool
		expectedPublished  int
		expectedHeapSize   int
		validateTasks      func(*testing.T, *publisherTestFixture)
	}{
		{
			name: "NoTasks",
			setupTasks: func() []*ScheduledTask {
				return nil
			},
			expectedPublished: 0,
			expectedHeapSize:  0,
		},
		{
			name: "TaskNotReady",
			setupTasks: func() []*ScheduledTask {
				return []*ScheduledTask{
					createTestTask(uuid.New(), PeriodicTaskTypeRepositoryTester, 1*time.Hour, 0),
				}
			},
			expectedPublished: 0,
			expectedHeapSize:  1,
			validateTasks: func(t *testing.T, f *publisherTestFixture) {
				task := f.publisher.taskHeap.Peek()
				require.NotNil(t, task)
				require.True(t, task.NextRun.After(time.Now().Add(55*time.Minute)))
			},
		},
		{
			name: "TaskReady",
			setupTasks: func() []*ScheduledTask {
				return []*ScheduledTask{
					createTestTask(uuid.New(), PeriodicTaskTypeRepositoryTester, -1*time.Minute, 2),
				}
			},
			expectedPublished: 1,
			expectedHeapSize:  1,
			validateTasks: func(t *testing.T, f *publisherTestFixture) {
				task := f.publisher.taskHeap.Peek()
				require.Equal(t, 0, task.Retries, "Retries should be reset")
				require.True(t, task.NextRun.After(time.Now()))
			},
		},
		{
			name: "TaskFailureRetry",
			setupTasks: func() []*ScheduledTask {
				return []*ScheduledTask{
					createTestTask(uuid.New(), PeriodicTaskTypeRepositoryTester, -1*time.Minute, 0),
				}
			},
			channelShouldError: true,
			expectedPublished:  0,
			expectedHeapSize:   1,
			validateTasks: func(t *testing.T, f *publisherTestFixture) {
				task := f.publisher.taskHeap.Peek()
				require.GreaterOrEqual(t, task.Retries, 1, "Task should be marked as retried")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPublisherTestFixture(t)

			if tt.channelShouldError {
				f.withChannelError(errors.New("test error"))
			}

			// Setup organizations if tasks reference them
			if tasks := tt.setupTasks(); len(tasks) > 0 {
				orgs := createTestOrganizations(len(tasks))
				f.orgService.organizations = orgs
				f.orgService.status = api.Status{Code: 200}

				// Update task OrgIDs if needed
				for i, task := range tasks {
					if i < len(orgs.Items) {
						task.OrgID = uuid.MustParse(*orgs.Items[i].Metadata.Name)
					}
					f.addTaskToHeap(task)
				}

				// Sync organizations to publisher's tracking
				f.syncOrganizations(context.Background())
				// Clear heap after sync and re-add our test tasks
				f.clearHeap()
				for _, task := range tasks {
					f.addTaskToHeap(task)
				}
			}

			runSchedulingLoopWithTimeout(t, f.publisher, 100*time.Millisecond)

			require.Equal(t, tt.expectedPublished, f.channelManager.getPublishedTaskCount())
			require.Equal(t, tt.expectedHeapSize, f.publisher.taskHeap.Len())

			if tt.validateTasks != nil {
				f.publisher.mu.Lock()
				tt.validateTasks(t, f)
				f.publisher.mu.Unlock()
			}
		})
	}
}

func TestPeriodicTaskPublisher_schedulingLoop_MultipleTasksReady(t *testing.T) {
	f := newPublisherTestFixture(t)

	// Create test organizations
	orgs := createTestOrganizations(3)
	f.orgService.organizations = orgs
	f.orgService.status = api.Status{Code: 200}
	orgIDs := []uuid.UUID{
		uuid.MustParse(*orgs.Items[0].Metadata.Name),
		uuid.MustParse(*orgs.Items[1].Metadata.Name),
		uuid.MustParse(*orgs.Items[2].Metadata.Name),
	}

	// Add organizations to publisher's tracking
	ctx := context.Background()
	f.syncOrganizations(ctx)
	f.clearHeap()

	now := time.Now()
	tasks := []*ScheduledTask{
		{
			NextRun:  now.Add(-10 * time.Minute), // Oldest
			OrgID:    orgIDs[0],
			TaskType: PeriodicTaskTypeRepositoryTester,
			Interval: 1 * time.Second,
			Retries:  1,
		},
		{
			NextRun:  now.Add(-1 * time.Minute), // Newest
			OrgID:    orgIDs[1],
			TaskType: PeriodicTaskTypeResourceSync,
			Interval: 2 * time.Second,
			Retries:  0,
		},
		{
			NextRun:  now.Add(-5 * time.Minute), // Middle
			OrgID:    orgIDs[2],
			TaskType: PeriodicTaskTypeRepositoryTester,
			Interval: 1500 * time.Millisecond,
			Retries:  3,
		},
	}

	// Add tasks in non-chronological order
	f.addTaskToHeap(tasks[1]) // Newest first
	f.addTaskToHeap(tasks[0]) // Oldest second
	f.addTaskToHeap(tasks[2]) // Middle third

	runSchedulingLoopWithTimeout(t, f.publisher, 200*time.Millisecond)

	// Verify all tasks were published
	require.Equal(t, 3, f.channelManager.getPublishedTaskCount())

	// Verify tasks were processed in NextRun time order
	publishedTasks := f.channelManager.publishedTasks
	require.Equal(t, orgIDs[0], publishedTasks[0].OrgID) // Oldest first
	require.Equal(t, orgIDs[2], publishedTasks[1].OrgID) // Middle second
	require.Equal(t, orgIDs[1], publishedTasks[2].OrgID) // Newest third

	// Verify all tasks were rescheduled
	require.Equal(t, 3, f.publisher.taskHeap.Len())

	// Verify all tasks have retries reset and NextRun in the future
	f.publisher.mu.Lock()
	var rescheduledTasks []*ScheduledTask
	for f.publisher.taskHeap.Len() > 0 {
		task := heap.Pop(f.publisher.taskHeap).(*ScheduledTask)
		rescheduledTasks = append(rescheduledTasks, task)
	}
	f.publisher.mu.Unlock()

	require.Len(t, rescheduledTasks, 3)
	for _, task := range rescheduledTasks {
		require.Equal(t, 0, task.Retries)
		require.True(t, task.NextRun.After(now))
	}
}

func TestPeriodicTaskPublisher_schedulingLoopWakeupSignal(t *testing.T) {
	f := newPublisherTestFixture(t)

	// Create test organization
	orgs := createTestOrganizations(1)
	f.orgService.organizations = orgs
	f.orgService.status = api.Status{Code: 200}
	orgID := uuid.MustParse(*orgs.Items[0].Metadata.Name)

	// Add organization to publisher's tracking
	ctx := context.Background()
	f.syncOrganizations(ctx)
	f.clearHeap()

	now := time.Now()
	tasks := []*ScheduledTask{
		{
			NextRun:  now.Add(1 * time.Minute), // Not ready to run
			OrgID:    orgID,
			TaskType: PeriodicTaskTypeResourceSync,
			Interval: 1 * time.Hour,
			Retries:  0,
		},
	}

	f.addTaskToHeap(tasks[0])

	// Explicitly signal wakeup to start the scheduler
	f.publisher.signalWakeup()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	f.publisher.wg.Add(1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.publisher.schedulingLoop(ctx)
	}()

	// Assert that the existing task is not published
	require.Equal(t, 0, f.channelManager.getPublishedTaskCount())

	// Add a new task that is ready to run and signal wakeup
	readyTask := createTestTask(orgID, PeriodicTaskTypeRepositoryTester, -1*time.Minute, 0)
	f.addTaskToHeap(readyTask)
	f.publisher.signalWakeup()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Scheduling loop did not exit within expected timeout")
	}

	// Assert that the newly added task was published
	require.Equal(t, 1, f.channelManager.getPublishedTaskCount())
	publishedTasks := f.channelManager.publishedTasks
	require.Equal(t, PeriodicTaskTypeRepositoryTester, publishedTasks[0].Type)
}

func TestPeriodicTaskPublisher_schedulingLoop_ContextCancellation(t *testing.T) {
	f := newPublisherTestFixture(t)

	initialGoroutines := runtime.NumGoroutine()
	t.Cleanup(func() {
		// Verify no goroutine leaks
		time.Sleep(10 * time.Millisecond)
		finalGoroutines := runtime.NumGoroutine()
		goroutineDiff := finalGoroutines - initialGoroutines
		require.LessOrEqual(t, goroutineDiff, 2,
			"Potential goroutine leak: started with %d, ended with %d",
			initialGoroutines, finalGoroutines)
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	f.publisher.wg.Add(1)
	go func() {
		defer close(done)
		f.publisher.schedulingLoop(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	require.GreaterOrEqual(t, runtime.NumGoroutine(), initialGoroutines)

	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Scheduling loop did not exit after cancellation")
	}

	require.Error(t, ctx.Err())
	require.Equal(t, context.Canceled, ctx.Err())
}

func TestPeriodicTaskPublisher_publishTask(t *testing.T) {
	tests := []struct {
		name               string
		setupOrgs          bool
		channelShouldError bool
		expectedError      bool
		expectedPublished  int
	}{
		{
			name:              "Success",
			setupOrgs:         true,
			expectedError:     false,
			expectedPublished: 1,
		},
		{
			name:               "ChannelManagerError",
			setupOrgs:          false,
			channelShouldError: true,
			expectedError:      true,
			expectedPublished:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPublisherTestFixture(t)

			if tt.channelShouldError {
				f.withChannelError(errors.New("channel manager test error"))
			}

			var orgID uuid.UUID
			if tt.setupOrgs {
				f.withOrganizations(1)
				orgID = uuid.MustParse(*f.orgService.organizations.Items[0].Metadata.Name)
				ctx := context.Background()
				f.syncOrganizations(ctx)
			} else {
				orgID = uuid.New()
			}

			ctx := context.Background()
			err := f.publisher.publishTask(ctx, PeriodicTaskTypeRepositoryTester, orgID)

			if tt.expectedError {
				require.Error(t, err)
			} else {
				require.Eventually(t, func() bool {
					return f.channelManager.getPublishedTaskCount() >= tt.expectedPublished
				}, 100*time.Millisecond, 5*time.Millisecond,
					"Timed out waiting for task to be published")
			}
		})
	}
}

func TestPeriodicTaskPublisher_clearHeap(t *testing.T) {
	f := newPublisherTestFixture(t).withOrganizations(2)

	ctx := context.Background()
	f.syncOrganizations(ctx)
	require.Equal(t, 4, f.publisher.taskHeap.Len()) // 2 orgs * 2 task types

	f.clearHeap()
	require.Equal(t, 0, f.publisher.taskHeap.Len())
}

func TestPeriodicTaskPublisher_reschedule(t *testing.T) {
	createRescheduleTask := func(retries int) *ScheduledTask {
		return &ScheduledTask{
			NextRun:  time.Now().Add(-10 * time.Minute),
			OrgID:    uuid.New(),
			TaskType: PeriodicTaskTypeRepositoryTester,
			Interval: 5 * time.Minute,
			Retries:  retries,
		}
	}

	t.Run("Success", func(t *testing.T) {
		f := newPublisherTestFixture(t)
		task := createRescheduleTask(3)

		f.publisher.rescheduleTask(task)

		assertTaskRescheduledWithInterval(t, task, task.Interval)
		require.Equal(t, 1, f.publisher.taskHeap.Len())

		// Verify task in heap
		taskInHeap := (*f.publisher.taskHeap)[0]
		require.Equal(t, task, taskInHeap)
	})

	t.Run("RetryWithBackoff", func(t *testing.T) {
		retryTests := []struct {
			initialRetries  int
			expectedRetries int
			expectedBackoff time.Duration
		}{
			{0, 1, 100 * time.Millisecond},
			{1, 2, 200 * time.Millisecond},
			{2, 3, 400 * time.Millisecond},
			{3, 4, 800 * time.Millisecond},
		}

		for _, tt := range retryTests {
			t.Run(fmt.Sprintf("Retry_%d", tt.initialRetries), func(t *testing.T) {
				f := newPublisherTestFixture(t)
				task := createRescheduleTask(tt.initialRetries)

				f.publisher.rescheduleTaskRetry(task)

				assertTaskRescheduledWithBackoff(t, task, f.taskBackoff, tt.expectedRetries)
				require.Equal(t, 1, f.publisher.taskHeap.Len())

				// Verify task in heap
				taskInHeap := (*f.publisher.taskHeap)[0]
				require.Equal(t, task, taskInHeap)
				require.Equal(t, tt.expectedRetries, taskInHeap.Retries)

				f.clearHeap()
			})
		}
	})
}
