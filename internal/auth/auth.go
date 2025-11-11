package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
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
	return &tls.Config{
		InsecureSkipVerify: cfg.Auth.InsecureSkipTlsVerify, //nolint:gosec
	}
}

func getOrgConfig(cfg *config.Config) *common.AuthOrganizationsConfig {
	orgConfig := &common.AuthOrganizationsConfig{
		Enabled: false,
	}

	if cfg.Organizations != nil {
		orgConfig.Enabled = cfg.Organizations.Enabled
	}

	// Include organization assignment from OIDC config if available
	if cfg.Auth.OIDC != nil {
		// Determine the discriminator to check which type it is
		discriminator, err := cfg.Auth.OIDC.OrganizationAssignment.Discriminator()
		if err == nil && discriminator != "" {
			// Convert API type to common type
			orgAssignment := &common.OrganizationAssignment{
				Type: discriminator,
			}

			// Extract the appropriate fields based on type
			switch discriminator {
			case "static":
				if staticAssignment, err := cfg.Auth.OIDC.OrganizationAssignment.AsAuthStaticOrganizationAssignment(); err == nil {
					orgAssignment.OrganizationName = &staticAssignment.OrganizationName
				}
			case "dynamic":
				if dynamicAssignment, err := cfg.Auth.OIDC.OrganizationAssignment.AsAuthDynamicOrganizationAssignment(); err == nil {
					orgAssignment.ClaimPath = dynamicAssignment.ClaimPath
					orgAssignment.OrganizationNamePrefix = dynamicAssignment.OrganizationNamePrefix
					orgAssignment.OrganizationNameSuffix = dynamicAssignment.OrganizationNameSuffix
				}
			case "perUser":
				if perUserAssignment, err := cfg.Auth.OIDC.OrganizationAssignment.AsAuthPerUserOrganizationAssignment(); err == nil {
					orgAssignment.OrganizationNamePrefix = perUserAssignment.OrganizationNamePrefix
					orgAssignment.OrganizationNameSuffix = perUserAssignment.OrganizationNameSuffix
				}
			}

			orgConfig.OrganizationAssignment = orgAssignment
		}
	}

	return orgConfig
}

func initOIDCAuth(cfg *config.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
	oidcUrl := strings.TrimSuffix(cfg.Auth.OIDC.Issuer, "/")
	externalOidcUrl := strings.TrimSuffix(cfg.Auth.OIDC.ExternalOIDCAuthority, "/")
	log.Infof("OIDC auth enabled: %s (external: %s)", oidcUrl, externalOidcUrl)
	usernameClaim := []string{"preferred_username"}
	if cfg.Auth.OIDC.UsernameClaim != nil {
		usernameClaim = *cfg.Auth.OIDC.UsernameClaim
	}

	// Use the role assignment from the config
	roleExtractor := authn.NewRoleExtractor(cfg.Auth.OIDC.RoleAssignment)

	scopes := []string{"openid", "profile", "email", "roles"}
	if len(cfg.Auth.OIDC.Scopes) > 0 {
		scopes = cfg.Auth.OIDC.Scopes
	}
	clientId := cfg.Auth.OIDC.ClientId
	authNProvider, err := authn.NewOIDCAuth("oidc", "", oidcUrl, externalOidcUrl, getTlsConfig(cfg), getOrgConfig(cfg), usernameClaim, roleExtractor, clientId, scopes)
	if err != nil {
		return nil, fmt.Errorf("failed to create OIDC AuthN: %w", err)
	}
	return authNProvider, nil
}

func initAAPAuth(cfg *config.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
	gatewayUrl := strings.TrimSuffix(cfg.Auth.AAP.ApiUrl, "/")
	gatewayExternalUrl := strings.TrimSuffix(cfg.Auth.AAP.ExternalApiUrl, "/")
	log.Infof("AAP Gateway auth enabled: %s", gatewayUrl)
	authNProvider, err := authn.NewAapGatewayAuth(gatewayUrl, gatewayExternalUrl, getTlsConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("failed to create AAP Gateway AuthN: %w", err)
	}
	return authNProvider, nil
}

func initK8sAuth(cfg *config.Config, log logrus.FieldLogger) (common.AuthNMiddleware, error) {
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
		return nil, fmt.Errorf("failed to create k8s client: %w", err)
	}
	authNProvider, err := authn.NewK8sAuthN(k8sClient, externalOpenShiftApiUrl, cfg.Auth.K8s.RBACNs)
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
		authNProvider := NilAuth{}
		return authNProvider, nil
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

	// Start the cache background cleanup
	multiAuth.Start()

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
