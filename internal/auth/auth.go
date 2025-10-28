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
	authNProvider, err := authn.NewOIDCAuth(oidcUrl, getTlsConfig(cfg), getOrgConfig(cfg), usernameClaim, roleClaim, "", []string{})
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

func InitAuth(cfg *config.Config, log logrus.FieldLogger, orgResolver resolvers.Resolver) (AuthNMiddleware, AuthZMiddleware, error) {
	value, exists := os.LookupEnv(DisableAuthEnvKey)
	if exists && value != "" {
		log.Warnln("Auth disabled")
		configuredAuthType = AuthTypeNil
		authNProvider := NilAuth{}
		authZProvider := NilAuth{}
		return authNProvider, authZProvider, nil
	} else if cfg.Auth != nil {
		var authNProvider AuthNMiddleware
		var authZProvider AuthZMiddleware
		var err error
		if cfg.Auth.K8s != nil {
			configuredAuthType = AuthTypeK8s
			authNProvider, authZProvider, err = initK8sAuth(cfg, log)
		} else if cfg.Auth.OIDC != nil {
			configuredAuthType = AuthTypeOIDC
			authNProvider, authZProvider, err = initOIDCAuth(cfg, log, orgResolver)
		} else if cfg.Auth.AAP != nil {
			configuredAuthType = AuthTypeAAP
			authNProvider, authZProvider, err = initAAPAuth(cfg, log, orgResolver)
		}

		if err != nil {
			return nil, nil, err
		}

		if authNProvider == nil {
			return nil, nil, errors.New("no authN provider defined")
		}
		if authZProvider == nil {
			return nil, nil, errors.New("no authZ provider defined")
		}
		return authNProvider, authZProvider, nil
	}

	return nil, nil, errors.New("no auth configuration provided")
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
