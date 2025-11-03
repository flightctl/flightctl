package periodic_test

import (
	"context"
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
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

var suiteCtx context.Context

// Mock task executor for testing
type mockPeriodicTaskExecutor struct {
	mu               sync.Mutex
	executeCallCount int
	orgIDs           []uuid.UUID // Store org IDs received during execution
	panic            bool
}

func (m *mockPeriodicTaskExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executeCallCount++
	m.orgIDs = append(m.orgIDs, orgID)

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

func (m *mockPeriodicTaskExecutor) GetOrgIDs() []uuid.UUID {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]uuid.UUID, len(m.orgIDs))
	copy(result, m.orgIDs)
	return result
}

// Test periodic task metadata for publisher
var testPeriodicTasks = map[periodic.PeriodicTaskType]periodic.PeriodicTaskMetadata{
	periodic.PeriodicTaskTypeRepositoryTester: {
		Interval: 5 * time.Second,
	},
	periodic.PeriodicTaskTypeResourceSync: {
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
		ctx                      context.Context
		kvStore                  kvstore.KVStore
		log                      *logrus.Logger
		queuesProvider           queues.Provider
		serviceHandler           service.Service
		storeInst                store.Store
		cfg                      *config.Config
		dbName                   string
		workerClient             worker_client.WorkerClient
		orgId                    uuid.UUID
		publisherConfig          periodic.PeriodicTaskPublisherConfig
		consumerConfig           periodic.PeriodicTaskConsumerConfig
		repositoryTesterExecutor *mockPeriodicTaskExecutor
		resourceSyncExecutor     *mockPeriodicTaskExecutor
		cancel                   context.CancelFunc
		channelManager           *periodic.ChannelManager
	)

	BeforeEach(func() {
		baseCtx := testutil.StartSpecTracerForGinkgo(suiteCtx)
		baseCtx = context.WithValue(baseCtx, consts.EventSourceComponentCtxKey, "flightctl-periodic")
		baseCtx = context.WithValue(baseCtx, consts.EventActorCtxKey, "service:flightctl-periodic")
		baseCtx = context.WithValue(baseCtx, consts.InternalRequestCtxKey, true)
		ctx, cancel = context.WithCancel(baseCtx)

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

		processID := fmt.Sprintf("periodic-test-%s", uuid.New().String())
		queuesProvider, err = queues.NewRedisProvider(ctx, log, processID, "localhost", 6379, "adminpass", queues.DefaultRetryConfig())
		Expect(err).ToNot(HaveOccurred())

		// Setup kvStore for test
		kvStore, err = kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())

		queuePublisher, err := worker_client.QueuePublisher(ctx, queuesProvider)
		Expect(err).ToNot(HaveOccurred())

		// Setup worker client and service handler
		workerClient = worker_client.NewWorkerClient(queuePublisher, log)
		serviceHandler = service.NewServiceHandler(storeInst, workerClient, kvStore, nil, log, "", "", []string{})

		channelManager, err = periodic.NewChannelManager(periodic.ChannelManagerConfig{
			Log: log,
		})
		Expect(err).ToNot(HaveOccurred())

		publisherConfig = periodic.PeriodicTaskPublisherConfig{
			Log:            log,
			OrgService:     serviceHandler,
			TasksMetadata:  testPeriodicTasks,
			ChannelManager: channelManager,
			TaskBackoff: &poll.Config{
				BaseDelay: 1 * time.Minute, // Make backoff very long to make it clear if it is triggered
				Factor:    2,
				MaxDelay:  2 * time.Minute,
			},
		}

		repositoryTesterExecutor = &mockPeriodicTaskExecutor{}
		resourceSyncExecutor = &mockPeriodicTaskExecutor{}
		executors := map[periodic.PeriodicTaskType]periodic.PeriodicTaskExecutor{
			periodic.PeriodicTaskTypeRepositoryTester: repositoryTesterExecutor,
			periodic.PeriodicTaskTypeResourceSync:     resourceSyncExecutor,
		}
		consumerConfig = periodic.PeriodicTaskConsumerConfig{
			ChannelManager: channelManager,
			Log:            log,
			Executors:      executors,
			ConsumerCount:  3,
		}
	})

	AfterEach(func() {
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

		// Clean up channel manager
		if channelManager != nil {
			channelManager.Close()
		}

		if cancel != nil {
			cancel()
		}
	})

	When("running periodic tasks", func() {
		It("runs tasks on configured intervals", func() {
			// Start consumer and publisher together
			eg, egCtx := errgroup.WithContext(ctx)

			periodicTaskConsumer, err := periodic.NewPeriodicTaskConsumer(consumerConfig)
			Expect(err).ToNot(HaveOccurred())
			eg.Go(func() error {
				periodicTaskConsumer.Run(egCtx)
				return nil
			})

			periodicTaskPublisher, err := periodic.NewPeriodicTaskPublisher(publisherConfig)
			Expect(err).ToNot(HaveOccurred())
			eg.Go(func() error {
				periodicTaskPublisher.Run(egCtx)
				return nil
			})

			// Give some time for tasks to be processed and published
			Eventually(func() bool {
				return repositoryTesterExecutor.GetExecuteCallCount() >= 1 && resourceSyncExecutor.GetExecuteCallCount() >= 4
			}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
		})

		It("works for multiple organizations", func() {
			externalID2 := "external-id-2"
			// Create a second organization
			orgId2 := uuid.New()
			org := &model.Organization{
				ID:          orgId2,
				DisplayName: "test-org-2",
				ExternalID:  externalID2,
			}
			_, err := storeInst.Organization().Create(ctx, org)
			Expect(err).ToNot(HaveOccurred())

			// Start consumer and publisher together
			eg, egCtx := errgroup.WithContext(ctx)

			periodicTaskConsumer, err := periodic.NewPeriodicTaskConsumer(consumerConfig)
			Expect(err).ToNot(HaveOccurred())
			eg.Go(func() error {
				periodicTaskConsumer.Run(egCtx)
				return nil
			})

			periodicTaskPublisher, err := periodic.NewPeriodicTaskPublisher(publisherConfig)
			Expect(err).ToNot(HaveOccurred())
			eg.Go(func() error {
				periodicTaskPublisher.Run(egCtx)
				return nil
			})

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
				if repositoryTesterExecutor.GetExecuteCallCount() < 2 && resourceSyncExecutor.GetExecuteCallCount() < 10 {
					return false
				}

				return true
			}, 10*time.Second, 1*time.Second).Should(BeTrue())
		})
	})
})
