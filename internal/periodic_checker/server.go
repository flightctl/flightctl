package periodic

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rollout/device_selection"
	"github.com/flightctl/flightctl/internal/rollout/disruption_budget"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/flightctl/flightctl/pkg/thread"
	"github.com/sirupsen/logrus"
)

type Server struct {
	cfg   *config.Config
	log   logrus.FieldLogger
	store store.Store
}

// New returns a new instance of a flightctl server.
func New(
	cfg *config.Config,
	log logrus.FieldLogger,
	store store.Store,
) *Server {
	return &Server{
		cfg:   cfg,
		log:   log,
		store: store,
	}
}

// TODO: expose metrics
func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-periodic")
	ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-periodic")
	defer cancel()

	queuesProvider, err := queues.NewRedisProvider(ctx, s.log, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password)
	if err != nil {
		return err
	}
	defer queuesProvider.Stop()

	kvStore, err := kvstore.NewKVStore(ctx, s.log, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password)
	if err != nil {
		return err
	}

	publisher, err := tasks_client.TaskQueuePublisher(queuesProvider)
	if err != nil {
		return err
	}
	callbackManager := tasks_client.NewCallbackManager(publisher, s.log)
	serviceHandler := service.WrapWithTracing(service.NewServiceHandler(s.store, callbackManager, kvStore, nil, s.log, "", ""))

	// repository tester
	repoTester := tasks.NewRepoTester(s.log, serviceHandler)
	repoTesterThread := thread.New(
		s.log.WithField("pkg", "repository-tester"), "Repository tester", 2*time.Minute, repoTester.TestRepositories)
	repoTesterThread.Start()
	defer repoTesterThread.Stop()

	// resource sync
	resourceSync := tasks.NewResourceSync(callbackManager, serviceHandler, s.log)
	resourceSyncThread := thread.New(
		s.log.WithField("pkg", "resourcesync"), "ResourceSync", 2*time.Minute, resourceSync.Poll)
	resourceSyncThread.Start()
	defer resourceSyncThread.Stop()

	// device disconnected
	deviceDisconnected := tasks.NewDeviceDisconnected(s.log, serviceHandler)
	deviceDisconnectedThread := thread.New(
		s.log.WithField("pkg", "device-disconnected"), "Device disconnected", tasks.DeviceDisconnectedPollingInterval, deviceDisconnected.Poll)
	deviceDisconnectedThread.Start()
	defer deviceDisconnectedThread.Stop()

	// Rollout device selection
	rolloutDeviceSelection := device_selection.NewReconciler(serviceHandler, callbackManager, s.log)
	rolloutDeviceSelectionThread := thread.New(
		s.log.WithField("pkg", "rollout-device-selection"), "Rollout device selection", device_selection.RolloutDeviceSelectionInterval, func() { rolloutDeviceSelection.Reconcile(ctx) })
	rolloutDeviceSelectionThread.Start()
	defer rolloutDeviceSelectionThread.Stop()

	// Rollout disruption budget
	disruptionBudget := disruption_budget.NewReconciler(serviceHandler, callbackManager, s.log)
	disruptionBudgetThread := thread.New(
		s.log.WithField("pkg", "disruption-budget"), "Disruption budget", disruption_budget.DisruptionBudgetReconcilationInterval, func() { disruptionBudget.Reconcile(ctx) })
	disruptionBudgetThread.Start()
	defer disruptionBudgetThread.Stop()

	// Event cleanup
	eventCleanup := tasks.NewEventCleanup(s.log, serviceHandler, s.cfg.Service.EventRetentionPeriod)
	eventCleanupThread := thread.New(
		s.log.WithField("pkg", "event-cleanup"), "Event cleanup", tasks.EventCleanupPollingInterval, eventCleanup.Poll)
	eventCleanupThread.Start()
	defer eventCleanupThread.Stop()

	sigShutdown := make(chan os.Signal, 1)

	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	<-sigShutdown
	s.log.Println("Shutdown signal received")
	return nil
}
