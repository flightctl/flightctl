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

func (s *Server) Run(shutdownCtx context.Context, serviceHandler service.Service) error {
	logger := s.log.WithFields(logrus.Fields{
		"component": "alert_exporter_server",
	})

	logger.Info("Starting alert exporter server")

	// Start metrics server with longer grace period
	s.startMetricsServer(shutdownCtx)

	// Start uptime tracking
	go s.updateUptimeMetric(shutdownCtx)

	alertExporter := NewAlertExporter(s.log, serviceHandler, s.cfg)

	backoff := time.Second
	maxBackoff := time.Minute
	retryCount := 0

	for {
		select {
		case <-shutdownCtx.Done():
			logger.Info("Shutdown signal received, stopping alert exporter")
			return nil
		default:
		}

		// Check if shutdown cancellation happened
		if shutdownCtx.Err() != nil {
			logger.WithFields(logrus.Fields{
				"context_error": shutdownCtx.Err(),
				"retry_count":   retryCount,
			}).Info("Context canceled, exiting alert exporter")
			return nil
		}

		cycleLogger := logger.WithFields(logrus.Fields{
			"retry_count": retryCount,
			"backoff":     backoff,
		})

		cycleLogger.Debug("Starting polling cycle")

		err := alertExporter.Poll(shutdownCtx) // This runs its own ticker with s.interval
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
			case <-shutdownCtx.Done():
				logger.Info("Shutdown signal received during backoff, exiting")
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

func (s *Server) startMetricsServer(shutdownCtx context.Context) {
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
		<-shutdownCtx.Done()
		logger.Info("Shutting down metrics server (graceful)")

		// Give metrics server longer grace period (60s to export shutdown metrics)
		shutdownTimeout, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		// Start graceful shutdown
		shutdownDone := make(chan error, 1)
		go func() {
			shutdownDone <- server.Shutdown(shutdownTimeout)
		}()

		err := <-shutdownDone
		if err != nil {
			logger.WithField("error", err).Error("Error during graceful metrics server shutdown")
		} else {
			logger.Info("Metrics server shut down gracefully")
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
