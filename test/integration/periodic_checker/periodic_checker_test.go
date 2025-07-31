package periodic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	periodic "github.com/flightctl/flightctl/internal/periodic_checker"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var (
	suiteCtx context.Context
)

// Mock task executor for testing
type mockPeriodicTaskExecutor struct {
	mu               sync.Mutex
	executeCallCount int
	executeCallArgs  []executeCallArgs
	orgIDs           []uuid.UUID // Store org IDs received during execution
	panic            bool
}

type executeCallArgs struct {
	ctx context.Context
	log logrus.FieldLogger
}

func (m *mockPeriodicTaskExecutor) Execute(ctx context.Context, log logrus.FieldLogger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executeCallCount++
	m.executeCallArgs = append(m.executeCallArgs, executeCallArgs{ctx: ctx, log: log})

	// Extract and store org ID from context
	if orgID, ok := util.GetOrgIdFromContext(ctx); ok {
		m.orgIDs = append(m.orgIDs, orgID)
	}

	// Panic last so we record the call count and args first
	if m.panic {
		panic("test panic")
	}
}

func (m *mockPeriodicTaskExecutor) GetExecuteCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.executeCallCount
}

func (m *mockPeriodicTaskExecutor) GetExecuteCallArgs() []executeCallArgs {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Return a copy to avoid races
	result := make([]executeCallArgs, len(m.executeCallArgs))
	copy(result, m.executeCallArgs)
	return result
}

func (m *mockPeriodicTaskExecutor) GetOrgIDs() []uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Return a copy to avoid races
	result := make([]uuid.UUID, len(m.orgIDs))
	copy(result, m.orgIDs)
	return result
}

var repositoryTesterExecutor = &mockPeriodicTaskExecutor{}
var resourceSyncExecutor = &mockPeriodicTaskExecutor{}

// Helper functions
func createTestExecutors() map[periodic.PeriodicTaskType]periodic.PeriodicTaskExecutor {
	return map[periodic.PeriodicTaskType]periodic.PeriodicTaskExecutor{
		periodic.PeriodicTaskTypeRepositoryTester: repositoryTesterExecutor,
		periodic.PeriodicTaskTypeResourceSync:     resourceSyncExecutor,
	}
}

// Test periodic task metadata for publisher
var testPeriodicTasks = []periodic.PeriodicTaskMetadata{
	{
		TaskType: periodic.PeriodicTaskTypeRepositoryTester,
		Interval: time.Minute,
	},
	{
		TaskType: periodic.PeriodicTaskTypeResourceSync,
		Interval: 1 * time.Second,
	},
}

func TestStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Periodic Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Periodic Suite")
})

var _ = Describe("Periodic", func() {
	var (
		ctx                   context.Context
		kvStore               kvstore.KVStore
		log                   *logrus.Logger
		queuesProvider        queues.Provider
		serviceHandler        service.Service
		storeInst             store.Store
		cfg                   *config.Config
		dbName                string
		periodicTaskConsumer  *periodic.PeriodicTaskConsumer
		periodicTaskPublisher *periodic.PeriodicTaskPublisher
		callbackManager       tasks_client.CallbackManager
		orgId                 uuid.UUID
		publisherConfig       periodic.PeriodicTaskPublisherConfig
		consumerConfig        periodic.PeriodicTaskConsumerConfig
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-periodic")
		ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-periodic")
		ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)

		log = flightlog.InitLogs()

		// Setup database and store
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)

		// Grab default org id from the database
		orgs, err := storeInst.Organization().List(ctx)
		Expect(err).ToNot(HaveOccurred())
		if len(orgs) > 0 {
			orgId = orgs[0].ID
		} else {
			Fail("No orgs found in database")
		}

		queuesProvider, err = queues.NewRedisProvider(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())

		// Setup kvStore for test
		kvStore, err = kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())

		taskQueuePublisher, err := tasks_client.TaskQueuePublisher(queuesProvider)
		Expect(err).ToNot(HaveOccurred())

		// Setup callback manager and service handler
		callbackManager = tasks_client.NewCallbackManager(taskQueuePublisher, log)
		serviceHandler = service.NewServiceHandler(storeInst, callbackManager, kvStore, nil, log, "", "")

		repositoryTesterExecutor = &mockPeriodicTaskExecutor{}
		resourceSyncExecutor = &mockPeriodicTaskExecutor{}

		publisherConfig = periodic.PeriodicTaskPublisherConfig{
			Log:                log,
			KvStore:            kvStore,
			OrgService:         serviceHandler,
			QueuesProvider:     queuesProvider,
			TasksMetadata:      testPeriodicTasks,
			TaskTickerInterval: 100 * time.Millisecond,
		}

		consumerConfig = periodic.PeriodicTaskConsumerConfig{
			QueuesProvider: queuesProvider,
			Log:            log,
			Executors:      createTestExecutors(),
			ConsumerCount:  3,
		}
	})

	AfterEach(func() {
		// Stop consumer if running
		if periodicTaskConsumer != nil {
			periodicTaskConsumer.Stop()
		}

		// Stop queues provider
		if queuesProvider != nil {
			queuesProvider.Stop()
		}

		// Clean up kvStore
		if kvStore != nil {
			err := kvStore.DeleteAllKeys(ctx)
			Expect(err).ToNot(HaveOccurred())
			kvStore.Close()
		}

		// Clean up database
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
	})

	When("publishing tasks", func() {
		It("publishes tasks when an existing task is not in redis", func() {
			// Assert that no tasks are in redis
			taskKey := fmt.Sprintf("%s%s:%s", periodic.RedisKeyPeriodicTaskLastPublish, periodic.PeriodicTaskTypeRepositoryTester, orgId)
			lastPublish, err := kvStore.Get(ctx, taskKey)
			Expect(err).ToNot(HaveOccurred())
			Expect(lastPublish).To(BeNil())

			periodicTaskPublisher, err = periodic.NewPeriodicTaskPublisher(publisherConfig)
			Expect(err).ToNot(HaveOccurred())
			periodicTaskPublisher.Start(ctx)

			// Give some time for tasks to be processed and published
			Eventually(func() bool {
				lastPublish, err := kvStore.Get(ctx, taskKey)
				Expect(err).ToNot(HaveOccurred())
				return lastPublish != nil
			}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
		})

		It("publishes tasks when an existing task is in redis and the interval has passed", func() {
			// Insert a task into redis with a timestamp far enough in the past
			taskKey := fmt.Sprintf("%s%s:%s", periodic.RedisKeyPeriodicTaskLastPublish, periodic.PeriodicTaskTypeRepositoryTester, orgId)
			lastPublishTime := time.Now().Add(-1 * time.Hour)
			lastPublish := periodic.PeriodicTaskLastPublish{
				LastPublish: lastPublishTime,
			}
			lastPublishJSON, err := json.Marshal(lastPublish)
			Expect(err).ToNot(HaveOccurred())
			err = kvStore.Set(ctx, taskKey, lastPublishJSON)
			Expect(err).ToNot(HaveOccurred())

			// Initialize and start periodic task publisher
			periodicTaskPublisher, err = periodic.NewPeriodicTaskPublisher(publisherConfig)
			Expect(err).ToNot(HaveOccurred())
			periodicTaskPublisher.Start(ctx)

			// Give some time for tasks to be processed and published
			Eventually(func() bool {
				updatedLastPublish, err := kvStore.Get(ctx, taskKey)
				Expect(err).ToNot(HaveOccurred())

				// Assert the last publish time has been updated
				updatedLastPublishObj := periodic.PeriodicTaskLastPublish{}
				err = json.Unmarshal(updatedLastPublish, &updatedLastPublishObj)
				Expect(err).ToNot(HaveOccurred())
				return updatedLastPublishObj.LastPublish.After(lastPublishTime)
			}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
		})

		It("does not publish tasks when an existing task is in redis and the interval has not passed", func() {
			// Insert a task into redis with a last publish of now
			taskKey := fmt.Sprintf("%s%s:%s", periodic.RedisKeyPeriodicTaskLastPublish, periodic.PeriodicTaskTypeRepositoryTester, orgId)
			lastPublishTime := time.Now()
			lastPublish := periodic.PeriodicTaskLastPublish{
				LastPublish: lastPublishTime,
			}
			lastPublishJSON, err := json.Marshal(lastPublish)
			Expect(err).ToNot(HaveOccurred())
			err = kvStore.Set(ctx, taskKey, lastPublishJSON)
			Expect(err).ToNot(HaveOccurred())

			// Initialize and start periodic task publisher
			periodicTaskPublisher, err = periodic.NewPeriodicTaskPublisher(publisherConfig)
			Expect(err).ToNot(HaveOccurred())
			periodicTaskPublisher.Start(ctx)

			// Give some time for tasks to be processed and published
			Eventually(func() bool {
				updatedLastPublish, err := kvStore.Get(ctx, taskKey)
				Expect(err).ToNot(HaveOccurred())

				// Assert the last publish time has NOT been updated
				updatedLastPublishObj := periodic.PeriodicTaskLastPublish{}
				err = json.Unmarshal(updatedLastPublish, &updatedLastPublishObj)
				Expect(err).ToNot(HaveOccurred())
				return updatedLastPublishObj.LastPublish.Equal(lastPublishTime)
			}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
		})
	})

	When("Consuming tasks", func() {
		It("consumes tasks once per publish", func() {
			// Start the consumer
			periodicTaskConsumer = periodic.NewPeriodicTaskConsumer(consumerConfig)
			err := periodicTaskConsumer.Start(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Publish a task
			taskReference := periodic.PeriodicTaskReference{
				Type:  periodic.PeriodicTaskTypeRepositoryTester,
				OrgID: orgId,
			}
			publisher, err := queuesProvider.NewPublisher(consts.PeriodicTaskQueue)
			Expect(err).ToNot(HaveOccurred())
			taskReferenceJSON, err := json.Marshal(taskReference)
			Expect(err).ToNot(HaveOccurred())
			err = publisher.Publish(ctx, taskReferenceJSON)
			Expect(err).ToNot(HaveOccurred())

			// Give some time for the task to be consumed
			Eventually(func() bool {
				return repositoryTesterExecutor.GetExecuteCallCount() == 1
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())

			// Publish the same task again
			err = publisher.Publish(ctx, taskReferenceJSON)
			Expect(err).ToNot(HaveOccurred())

			// Assert the executor was called a second time
			Eventually(func() bool {
				return repositoryTesterExecutor.GetExecuteCallCount() == 2
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())
		})
	})

	When("running periodic tasks", func() {
		It("works for a single organization", func() {
			// Start the consumer
			periodicTaskConsumer = periodic.NewPeriodicTaskConsumer(consumerConfig)
			err := periodicTaskConsumer.Start(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Initialize and start periodic task publisher
			periodicTaskPublisher, err := periodic.NewPeriodicTaskPublisher(publisherConfig)
			Expect(err).ToNot(HaveOccurred())
			periodicTaskPublisher.Start(ctx)

			// Repository tester should be called once since it has a minute interval
			// Resource sync should be called numerous times as it is re-enqueued with a 1 second interval
			Eventually(func() bool {
				// Short circuit after 5 calls to resource sync which should be called roughly every second
				if repositoryTesterExecutor.GetExecuteCallCount() != 1 && resourceSyncExecutor.GetExecuteCallCount() < 5 {
					return false
				}

				return true
			}, 10*time.Second, 1*time.Second).Should(BeTrue())
		})

		It("works for multiple organizations", func() {
			// Create a second organization
			orgId2 := uuid.New()
			org := &model.Organization{
				ID:          orgId2,
				DisplayName: "test-org-2",
			}
			_, err := storeInst.Organization().Create(ctx, org)
			Expect(err).ToNot(HaveOccurred())

			// Start the consumer
			periodicTaskConsumer = periodic.NewPeriodicTaskConsumer(consumerConfig)
			err = periodicTaskConsumer.Start(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Initialize and start periodic task publisher
			periodicTaskPublisher, err := periodic.NewPeriodicTaskPublisher(publisherConfig)
			Expect(err).ToNot(HaveOccurred())
			periodicTaskPublisher.Start(ctx)

			// Repository tester should be called once for each org since it has a minute interval
			// Resource sync should be called numerous times for each org as it is re-enqueued with a 1 second interval
			Eventually(func() bool {
				repoTesterOrgIds := repositoryTesterExecutor.GetOrgIDs()
				resourceSyncOrgIds := resourceSyncExecutor.GetOrgIDs()

				if !slices.Contains(repoTesterOrgIds, orgId2) || !slices.Contains(resourceSyncOrgIds, orgId2) ||
					!slices.Contains(repoTesterOrgIds, orgId) || !slices.Contains(resourceSyncOrgIds, orgId) {
					return false
				}

				// Short circuit after ~5 calls per org to resource sync which should be called roughly every second
				if repositoryTesterExecutor.GetExecuteCallCount() != 2 && resourceSyncExecutor.GetExecuteCallCount() < 10 {
					return false
				}

				return true
			}, 10*time.Second, 1*time.Second).Should(BeTrue())
		})
	})
})
