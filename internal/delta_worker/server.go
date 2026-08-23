package delta_worker

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

type Server struct {
	cfg            *config.Config
	log            logrus.FieldLogger
	queuesProvider queues.Provider
	store          deltastore.Store
	workerMetrics  *worker.WorkerCollector
}

func New(cfg *config.Config, log logrus.FieldLogger, queuesProvider queues.Provider, store deltastore.Store, workerMetrics *worker.WorkerCollector) *Server {
	return &Server{
		cfg:            cfg,
		log:            log,
		queuesProvider: queuesProvider,
		store:          store,
		workerMetrics:  workerMetrics,
	}
}

func (s *Server) Run(ctx context.Context) error {
	if err := LaunchConsumers(ctx, s.queuesProvider, s.cfg, s.store, s.workerMetrics, s.log); err != nil {
		s.log.WithError(err).Error("failed to launch delta-generation consumers")
		return err
	}
	go func() {
		<-ctx.Done()
		s.queuesProvider.Stop()
	}()
	s.queuesProvider.Wait()
	return nil
}
