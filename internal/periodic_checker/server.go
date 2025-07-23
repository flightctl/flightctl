package periodic

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/pkg/queues"
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
	ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
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

	taskQueuePublisher, err := tasks_client.TaskQueuePublisher(queuesProvider)
	if err != nil {
		return err
	}
	callbackManager := tasks_client.NewCallbackManager(taskQueuePublisher, s.log)
	serviceHandler := service.WrapWithTracing(service.NewServiceHandler(s.store, callbackManager, kvStore, nil, s.log, "", "", []string{}))

	// Initialize the task executors
	periodicTaskExecutors := InitializeTaskExecutors(s.log, serviceHandler, callbackManager, s.cfg)

	// Create channel manager for task distribution
	channelManagerConfig := ChannelManagerConfig{
		Log: s.log,
	}
	if s.cfg.Periodic != nil {
		channelManagerConfig.ChannelBufferSize = s.cfg.Periodic.Consumers * 2
	}
	channelManager := NewChannelManager(channelManagerConfig)
	defer channelManager.Close()

	// Periodic task consumer
	consumerConfig := PeriodicTaskConsumerConfig{
		ChannelManager: channelManager,
		Log:            s.log,
		Executors:      periodicTaskExecutors,
	}
	if s.cfg.Periodic != nil {
		consumerConfig.ConsumerCount = s.cfg.Periodic.Consumers
	}
	periodicTaskConsumer := NewPeriodicTaskConsumer(consumerConfig)
	if err := periodicTaskConsumer.Start(ctx); err != nil {
		return err
	}
	defer periodicTaskConsumer.Stop()

	// Periodic task publisher
	publisherConfig := PeriodicTaskPublisherConfig{
		Log:            s.log,
		OrgService:     serviceHandler,
		TasksMetadata:  periodicTasks,
		ChannelManager: channelManager,
	}
	periodicTaskPublisher, err := NewPeriodicTaskPublisher(publisherConfig)
	if err != nil {
		return err
	}
	periodicTaskPublisher.Start(ctx)
	defer periodicTaskPublisher.Stop()

	sigShutdown := make(chan os.Signal, 1)

	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	<-sigShutdown
	s.log.Println("Shutdown signal received")
	return nil
}
