package deviceauth

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/google/uuid"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

type ContextKey string

const (
	DeviceCNKey    ContextKey = "device_cn"
	DeviceIDKey    ContextKey = "device_id"
	DeviceOrgIDKey ContextKey = "device_org_id"
)

type CertInfo struct {
	CommonName string
	DeviceID   string
	OrgID      uuid.UUID
}

func (c CertInfo) String() string {
	return fmt.Sprintf("CN=%s, DeviceID=%s, OrgID=%s",
		c.CommonName, c.DeviceID, c.OrgID.String())
}

type deviceAuthConfig struct {
	AppCfg *config.Config `mapstructure:"-"`
}

type deviceAuth struct {
	logger *zap.Logger
	cfg    *deviceAuthConfig
}

func newDeviceAuth(_ context.Context, set extension.Settings, cfg *deviceAuthConfig) *deviceAuth {
	return &deviceAuth{
		cfg:    cfg,
		logger: set.Logger,
	}
}

func (d *deviceAuth) Start(ctx context.Context, host component.Host) error {
	expectedSigner := ""
	if d.cfg != nil && d.cfg.AppCfg != nil {
		expectedSigner = d.cfg.AppCfg.CA.DeviceSvcClientSignerName
	}
	d.logger.Info("device authenticator started",
		zap.String("expected_signer", expectedSigner))
	return nil
}

func (d *deviceAuth) Shutdown(ctx context.Context) error {
	d.logger.Info("device authenticator stopped")
	return nil
}

// Authenticate hooks gRPC requests.
func (d *deviceAuth) Authenticate(ctx context.Context, headers map[string][]string) (context.Context, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		d.logger.Debug("no peer info in context (skipping auth)")
		return ctx, nil
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		d.logger.Debug("peer not using TLS (skipping auth)")
		return ctx, nil
	}

	certInfo, err := d.authenticate(tlsInfo.State)
	if err != nil {
		d.logger.Warn("device authentication failed (gRPC)",
			zap.String("reason", err.Error()))
		return ctx, fmt.Errorf("failed to authenticate device: %w", err)
	}

	d.logger.Debug("device authenticated (gRPC)",
		zap.String("cn", certInfo.CommonName),
		zap.String("device_id", certInfo.DeviceID),
		zap.String("org_id", certInfo.OrgID.String()))

	ctx = context.WithValue(ctx, DeviceCNKey, certInfo.CommonName)
	ctx = context.WithValue(ctx, DeviceIDKey, certInfo.DeviceID)
	ctx = context.WithValue(ctx, DeviceOrgIDKey, certInfo.OrgID)
	return ctx, nil
}

// AuthenticateHTTP hooks HTTP requests.
func (d *deviceAuth) AuthenticateHTTP(req *http.Request) (*http.Request, error) {
	ctx := req.Context()

	if req.TLS == nil {
		d.logger.Debug("no TLS on HTTP request (skipping auth)")
		return req, nil
	}

	certInfo, err := d.authenticate(*req.TLS)
	if err != nil {
		d.logger.Warn("device authentication failed (HTTP)",
			zap.String("reason", err.Error()))
		return nil, err
	}

	d.logger.Debug("device authenticated (HTTP)",
		zap.String("cn", certInfo.CommonName),
		zap.String("device_id", certInfo.DeviceID),
		zap.String("org_id", certInfo.OrgID.String()))

	ctx = context.WithValue(ctx, DeviceCNKey, certInfo.CommonName)
	ctx = context.WithValue(ctx, DeviceIDKey, certInfo.DeviceID)
	ctx = context.WithValue(ctx, DeviceOrgIDKey, certInfo.OrgID)
	return req.WithContext(ctx), nil
}

// authenticate inspects the peer certificates and validates the device.
// It prefers Warn for expected mismatches and Error for unexpected parse errors.
func (d *deviceAuth) authenticate(state tls.ConnectionState) (CertInfo, error) {
	if len(state.PeerCertificates) == 0 {
		d.logger.Debug("no peer certificates presented")
		return CertInfo{}, fmt.Errorf("no peer certificates presented")
	}

	expectedSigner := ""
	if d.cfg != nil && d.cfg.AppCfg != nil {
		expectedSigner = d.cfg.AppCfg.CA.DeviceSvcClientSignerName
	}

	for i, cert := range state.PeerCertificates {
		cn := cert.Subject.CommonName

		signerName, err := signer.GetSignerNameExtension(cert)
		if err != nil {
			d.logger.Debug("skipping cert: missing/invalid signer extension",
				zap.Int("index", i),
				zap.String("cn", cn),
				zap.String("error", err.Error()))
			continue
		}

		if signerName != expectedSigner {
			d.logger.Debug("skipping cert: signer mismatch",
				zap.Int("index", i),
				zap.String("cn", cn),
				zap.String("found_signer", signerName),
				zap.String("expected_signer", expectedSigner))
			continue
		}

		deviceFingerprint, err := signer.GetDeviceFingerprintExtension(cert)
		if err != nil {
			d.logger.Error("failed to extract device fingerprint from extension",
				zap.Int("index", i),
				zap.String("cn", cn),
				zap.String("error", err.Error()))
			return CertInfo{}, fmt.Errorf("device fingerprint from extension: %w", err)
		}

		lastHyphen := strings.LastIndex(cert.Subject.CommonName, "-")
		if lastHyphen == -1 {
			d.logger.Error("failed to extract device fingerprint from extension",
				zap.Int("index", i),
				zap.String("cn", cn))
			return CertInfo{}, fmt.Errorf("invalid CN format: no hyphen found")
		}
		deviceID := cert.Subject.CommonName[lastHyphen+1:]

		if deviceID != deviceFingerprint {
			d.logger.Warn("device ID mismatch",
				zap.Int("index", i),
				zap.String("cn", cn),
				zap.String("device_id_cn", deviceID),
				zap.String("device_id_ext", deviceFingerprint))
			return CertInfo{}, fmt.Errorf("device ID mismatch: CN-derived=%s, extension=%s", deviceID, deviceFingerprint)
		}

		orgID, hasOrg, err := signer.GetOrgIDExtensionFromCert(cert)
		if err != nil {
			d.logger.Error("failed to extract org ID from extension",
				zap.Int("index", i),
				zap.String("cn", cn),
				zap.String("error", err.Error()))
			return CertInfo{}, fmt.Errorf("device orgid from extension: %w", err)
		}
		if !hasOrg {
			d.logger.Warn("missing org ID extension",
				zap.Int("index", i),
				zap.String("cn", cn))
			return CertInfo{}, fmt.Errorf("missing org ID extension")
		}

		return CertInfo{
			CommonName: cn,
			DeviceID:   deviceID,
			OrgID:      orgID,
		}, nil
	}

	d.logger.Warn("no certificate matched expected signer",
		zap.String("expected_signer", expectedSigner))
	return CertInfo{}, fmt.Errorf("no certificate found with expected signer %q", expectedSigner)
}
