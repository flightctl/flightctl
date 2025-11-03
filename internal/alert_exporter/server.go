package alert_exporter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

type Server struct {
	cfg       *config.Config
	log       *logrus.Logger
	startTime time.Time
}

// New returns a new instance of a flightctl server.
func New(
	cfg *config.Config,
	log *logrus.Logger,
) *Server {
	return &Server{
		cfg:       cfg,
		log:       log,
		startTime: time.Now(),
	}
}

func (s *Server) Run(ctx context.Context, serviceHandler service.Service) error {
	logger := s.log.WithFields(logrus.Fields{
		"component": "alert_exporter_server",
	})

	logger.Info("Starting alert exporter server")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start metrics server
	s.startMetricsServer(ctx)

	// Start uptime tracking
	go s.updateUptimeMetric(ctx)

	alertExporter := NewAlertExporter(s.log, serviceHandler, s.cfg)

	backoff := time.Second
	maxBackoff := time.Minute
	retryCount := 0

	for {
		if ctx.Err() != nil {
			logger.WithFields(logrus.Fields{
				"context_error": ctx.Err(),
				"retry_count":   retryCount,
			}).Info("Context canceled, exiting alert exporter")
			return nil
		}

		cycleLogger := logger.WithFields(logrus.Fields{
			"retry_count": retryCount,
			"backoff":     backoff,
		})

		cycleLogger.Debug("Starting polling cycle")

		err := alertExporter.Poll(ctx) // This runs its own ticker with s.interval
		if errors.Is(err, context.Canceled) {
			logger.Info("Alert exporter received context cancellation")
			return nil
		}
		if err != nil {
			retryCount++
			ErrorsTotal.WithLabelValues("server", "polling").Inc()
			cycleLogger.WithFields(logrus.Fields{
				"error":        err,
				"error_type":   fmt.Sprintf("%T", err),
				"retry_count":  retryCount,
				"next_backoff": backoff.String(),
			}).Error("Alert exporter polling failed, will retry after backoff")

			select {
			case <-time.After(backoff):
				// Exponential backoff
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			case <-ctx.Done():
				logger.WithField("context_error", ctx.Err()).
					Info("Context cancelled during backoff, exiting")
				return nil
			}
		} else {
			// Reset backoff on successful poll (though this is unusual for Poll to exit cleanly)
			if retryCount > 0 {
				logger.WithField("successful_after_retries", retryCount).
					Info("Alert exporter polling recovered successfully")
				retryCount = 0
			}
			backoff = time.Second
		}
	}
}

func (s *Server) startMetricsServer(ctx context.Context) {
	// Default metrics port for alert exporter
	metricsPort := 8081

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/health", s.healthHandler)

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", metricsPort),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	logger := s.log.WithFields(logrus.Fields{
		"component": "metrics_server",
		"port":      metricsPort,
	})

	go func() {
		logger.Info("Starting Prometheus metrics and health server")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.WithField("error", err).Error("Metrics server failed")
		}
	}()

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down metrics server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.WithField("error", err).Error("Error shutting down metrics server")
		}
	}()
}

// healthHandler provides a simple health check endpoint
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(s.startTime)

	health := map[string]interface{}{
		"status":         "ok",
		"uptime_seconds": uptime.Seconds(),
		"component":      "flightctl-alert-exporter",
		"timestamp":      time.Now().Unix(),
	}

	// Check if we have recent metrics (last successful processing)
	if LastSuccessfulProcessingTimestamp != nil {
		// This is a gauge metric, we can't directly read it, but if it exists it means we've processed at least once
		health["last_processing_check"] = "metrics_available"
	}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(health); err != nil {
		s.log.WithField("error", err).Error("Failed to encode health response")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

// updateUptimeMetric periodically updates the uptime metric
func (s *Server) updateUptimeMetric(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			UptimeSeconds.Set(time.Since(s.startTime).Seconds())
		}
	}
}
