// Package httpsource provides an HTTP snapshot source for the catalog collector.
//
// It accepts POST requests containing complete desired-state snapshots as JSON
// and forwards them to the downstream consumer. It is intended for local
// development and integration testing; it binds to loopback by default because
// it has no inbound authentication.
package httpsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"time"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/sirupsen/logrus"
)

const maxBodySize = 16 << 20 // 16 MiB

type server struct {
	cfg  *Config
	id   catalogcollector.ComponentID
	log  *logrus.Entry
	next catalogcollector.Consumer
}

type snapshotRequest struct {
	Revision     string                    `json:"revision"`
	Catalogs     []apiv1alpha1.Catalog     `json:"catalogs"`
	CatalogItems []apiv1alpha1.CatalogItem `json:"catalogItems"`
}

func (s *server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc(s.cfg.Path, s.handleSnapshot)

	srv := &http.Server{
		Handler:           mux,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ln, err := net.Listen("tcp", s.cfg.ListenAddress)
	if err != nil {
		s.log.WithError(err).Error("failed to listen")
		return fmt.Errorf("source %q: listen: %w", s.id, err)
	}

	s.log.WithField(
		"address",
		ln.Addr().String(),
	).Info("source started, accepting snapshots")

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("source %q: serve: %w", s.id, err)
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		s.log.Info("context cancelled, shutting down HTTP server")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if shutdownErr := srv.Shutdown(shutdownCtx); shutdownErr != nil {
			s.log.WithError(shutdownErr).Warn("graceful shutdown failed, forcing close")
			srv.Close()
		}
		<-errCh
		s.log.Info("source stopped")
		return nil

	case err := <-errCh:
		s.log.WithError(err).Error("HTTP server exited with error")
		return err
	}
}

func (s *server) handleSnapshot(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(
			w,
			"Content-Type must be application/json",
			http.StatusUnsupportedMediaType,
		)
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxBodySize)
	data, err := io.ReadAll(body)
	if err != nil {
		if isMaxBytesError(err) {
			http.Error(w, "request body too large", http.StatusBadRequest)
			return
		}
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req snapshotRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		http.Error(
			w,
			"invalid JSON: "+err.Error(),
			http.StatusBadRequest,
		)
		return
	}

	if decoder.More() {
		http.Error(
			w,
			"invalid JSON: trailing data after value",
			http.StatusBadRequest,
		)
		return
	}

	if req.Revision == "" {
		http.Error(
			w,
			`"revision" is required and must be non-empty`,
			http.StatusBadRequest,
		)
		return
	}

	if req.Catalogs == nil {
		http.Error(
			w,
			`missing required field "catalogs"; use [] for an empty collection`,
			http.StatusBadRequest,
		)
		return
	}

	if req.CatalogItems == nil {
		http.Error(
			w,
			`missing required field "catalogItems"; use [] for an empty collection`,
			http.StatusBadRequest,
		)
		return
	}

	snapshot := &catalogcollector.CatalogSnapshot{
		Revision:     req.Revision,
		Catalogs:     req.Catalogs,
		CatalogItems: req.CatalogItems,
	}

	s.log.WithFields(logrus.Fields{
		"revision":           req.Revision,
		"catalog_count":      len(req.Catalogs),
		"catalog_item_count": len(req.CatalogItems),
	}).Info("snapshot received")

	if err := s.next.Consume(r.Context(), snapshot); err != nil {
		s.log.WithError(err).
			WithField("revision", req.Revision).
			Error("downstream processing failed")

		http.Error(
			w,
			"downstream processing failed",
			http.StatusBadGateway,
		)
		return
	}

	s.log.WithField(
		"revision",
		req.Revision,
	).Info("snapshot delivered")

	w.WriteHeader(http.StatusNoContent)
}

func isMaxBytesError(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}
