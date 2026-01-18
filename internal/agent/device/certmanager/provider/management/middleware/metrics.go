package middleware

import (
	"context"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"time"

	"github.com/flightctl/flightctl/pkg/certmanager"
	"k8s.io/client-go/util/cert"
)

const metaKeyStartUnixNano = "flightctl.mgmt_cert.renewal_start_unix_nano"

var ErrProvisionNotReady = errors.New("certificate not ready")

type ManagementCertEventKind string

const (
	// Emitted when we load the currently-installed certificate (e.g., agent restart).
	ManagementCertEventKindCurrent ManagementCertEventKind = "current"
	// Emitted for a renewal flow (provision/store).
	ManagementCertEventKindRenewal ManagementCertEventKind = "renewal"
)

// ManagementCertMetricsCallback observes management certificate events.
//
// Semantics:
//   - kind=current: best-effort observation of the currently installed cert (no renewal attempt).
//   - kind=renewal: one renewal attempt observation (success/failure) + duration.
//   - cert may be nil (e.g., failures or non-ready).
type ManagementCertMetricsCallback func(kind ManagementCertEventKind, cert *x509.Certificate, durationSeconds float64, err error)

func WithMetricsProvisioner(cb ManagementCertMetricsCallback, f certmanager.ProvisionerFactory) certmanager.ProvisionerFactory {
	if cb == nil || f == nil {
		return f
	}

	return &chainProvisionerFactory{
		next: f,
		provision: func(ctx context.Context, next certmanager.ProvisionerProvider, req certmanager.ProvisionRequest) (*certmanager.ProvisionResult, error) {
			start := time.Now()

			res, err := next.Provision(ctx, req)
			if err != nil {
				cb(ManagementCertEventKindRenewal, nil, time.Since(start).Seconds(), err)
				return res, err
			}

			if res == nil || !res.Ready {
				// Not ready is not a "hard" error for control flow, but for metrics it is a failed attempt.
				cb(ManagementCertEventKindRenewal, nil, time.Since(start).Seconds(), ErrProvisionNotReady)
				return res, nil
			}

			if res.Meta == nil {
				res.Meta = make(map[string][]byte, 1)
			}

			buf := make([]byte, 8)
			binary.BigEndian.PutUint64(buf, uint64(start.UnixNano())) //nolint:gosec // G115: UnixNano always positive
			res.Meta[metaKeyStartUnixNano] = buf

			return res, nil
		},
	}
}

func WithMetricsStorage(cb ManagementCertMetricsCallback, f certmanager.StorageFactory) certmanager.StorageFactory {
	if cb == nil || f == nil {
		return f
	}

	return &chainStorageFactory{
		next: f,
		loadCert: func(ctx context.Context, next certmanager.StorageProvider) (*x509.Certificate, error) {
			start := time.Now()
			c, err := next.LoadCertificate(ctx)
			cb(ManagementCertEventKindCurrent, c, time.Since(start).Seconds(), err)
			return c, err
		},
		store: func(ctx context.Context, next certmanager.StorageProvider, req certmanager.StoreRequest) error {
			start := time.Now()
			if req.Result != nil && req.Result.Meta != nil {
				if raw, ok := req.Result.Meta[metaKeyStartUnixNano]; ok && len(raw) == 8 {
					ns := int64(binary.BigEndian.Uint64(raw)) //nolint:gosec // G115: UnixNano always positive
					start = time.Unix(0, ns)
				}
			}

			err := next.Store(ctx, req)
			if err != nil {
				cb(ManagementCertEventKindRenewal, nil, time.Since(start).Seconds(), err)
				return err
			}

			var parsed *x509.Certificate
			if req.Result != nil && len(req.Result.Cert) > 0 {
				certs, parseErr := cert.ParseCertsPEM(req.Result.Cert)
				if parseErr == nil && len(certs) > 0 {
					parsed = certs[0]
				}
			}

			cb(ManagementCertEventKindRenewal, parsed, time.Since(start).Seconds(), nil)
			return nil
		},
	}
}
