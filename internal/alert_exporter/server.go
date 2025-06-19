package alert_exporter

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/sirupsen/logrus"
)

type Server struct {
	cfg *config.Config
	log *logrus.Logger
}

// New returns a new instance of a flightctl server.
func New(
	cfg *config.Config,
	log *logrus.Logger,
) *Server {
	return &Server{
		cfg: cfg,
		log: log,
	}
}

func (s *Server) Run(ctx context.Context, serviceHandler service.Service) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	alertExporter := NewAlertExporter(s.log, serviceHandler, s.cfg)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		sig := <-sigCh
		s.log.Infof("Received signal %s, shutting down", sig)
		cancel()
	}()

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			s.log.Info("Context canceled, exiting alert exporter")
			return nil
		}

		err := alertExporter.Poll(ctx) // This runs its own ticker with s.interval
		if errors.Is(err, context.Canceled) {
			return nil
		}
		if err != nil {
			s.log.Errorf("Poller failed: %v. Restarting after %v...", err, backoff)
			select {
			case <-time.After(backoff):
				backoff *= 2
				if backoff > 60*time.Second {
					backoff = 60 * time.Second
				}
			case <-ctx.Done():
				s.log.Info("Context cancelled during backoff, exiting")
				return nil
			}
		} else {
			backoff = time.Second // Reset if Poll exited cleanly (unusual)
		}
	}
}
