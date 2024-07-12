package periodic

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
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
func (s *Server) Run() error {
	provider := queues.NewAmqpProvider(s.cfg.Queue.AmqpURL, s.log)
	defer provider.Stop()

	publisher, err := tasks.TaskQueuePublisher(provider)
	if err != nil {
		return err
	}
	callbackManager := tasks.NewCallbackManager(publisher, s.log)

	// repository tester
	repoTester := tasks.NewRepoTester(s.log, s.store)
	repoTesterThread := thread.New(
		s.log.WithField("pkg", "repository-tester"), "Repository tester", 2*time.Minute, repoTester.TestRepositories)
	repoTesterThread.Start()
	defer repoTesterThread.Stop()

	// resource sync
	resourceSync := tasks.NewResourceSync(callbackManager, s.store, s.log)
	resourceSyncThread := thread.New(
		s.log.WithField("pkg", "resourcesync"), "ResourceSync", 2*time.Minute, resourceSync.Poll)
	resourceSyncThread.Start()
	defer resourceSyncThread.Stop()

	// device disconnected
	deviceDisconnected := tasks.NewDeviceDisconnected(s.log, s.store)
	deviceDisconnectedThread := thread.New(
		s.log.WithField("pkg", "device-disconnected"), "Device disconnected", tasks.DeviceDisconnectedPollingInterval, deviceDisconnected.Poll)
	deviceDisconnectedThread.Start()
	defer deviceDisconnectedThread.Stop()

	sigShutdown := make(chan os.Signal, 1)

	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	<-sigShutdown
	s.log.Println("Shutdown signal received")
	return nil
}
