package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/auth/authz"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
)

const (
	// DisableAuthEnvKey is the environment variable key used to disable auth when developing.
	DisableAuthEnvKey = "FLIGHTCTL_DISABLE_AUTH"
	k8sCACertPath     = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	k8sApiService     = "https://kubernetes.default.svc"
)

type AuthZMiddleware interface {
	CheckPermission(ctx context.Context, resource string, op string) (bool, error)
}

type AuthType string

const (
	AuthTypeNil  AuthType = "nil"
	AuthTypeK8s  AuthType = "k8s"
	AuthTypeOIDC AuthType = "oidc"
	AuthTypeAAP  AuthType = "aap"
)

func GetConfiguredAuthType() AuthType {
	return configuredAuthType
}

var configuredAuthType AuthType

func initK8sAuth(cfg *config.Config, log logrus.FieldLogger) (common.AuthNMiddleware, AuthZMiddleware, error) {
	apiUrl := strings.TrimSuffix(cfg.Auth.K8s.ApiUrl, "/")
	externalOpenShiftApiUrl := strings.TrimSuffix(cfg.Auth.K8s.ExternalOpenShiftApiUrl, "/")
	log.Infof("k8s auth enabled: %s", apiUrl)
	var k8sClient k8sclient.K8SClient
	var err error
	if apiUrl == k8sApiService {
		k8sClient, err = k8sclient.NewK8SClient()
	} else {
		k8sClient, err = k8sclient.NewK8SExternalClient(apiUrl, cfg.Auth.InsecureSkipTlsVerify, cfg.Auth.CACert)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create k8s client: %w", err)
	}
	authZProvider := K8sToK8sAuth{K8sAuthZ: authz.K8sAuthZ{K8sClient: k8sClient, Namespace: cfg.Auth.K8s.RBACNs}}
	authNProvider, err := authn.NewK8sAuthN(k8sClient, externalOpenShiftApiUrl, cfg.Auth.K8s.RBACNs)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create k8s AuthN: %w", err)
	}
	return authNProvider, authZProvider, nil
}

func getTlsConfig(cfg *config.Config) *tls.Config {
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

func getOrgConfig(cfg *config.Config) *common.AuthOrganizationsConfig {
	if cfg.Organizations == nil {
		return &common.AuthOrganizationsConfig{
			Enabled: false,
		}
	}
	return &common.AuthOrganizationsConfig{
		Enabled: cfg.Organizations.Enabled,
	}
}

func initOIDCAuth(cfg *config.Config, log logrus.FieldLogger) (common.AuthNMiddleware, AuthZMiddleware, error) {
	oidcUrl := strings.TrimSuffix(cfg.Auth.OIDC.Issuer, "/")
	log.Infof("OIDC auth enabled: %s", oidcUrl)
	authZProvider := authz.NewStaticAuthZ()
	usernameClaim := "preferred_username"
	if cfg.Auth.OIDC.UsernameClaim != nil {
		usernameClaim = *cfg.Auth.OIDC.UsernameClaim
	}
	roleClaim := "groups"
	if cfg.Auth.OIDC.RoleClaim != nil {
		roleClaim = *cfg.Auth.OIDC.RoleClaim
	}
	authNProvider, err := authn.NewOIDCAuth("oidc", oidcUrl, getTlsConfig(cfg), getOrgConfig(cfg), usernameClaim, roleClaim, "", []string{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create OIDC AuthN: %w", err)
	}
	return authNProvider, authZProvider, nil
}

func initAAPAuth(cfg *config.Config, log logrus.FieldLogger) (common.AuthNMiddleware, AuthZMiddleware, error) {
	gatewayUrl := strings.TrimSuffix(cfg.Auth.AAP.ApiUrl, "/")
	gatewayExternalUrl := strings.TrimSuffix(cfg.Auth.AAP.ExternalApiUrl, "/")
	log.Infof("AAP Gateway auth enabled: %s", gatewayUrl)
	authZProvider := authz.NewStaticAuthZ()
	authNProvider, err := authn.NewAapGatewayAuth(gatewayUrl, gatewayExternalUrl, getTlsConfig(cfg))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create AAP Gateway AuthN: %w", err)
	}
	return authNProvider, authZProvider, nil
}

// InitMultiAuth initializes authentication with support for multiple methods
func InitMultiAuth(cfg *config.Config, log logrus.FieldLogger, authProviderService authn.AuthProviderService) (common.AuthNMiddleware, AuthZMiddleware, error) {
	value, exists := os.LookupEnv(DisableAuthEnvKey)
	if exists && value != "" {
		log.Warnln("Auth disabled")
		configuredAuthType = AuthTypeNil
		authNProvider := NilAuth{}
		authZProvider := NilAuth{}
		return authNProvider, authZProvider, nil
	}

	if cfg.Auth == nil {
		return nil, nil, errors.New("no auth configuration provided")
	}

	// Create TLS config for OIDC provider connections
	tlsConfig := &tls.Config{
		InsecureSkipVerify: cfg.Auth.InsecureSkipTlsVerify, //nolint:gosec // Configurable TLS verification for testing/dev environments
	}

	// Create MultiAuth instance
	multiAuth := authn.NewMultiAuth(authProviderService, tlsConfig, log)
	var authZProvider AuthZMiddleware

	// Initialize static authentication methods
	if cfg.Auth.K8s != nil {
		log.Infof("K8s auth enabled: %s", cfg.Auth.K8s.ApiUrl)
		k8sAuthN, k8sAuthZ, err := initK8sAuth(cfg, log)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to initialize K8s auth: %w", err)
		}

		// Register K8s auth under normalized issuer aliases (with/without .cluster.local)
		addAliases := func(raw string) {
			raw = strings.TrimSuffix(raw, "/")
			if raw == "" {
				return
			}
			// exact as provided
			multiAuth.AddStaticProvider(raw, k8sAuthN)
			// host alias with/without .cluster.local
			if u, err := url.Parse(raw); err == nil {
				host := u.Host
				var aliasHost string
				if strings.HasSuffix(host, ".cluster.local") {
					aliasHost = strings.TrimSuffix(host, ".cluster.local")
				} else {
					aliasHost = host + ".cluster.local"
				}
				if aliasHost != host {
					u.Host = aliasHost
					multiAuth.AddStaticProvider(strings.TrimSuffix(u.String(), "/"), k8sAuthN)
				}
			}
		}

		primary := strings.TrimSuffix(cfg.Auth.K8s.ApiUrl, "/")
		if primary == "" {
			primary = k8sApiService
		}
		addAliases(primary)
		if cfg.Auth.K8s.ExternalOpenShiftApiUrl != "" {
			addAliases(strings.TrimSuffix(cfg.Auth.K8s.ExternalOpenShiftApiUrl, "/"))
		}

		// Use K8s authZ as primary (can be overridden by other methods)
		if authZProvider == nil {
			authZProvider = k8sAuthZ
		}
		configuredAuthType = AuthTypeK8s
	}

	if cfg.Auth.OIDC != nil {
		log.Infof("OIDC auth enabled: %s", cfg.Auth.OIDC.Issuer)
		oidcAuthN, oidcAuthZ, err := initOIDCAuth(cfg, log)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to initialize OIDC auth: %w", err)
		}

		// Add OIDC auth with its issuer
		oidcIssuer := strings.TrimSuffix(cfg.Auth.OIDC.Issuer, "/")
		multiAuth.AddStaticProvider(oidcIssuer, oidcAuthN)

		// Use OIDC authZ as primary (can be overridden by other methods)
		if authZProvider == nil {
			authZProvider = oidcAuthZ
		}
		configuredAuthType = AuthTypeOIDC
	}

	if cfg.Auth.AAP != nil {
		log.Infof("AAP Gateway auth enabled: %s", cfg.Auth.AAP.ApiUrl)
		aapAuthN, aapAuthZ, err := initAAPAuth(cfg, log)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to initialize AAP auth: %w", err)
		}

		// Add AAP auth (for opaque tokens)
		multiAuth.AddStaticProvider("aap", aapAuthN)

		// Use AAP authZ as primary (can be overridden by other methods)
		if authZProvider == nil {
			authZProvider = aapAuthZ
		}
		configuredAuthType = AuthTypeAAP
	}

	if !multiAuth.HasProviders() {
		return nil, nil, errors.New("no authentication providers configured")
	}

	if authZProvider == nil {
		return nil, nil, errors.New("no authZ provider defined")
	}

	// Start the cache background cleanup
	multiAuth.Start()

	return multiAuth, authZProvider, nil
}

type K8sToK8sAuth struct {
	authz.K8sAuthZ
}

func (o K8sToK8sAuth) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	k8sTokenVal := ctx.Value(consts.TokenCtxKey)
	if k8sTokenVal == nil {
		return false, nil
	}
	k8sToken := k8sTokenVal.(string)
	return o.K8sAuthZ.CheckPermission(ctx, k8sToken, resource, op)
}
