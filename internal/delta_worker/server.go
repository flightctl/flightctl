package delta_worker

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
)

type Server struct {
	cfg            *config.Config
	log            logrus.FieldLogger
	queuesProvider queues.Provider
	workerMetrics  *worker.WorkerCollector
}

func New(cfg *config.Config, log logrus.FieldLogger, queuesProvider queues.Provider, workerMetrics *worker.WorkerCollector) *Server {
	return &Server{
		cfg:            cfg,
		log:            log,
		queuesProvider: queuesProvider,
		workerMetrics:  workerMetrics,
	}
}

func (s *Server) Run(ctx context.Context) error {
	if err := LaunchConsumers(ctx, s.queuesProvider, s.cfg, s.workerMetrics, s.log); err != nil {
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
