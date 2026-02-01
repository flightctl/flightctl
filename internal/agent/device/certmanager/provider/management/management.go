package management

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/identity"
	"github.com/flightctl/flightctl/pkg/certmanager"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/google/uuid"
	"k8s.io/client-go/util/cert"
)

const (
	configProviderName = "device-management-config"
	managementCertName = "device-management"

	provisionerType = "management"

	backoffInitial      = 10 * time.Minute
	backoffFactor       = 2.0
	backoffMax          = 30 * time.Minute
	backoffJitterFactor = 0.1

	managementSignerName = "flightctl.io/device-management-renewal"
)

// ---- ConfigProvider ----

type managementConfigProvider struct {
	renewBeforeExpiryPercentage int32
}

func NewManagementConfigProvider(renewBeforeExpiryPercentage int32) *managementConfigProvider {
	return &managementConfigProvider{renewBeforeExpiryPercentage: renewBeforeExpiryPercentage}
}

func (p *managementConfigProvider) Name() string { return configProviderName }

func (p *managementConfigProvider) GetCertificateConfigs() ([]certmanager.CertificateConfig, error) {
	cfg := certmanager.CertificateConfig{
		Name: managementCertName,
		Provisioner: certmanager.ProvisionerConfig{
			Type: provisionerType,
		},
		Storage: certmanager.StorageConfig{
			Type: provisionerType,
		},
	}

	// Apply env overrides (can set both, precedence handled by certmanager)
	renewBefore, renewBeforePercent := renewPolicyFromEnv()
	cfg.RenewBefore = renewBefore
	cfg.RenewBeforePercentage = renewBeforePercent

	// Fall back to provider default only if neither override is set
	if cfg.RenewBefore == nil && cfg.RenewBeforePercentage == nil {
		cfg.RenewBeforePercentage = &p.renewBeforeExpiryPercentage
	}

	return []certmanager.CertificateConfig{cfg}, nil
}

// ---- ProvisionerProvider ----

type managementProvisioner struct {
	log              certmanager.Logger
	identityProvider identity.Provider
	managementClient client.Management
	deviceName       string

	backoffCfg poll.Config

	csrName  string
	lastInfo time.Time
}

func (p *managementProvisioner) Provision(ctx context.Context, req certmanager.ProvisionRequest) (*certmanager.ProvisionResult, error) {
	if p.managementClient == nil {
		return nil, fmt.Errorf("management provisioner: managementClient is nil")
	}
	if p.identityProvider == nil {
		return nil, fmt.Errorf("management provisioner: identityProvider is nil")
	}
	if p.deviceName == "" {
		return nil, fmt.Errorf("management provisioner: deviceName is empty")
	}

	// First call for this issuance: create CSR on the server.
	if p.csrName == "" {
		curr, err := getCurrentCertificate(p.identityProvider)
		if err != nil {
			return nil, err
		}

		if curr != nil {
			expiresIn := time.Until(curr.NotAfter)
			p.log.Infof(
				"Attempting to renew management certificate (expires in %s)",
				formatExpiry(expiresIn),
			)
		} else {
			p.log.Infof("Attempting to issue management certificate (no current certificate found)")
		}

		csrName := fmt.Sprintf("%s-%s", p.deviceName, uuid.NewString()[:8])

		csrPEM, err := p.identityProvider.GenerateCSR(p.deviceName)
		if err != nil {
			return nil, fmt.Errorf("generate CSR: %w", err)
		}

		usages := []string{"clientAuth", "CA:false"}
		csrReq := api.CertificateSigningRequest{
			ApiVersion: api.CertificateSigningRequestAPIVersion,
			Kind:       api.CertificateSigningRequestKind,
			Metadata: api.ObjectMeta{
				Name: &csrName,
			},
			Spec: api.CertificateSigningRequestSpec{
				Request:    csrPEM,
				SignerName: managementSignerName,
				Usages:     &usages,
			},
		}

		_, statusCode, err := p.managementClient.CreateCertificateSigningRequest(ctx, csrReq)
		if err != nil {
			if errors.IsRetryable(err) {
				p.log.Infof(
					"Transient error while creating management certificate signing request %q: %v",
					csrName,
					err,
				)
				return p.requeueWithBackoff(req.Attempt, "create csr transient error", err)
			}
			return nil, err
		}

		if statusCode == http.StatusServiceUnavailable {
			p.log.Infof(
				"Management API unavailable while creating certificate signing request %q (HTTP 503)",
				csrName,
			)
			return p.requeueWithBackoff(req.Attempt, "create csr got 503", nil)
		}
		if statusCode != http.StatusOK && statusCode != http.StatusCreated {
			return nil, fmt.Errorf("create CSR %q: unexpected status code %d", csrName, statusCode)
		}

		p.csrName = csrName
		p.log.Debugf("Created management certificate CSR %q", p.csrName)

		return p.poll(ctx, req)
	}

	// Subsequent calls: poll CSR.
	return p.poll(ctx, req)
}

func (p *managementProvisioner) poll(ctx context.Context, req certmanager.ProvisionRequest) (*certmanager.ProvisionResult, error) {
	csrObj, statusCode, err := p.managementClient.GetCertificateSigningRequest(ctx, p.csrName)
	if err != nil {
		if errors.IsRetryable(err) {
			p.log.Infof(
				"Transient error while polling management certificate signing request %q: %v",
				p.csrName,
				err,
			)
			return p.requeueWithBackoff(req.Attempt, "get csr transient error", err)
		}
		return nil, err
	}

	if statusCode == http.StatusServiceUnavailable {
		p.log.Infof(
			"Management API unavailable while polling certificate signing request %q (HTTP 503)",
			p.csrName,
		)
		return p.requeueWithBackoff(req.Attempt, "get csr got 503", nil)
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("get CSR %q: unexpected status code %d", p.csrName, statusCode)
	}

	if csrObj == nil || csrObj.Status == nil {
		p.log.Debugf("CSR %q: still pending (no status yet)", p.csrName)
		return p.requeueWithBackoff(req.Attempt, "csr pending", nil)
	}

	// Approved
	if api.IsStatusConditionTrue(
		csrObj.Status.Conditions,
		api.ConditionTypeCertificateSigningRequestApproved,
	) {
		if csrObj.Status.Certificate == nil || len(*csrObj.Status.Certificate) == 0 {
			p.log.Infof(
				"Management certificate signing request %q approved; waiting for certificate issuance",
				p.csrName,
			)
			return p.requeueWithBackoff(req.Attempt, "csr approved waiting for cert", nil)
		}

		p.log.Debugf("CSR %q: approved and certificate issued", p.csrName)

		p.csrName = ""
		p.lastInfo = time.Time{}

		return &certmanager.ProvisionResult{
			Ready: true,
			Cert:  *csrObj.Status.Certificate,
		}, nil
	}

	// Denied
	if api.IsStatusConditionTrue(
		csrObj.Status.Conditions,
		api.ConditionTypeCertificateSigningRequestDenied,
	) {
		cond := api.FindStatusCondition(
			csrObj.Status.Conditions,
			api.ConditionTypeCertificateSigningRequestDenied,
		)
		msg := ""
		if cond != nil {
			msg = cond.Message
		}
		return nil, fmt.Errorf("csr %q denied: %s", p.csrName, msg)
	}

	// Failed
	if api.IsStatusConditionTrue(
		csrObj.Status.Conditions,
		api.ConditionTypeCertificateSigningRequestFailed,
	) {
		cond := api.FindStatusCondition(
			csrObj.Status.Conditions,
			api.ConditionTypeCertificateSigningRequestFailed,
		)
		msg := ""
		if cond != nil {
			msg = cond.Message
		}
		return nil, fmt.Errorf("csr %q failed: %s", p.csrName, msg)
	}

	// Default: pending approval
	p.log.Infof("Management certificate signing request %q is pending approval", p.csrName)
	return p.requeueWithBackoff(req.Attempt, "csr pending approval", nil)
}

func (p *managementProvisioner) requeueWithBackoff(attempt int, reason string, err error) (*certmanager.ProvisionResult, error) {
	if attempt < 1 {
		attempt = 1
	}

	delay := poll.CalculateBackoffDelay(&p.backoffCfg, attempt)
	now := time.Now()

	// First user-facing INFO
	if p.lastInfo.IsZero() {
		p.lastInfo = now
		p.log.Infof("Management certificate renewal pending; next retry in %s", formatExpiry(delay))
	} else if now.Sub(p.lastInfo) >= p.backoffCfg.MaxDelay {
		// Periodic INFO during long outages
		p.lastInfo = now
		p.log.Infof("Management certificate renewal still pending; next retry in %s", formatExpiry(delay))
	}

	if err != nil {
		p.log.Debugf(
			"Renewal retry (%s, attempt=%d, requeue=%s): %v",
			reason,
			attempt,
			delay,
			err,
		)
	} else {
		p.log.Debugf(
			"Renewal retry (%s, attempt=%d, requeue=%s)",
			reason,
			attempt,
			delay,
		)
	}

	return &certmanager.ProvisionResult{
		Ready:        false,
		RequeueAfter: delay,
	}, nil
}

// ---- StorageProvider ----

type managementStorage struct {
	log              certmanager.Logger
	identityProvider identity.Provider
	managementClient client.Management
}

func (s *managementStorage) Store(ctx context.Context, req certmanager.StoreRequest) error {
	if s.identityProvider == nil {
		return fmt.Errorf("management storage: identityProvider is nil")
	}
	if len(req.Result.Cert) == 0 {
		return fmt.Errorf("management storage: empty certificate PEM")
	}

	if err := s.identityProvider.StoreCertificate(req.Result.Cert); err != nil {
		return err
	}

	// Reload is best-effort: if we can reload, do it; if it fails, don't fail the store.
	if ok, err := client.TryReload(s.managementClient); ok && err != nil {
		s.log.Debugf("Failed to reload management client after cert rotation: %v", err)
	}

	// Best-effort: parse the cert just for logging.
	if s.log != nil {
		if certs, err := cert.ParseCertsPEM(req.Result.Cert); err == nil && len(certs) > 0 {
			c := certs[0]
			expiresIn := time.Until(c.NotAfter)
			s.log.Infof(
				"Installed new management certificate (expires in %s, notAfter=%s)",
				formatExpiry(expiresIn),
				c.NotAfter.UTC().Format(time.RFC3339),
			)
		} else {
			s.log.Infof("Installed new management certificate")
		}
	}

	return nil
}

func (s *managementStorage) LoadCertificate(ctx context.Context) (*x509.Certificate, error) {
	return getCurrentCertificate(s.identityProvider)
}

// ---- Factories ----

type managementProvisionerFactory struct {
	deviceName       string
	identityProvider identity.Provider
	managementClient client.Management
}

func NewManagementProvisionerFactory(deviceName string, identityProvider identity.Provider, managementClient client.Management) certmanager.ProvisionerFactory {
	return &managementProvisionerFactory{
		deviceName:       deviceName,
		identityProvider: identityProvider,
		managementClient: managementClient,
	}
}

func (f managementProvisionerFactory) Type() string { return provisionerType }

func (f managementProvisionerFactory) Validate(log certmanager.Logger, cc certmanager.CertificateConfig) error {
	return nil
}

func (f managementProvisionerFactory) New(log certmanager.Logger, cc certmanager.CertificateConfig) (certmanager.ProvisionerProvider, error) {
	return &managementProvisioner{
		log:              log,
		deviceName:       f.deviceName,
		identityProvider: f.identityProvider,
		managementClient: f.managementClient,
		backoffCfg: poll.Config{
			BaseDelay:    backoffInitial,
			Factor:       backoffFactor,
			MaxDelay:     backoffMax,
			JitterFactor: backoffJitterFactor,
		},
	}, nil
}

type managementStorageFactory struct {
	identityProvider identity.Provider
	managementClient client.Management
}

func NewManagementStorageFactory(identityProvider identity.Provider, managementClient client.Management) certmanager.StorageFactory {
	return &managementStorageFactory{
		identityProvider: identityProvider,
		managementClient: managementClient,
	}
}

func (f managementStorageFactory) Type() string { return provisionerType }

func (f managementStorageFactory) Validate(log certmanager.Logger, cc certmanager.CertificateConfig) error {
	return nil
}

func (f managementStorageFactory) New(log certmanager.Logger, cc certmanager.CertificateConfig) (certmanager.StorageProvider, error) {
	return &managementStorage{
		log:              log,
		identityProvider: f.identityProvider,
		managementClient: f.managementClient,
	}, nil
}

// ---- Helpers ----

func formatExpiry(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60

	switch {
	case days > 0:
		if hours > 0 {
			return fmt.Sprintf("%dd %dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	case hours > 0:
		if mins > 0 {
			return fmt.Sprintf("%dh %dm", hours, mins)
		}
		return fmt.Sprintf("%dh", hours)
	default:
		if mins == 0 {
			return "less than a minute"
		}
		return fmt.Sprintf("%dm", mins)
	}
}

func getCurrentCertificate(identityProvider identity.Provider) (*x509.Certificate, error) {
	if identityProvider == nil {
		return nil, fmt.Errorf("getCurrentCertificate: identityProvider is nil")
	}

	pemBytes, err := identityProvider.GetCertificate()
	if err != nil {
		return nil, err
	}
	if len(pemBytes) == 0 {
		return nil, nil
	}

	certs, err := cert.ParseCertsPEM(pemBytes)
	if err != nil {
		return nil, err
	}
	if len(certs) == 0 {
		return nil, nil
	}
	return certs[0], nil
}

func renewPolicyFromEnv() (*time.Duration, *int32) {
	var (
		renewBefore        *time.Duration
		renewBeforePercent *int32
	)

	if v := os.Getenv("FLIGHTCTL_TEST_MGMT_CERT_RENEW_BEFORE_SECONDS"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil && sec > 0 {
			d := time.Duration(sec) * time.Second
			renewBefore = &d
		}
	}

	if v := os.Getenv("FLIGHTCTL_TEST_MGMT_CERT_RENEW_BEFORE_PERCENT"); v != "" {
		if p, err := strconv.ParseInt(v, 10, 32); err == nil && p > 0 && p < 100 {
			pp := int32(p)
			renewBeforePercent = &pp
		}
	}

	return renewBefore, renewBeforePercent
}
