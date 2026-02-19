package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
)

func getTlsConfigFromConfig(cfg *config.Config) *tls.Config {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: cfg.Auth.InsecureSkipTlsVerify, //nolint:gosec
	}
	if cfg.Auth.CACert != "" {
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM([]byte(cfg.Auth.CACert))
		tlsConfig.RootCAs = caCertPool
	}
	return tlsConfig
}

func WaitForOIDCProvider(ctx context.Context, log logrus.FieldLogger, cfg *config.Config) error {
	if cfg == nil || cfg.Auth == nil || cfg.Auth.OIDC == nil {
		return nil // OIDC not configured
	}

	log.Infof("Waiting for OIDC provider at %s to be ready...", cfg.Auth.OIDC.Issuer)

	discoveryURL := fmt.Sprintf("%s/.well-known/openid-configuration", cfg.Auth.OIDC.Issuer)

	tlsConfig := getTlsConfigFromConfig(cfg)
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
		Timeout: 5 * time.Second,
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	timeoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	for {
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("timed out waiting for OIDC provider")
		case <-ticker.C:
			req, err := http.NewRequestWithContext(timeoutCtx, http.MethodGet, discoveryURL, nil)
			if err != nil {
				// Log the error and continue, maybe it's a temporary issue.
				log.Debugf("failed to create OIDC discovery request: %v", err)
				continue
			}

			resp, err := client.Do(req)
			if err != nil {
				log.Infof("Waiting for OIDC provider: %v", err)
				continue
			}

			if resp.StatusCode == http.StatusOK {
				resp.Body.Close()
				log.Info("OIDC provider is ready.")
				return nil
			}
			log.Infof("Waiting for OIDC provider: received status code %d", resp.StatusCode)
			resp.Body.Close()
		}
	}
}
