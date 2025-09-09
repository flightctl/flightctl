package periodic

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rendered"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
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

	processID := fmt.Sprintf("periodic-%s-%s", util.GetHostname(), uuid.New().String())
	queuesProvider, err := queues.NewRedisProvider(ctx, s.log, processID, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password, queues.DefaultRetryConfig())
	if err != nil {
		return err
	}
	defer func() {
		queuesProvider.Stop()
		queuesProvider.Wait()
	}()

	kvStore, err := kvstore.NewKVStore(ctx, s.log, s.cfg.KV.Hostname, s.cfg.KV.Port, s.cfg.KV.Password)
	if err != nil {
		return err
	}
	defer kvStore.Close()

	queuePublisher, err := worker_client.QueuePublisher(ctx, queuesProvider)
	if err != nil {
		return err
	}
	defer queuePublisher.Close()

	workerClient := worker_client.NewWorkerClient(queuePublisher, s.log)
	if err = rendered.Bus.Initialize(ctx, kvStore, queuesProvider, time.Duration(s.cfg.Service.RenderedWaitTimeout), s.log); err != nil {
		return err
	}
	serviceHandler := service.WrapWithTracing(service.NewServiceHandler(s.store, workerClient, kvStore, nil, s.log, "", "", []string{}))

	// Initialize the task executors
	periodicTaskExecutors := InitializeTaskExecutors(s.log, serviceHandler, s.cfg, queuesProvider, nil)

	// Create channel manager for task distribution
	channelManagerConfig := ChannelManagerConfig{
		Log: s.log,
	}
	if s.cfg.Periodic != nil {
		channelManagerConfig.ChannelBufferSize = s.cfg.Periodic.Consumers * 2
	}
	channelManager, err := NewChannelManager(channelManagerConfig)
	if err != nil {
		return err
	}
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
	periodicTaskConsumer, err := NewPeriodicTaskConsumer(consumerConfig)
	if err != nil {
		return err
	}

	// Periodic task publisher
	publisherConfig := PeriodicTaskPublisherConfig{
		Log:            s.log,
		OrgService:     serviceHandler,
		TasksMetadata:  periodicTasks,
		ChannelManager: channelManager,
		TaskBackoff: &poll.Config{
			BaseDelay: 100 * time.Millisecond,
			Factor:    3,
			MaxDelay:  10 * time.Second,
		},
	}
	periodicTaskPublisher, err := NewPeriodicTaskPublisher(publisherConfig)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		periodicTaskConsumer.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		periodicTaskPublisher.Run(ctx)
	}()

	sigShutdown := make(chan os.Signal, 1)
	signal.Notify(sigShutdown, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT)
	<-sigShutdown
	s.log.Println("Shutdown signal received")
	cancel()

	// Wait for consumer and publisher to finish
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.log.Info("Shutdown of publisher and consumer complete")
	case <-time.After(10 * time.Second):
		s.log.Error("Shutdown timeout exceeded, forcing exit")
		return fmt.Errorf("shutdown timeout exceeded")
	}

	return nil
}
