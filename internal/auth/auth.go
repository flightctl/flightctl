package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/auth/authz"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
)

const DisableAuthEnvKey = "FLIGHTCTL_DISABLE_AUTH"

const k8sApiService = "https://kubernetes.default.svc"

// Supported auth types
const (
	AuthTypeNil  = "nil"
	AuthTypeK8s  = "k8s"
	AuthTypeOIDC = "oidc"
	AuthTypeAAP  = "aap"
)

// configuredAuthType stores which auth type is configured
// This is set during InitAuth() and can be used by handlers
var configuredAuthType = "nil"

// GetConfiguredAuthType returns the configured auth type
func GetConfiguredAuthType() string {
	return configuredAuthType
}

// AuthNMiddleware is the interface for authentication middleware
type AuthNMiddleware = common.AuthNMiddleware

// Identity is the interface for user identity
type Identity = common.Identity

// AuthZMiddleware is the interface for authorization middleware
type AuthZMiddleware interface {
	CheckPermission(ctx context.Context, resource string, op string) (bool, error)
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

func initOIDCAuth(cfg *config.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
	oidcUrl := strings.TrimSuffix(cfg.Auth.OIDC.Issuer, "/")
	log.Infof("OIDC auth enabled: %s", oidcUrl)

	providerName := "oidc"
	metadata := api.ObjectMeta{
		Name: &providerName,
	}

	authNProvider, err := authn.NewOIDCAuth(metadata, *cfg.Auth.OIDC, getTlsConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("failed to create OIDC AuthN: %w", err)
	}
	return authNProvider, nil
}

func initAAPAuth(cfg *config.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
	gatewayUrl := strings.TrimSuffix(cfg.Auth.AAP.ApiUrl, "/")
	log.Infof("AAP Gateway auth enabled: %s", gatewayUrl)

	providerName := "aap"
	metadata := api.ObjectMeta{
		Name: &providerName,
	}

	authNProvider, err := authn.NewAapGatewayAuth(metadata, *cfg.Auth.AAP, getTlsConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("failed to create AAP Gateway AuthN: %w", err)
	}
	return authNProvider, nil
}

func initK8sAuth(cfg *config.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
	apiUrl := strings.TrimSuffix(cfg.Auth.K8s.ApiUrl, "/")
	log.Infof("k8s auth enabled: %s", apiUrl)

	var k8sClient k8sclient.K8SClient
	var err error
	if apiUrl == k8sApiService {
		k8sClient, err = k8sclient.NewK8SClient()
	} else {
		k8sClient, err = k8sclient.NewK8SExternalClient(apiUrl, cfg.Auth.InsecureSkipTlsVerify, cfg.Auth.CACert)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s client: %w", err)
	}

	providerName := "k8s"
	metadata := api.ObjectMeta{
		Name: &providerName,
	}

	authNProvider, err := authn.NewK8sAuthN(metadata, *cfg.Auth.K8s, k8sClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s AuthN: %w", err)
	}
	return authNProvider, nil
}

// InitMultiAuth initializes authentication with support for multiple methods
func InitMultiAuth(cfg *config.Config, log logrus.FieldLogger,
	authProviderService authn.AuthProviderService) (common.AuthNMiddleware, error) {
	value, exists := os.LookupEnv(DisableAuthEnvKey)
	if exists && value != "" {
		log.Warnln("Auth disabled")
		configuredAuthType = AuthTypeNil
		// When auth is disabled, return NilAuth instance
		return NilAuth{}, nil
	}

	if cfg.Auth == nil {
		return nil, errors.New("no auth configuration provided")
	}

	// Create TLS config for OIDC provider connections
	tlsConfig := getTlsConfig(cfg)

	// Create MultiAuth instance
	multiAuth := authn.NewMultiAuth(authProviderService, tlsConfig, log)

	// Initialize static authentication methods
	if cfg.Auth.K8s != nil {
		log.Infof("K8s auth enabled: %s", cfg.Auth.K8s.ApiUrl)
		k8sAuthN, err := initK8sAuth(cfg, log)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize K8s auth: %w", err)
		}

		// Add K8s auth with static "k8s" key
		multiAuth.AddStaticProvider("k8s", k8sAuthN)
		configuredAuthType = AuthTypeK8s
	}

	if cfg.Auth.OIDC != nil {
		log.Infof("OIDC auth enabled: %s", cfg.Auth.OIDC.Issuer)
		oidcAuthN, err := initOIDCAuth(cfg, log)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize OIDC auth: %w", err)
		}

		// Add OIDC auth with issuer:clientId key (required for OIDC token validation)
		oidcIssuer := strings.TrimSuffix(cfg.Auth.OIDC.Issuer, "/")
		oidcKey := fmt.Sprintf("%s:%s", oidcIssuer, cfg.Auth.OIDC.ClientId)
		multiAuth.AddStaticProvider(oidcKey, oidcAuthN)
		configuredAuthType = AuthTypeOIDC
	}

	if cfg.Auth.AAP != nil {
		log.Infof("AAP Gateway auth enabled: %s", cfg.Auth.AAP.ApiUrl)
		aapAuthN, err := initAAPAuth(cfg, log)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize AAP auth: %w", err)
		}

		// Add AAP auth (for opaque tokens)
		multiAuth.AddStaticProvider("aap", aapAuthN)
		configuredAuthType = AuthTypeAAP
	}

	if !multiAuth.HasProviders() {
		return nil, errors.New("no authentication providers configured")
	}

	// Note: caller is responsible for starting the background loader with context
	// by calling multiAuth.Start(ctx) in a goroutine

	return multiAuth, nil
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
