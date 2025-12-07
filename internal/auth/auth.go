package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/auth/authz"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
)

const k8sApiService = "https://kubernetes.default.svc"

// Supported auth types
const (
	AuthTypeK8s       = "k8s"
	AuthTypeOIDC      = "oidc"
	AuthTypeAAP       = "aap"
	AuthTypeOpenShift = "openshift"
	AuthTypeOauth2    = "oauth2"
)

// configuredAuthType stores which auth type is configured
// This is set during InitAuth() and can be used by handlers
var configuredAuthType = ""

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
	GetUserPermissions(ctx context.Context) (*api.PermissionList, error)
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

func initOAuth2Auth(cfg *config.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
	providerName := "oauth2"
	metadata := api.ObjectMeta{
		Name: &providerName,
		Annotations: &map[string]string{
			api.AuthProviderAnnotationCreatedBySuperAdmin: "true",
		},
	}

	authNProvider, err := authn.NewOAuth2Auth(metadata, *cfg.Auth.OAuth2, getTlsConfig(cfg), log)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth2 AuthN: %w", err)
	}
	return authNProvider, nil
}
func initOIDCAuth(cfg *config.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
	oidcUrl := strings.TrimSuffix(cfg.Auth.OIDC.Issuer, "/")
	log.Infof("OIDC auth enabled: %s", oidcUrl)

	providerName := "oidc"
	metadata := api.ObjectMeta{
		Name: &providerName,
		Annotations: &map[string]string{
			api.AuthProviderAnnotationCreatedBySuperAdmin: "true",
		},
	}

	authNProvider, err := authn.NewOIDCAuth(metadata, *cfg.Auth.OIDC, getTlsConfig(cfg), log)
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

func initOpenShiftAuth(cfg *config.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
	if cfg.Auth.OpenShift == nil {
		return nil, errors.New("OpenShift auth configuration is nil")
	}
	if cfg.Auth.OpenShift.ClusterControlPlaneUrl == nil {
		return nil, errors.New("OpenShift ClusterControlPlaneUrl is required but not configured")
	}
	if *cfg.Auth.OpenShift.ClusterControlPlaneUrl == "" {
		return nil, errors.New("OpenShift ClusterControlPlaneUrl cannot be empty")
	}

	apiUrl := strings.TrimSuffix(*cfg.Auth.OpenShift.ClusterControlPlaneUrl, "/")
	log.Infof("OpenShift auth enabled: %s", apiUrl)

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

	providerName := "openshift"
	metadata := api.ObjectMeta{
		Name: &providerName,
	}

	authNProvider, err := authn.NewOpenShiftAuth(metadata, *cfg.Auth.OpenShift, k8sClient, getTlsConfig(cfg), log)
	if err != nil {
		return nil, fmt.Errorf("failed to create OpenShift AuthN: %w", err)
	}
	return authNProvider, nil
}

// InitMultiAuth initializes authentication with support for multiple methods
func InitMultiAuth(cfg *config.Config, log logrus.FieldLogger,
	authProviderService authn.AuthProviderService) (*authn.MultiAuth, error) {

	// Create TLS config for OIDC provider connections (nil if no config)
	var tlsConfig *tls.Config
	if cfg != nil {
		tlsConfig = getTlsConfig(cfg)
	}

	// Always create MultiAuth instance - dynamic providers can come from DB
	multiAuth := authn.NewMultiAuth(authProviderService, tlsConfig, log)

	// Initialize static authentication methods if configuration is provided
	if cfg != nil && cfg.Auth != nil {
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

		if cfg.Auth.OpenShift != nil {
			if cfg.Auth.OpenShift.ClusterControlPlaneUrl == nil || *cfg.Auth.OpenShift.ClusterControlPlaneUrl == "" {
				return nil, errors.New("OpenShift ClusterControlPlaneUrl is required but not configured")
			}
			if cfg.Auth.OpenShift.AuthorizationUrl == nil || *cfg.Auth.OpenShift.AuthorizationUrl == "" {
				return nil, errors.New("OpenShift AuthorizationUrl is required but not configured")
			}
			if cfg.Auth.OpenShift.ClientId == nil || *cfg.Auth.OpenShift.ClientId == "" {
				return nil, errors.New("OpenShift ClientId is required but not configured")
			}

			apiUrl := *cfg.Auth.OpenShift.ClusterControlPlaneUrl
			log.Infof("OpenShift auth enabled: %s", apiUrl)

			openshiftAuthN, err := initOpenShiftAuth(cfg, log)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize OpenShift auth: %w", err)
			}

			// Add OpenShift auth with issuer:clientId key
			openshiftIssuer := strings.TrimSuffix(*cfg.Auth.OpenShift.AuthorizationUrl, "/")
			openshiftKey := fmt.Sprintf("%s:%s", openshiftIssuer, *cfg.Auth.OpenShift.ClientId)
			multiAuth.AddStaticProvider(openshiftKey, openshiftAuthN)
			configuredAuthType = AuthTypeOpenShift
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

		if cfg.Auth.OAuth2 != nil {
			log.Infof("OAuth2 auth enabled: %s", cfg.Auth.OAuth2.AuthorizationUrl)
			oauth2AuthN, err := initOAuth2Auth(cfg, log)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize OAuth2 auth: %w", err)
			}
			oauth2Issuer := strings.TrimSuffix(cfg.Auth.OAuth2.AuthorizationUrl, "/")
			oauth2Key := fmt.Sprintf("%s:%s", oauth2Issuer, cfg.Auth.OAuth2.ClientId)
			multiAuth.AddStaticProvider(oauth2Key, oauth2AuthN)
			configuredAuthType = AuthTypeOauth2
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
	}

	// Note: MultiAuth supports both static and dynamic providers.
	// Dynamic providers are loaded from the database by the background loader.
	// Caller is responsible for starting the background loader with context
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

func (o K8sToK8sAuth) GetUserPermissions(ctx context.Context) (*api.PermissionList, error) {
	k8sTokenVal := ctx.Value(consts.TokenCtxKey)
	if k8sTokenVal == nil {
		return nil, fmt.Errorf("no k8s token in context")
	}
	k8sToken := k8sTokenVal.(string)
	return o.K8sAuthZ.GetUserPermissions(ctx, k8sToken)
}
