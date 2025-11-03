package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/flightctl/flightctl/internal/auth/authz"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
)

// MultiAuthZ routes authorization requests based on the identity's issuer type
type MultiAuthZ struct {
	k8sAuthZ    *authz.K8sAuthZ
	staticAuthZ *authz.StaticAuthZ
	log         logrus.FieldLogger
}

// CheckPermission checks permission based on the identity's issuer type
func (m *MultiAuthZ) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	// Get identity from context
	identityVal := ctx.Value(consts.IdentityCtxKey)
	if identityVal == nil {
		m.log.Debug("No identity in context, using static authZ")
		return m.staticAuthZ.CheckPermission(ctx, resource, op)
	}

	ident, ok := identityVal.(common.Identity)
	if !ok {
		m.log.Warnf("Identity in context has incorrect type: %T", identityVal)
		return m.staticAuthZ.CheckPermission(ctx, resource, op)
	}

	// Check issuer type
	issuer := ident.GetIssuer()
	if issuer == nil {
		m.log.Debug("Identity has no issuer, using static authZ")
		return m.staticAuthZ.CheckPermission(ctx, resource, op)
	}

	// If K8s issuer and K8s authZ is configured, use K8s authZ
	if issuer.Type == identity.AuthTypeK8s && m.k8sAuthZ != nil {
		m.log.Debugf("Using K8s authZ for identity from issuer: %s", issuer.String())
		// Get token from context
		k8sTokenVal := ctx.Value(consts.TokenCtxKey)
		if k8sTokenVal == nil {
			m.log.Warn("K8s identity but no token in context")
			return false, nil
		}
		k8sToken, ok := k8sTokenVal.(string)
		if !ok {
			m.log.Warnf("K8s token in context has incorrect type: %T", k8sTokenVal)
			return false, nil
		}
		return m.k8sAuthZ.CheckPermission(ctx, k8sToken, resource, op)
	}

	// For all other issuer types, use static authZ
	m.log.Debugf("Using static authZ for identity from issuer: %s", issuer.String())
	return m.staticAuthZ.CheckPermission(ctx, resource, op)
}

// InitMultiAuthZ initializes authorization with support for multiple methods
func InitMultiAuthZ(cfg *config.Config, log logrus.FieldLogger) (AuthZMiddleware, error) {
	value, exists := os.LookupEnv(DisableAuthEnvKey)
	if exists && value != "" {
		log.Warnln("AuthZ disabled")
		authZProvider := NilAuth{}
		return authZProvider, nil
	}

	if cfg.Auth == nil {
		return nil, errors.New("no auth configuration provided")
	}

	multiAuthZ := &MultiAuthZ{
		staticAuthZ: authz.NewStaticAuthZ(log),
		log:         log,
	}

	// Initialize K8s authZ if K8s auth is configured
	if cfg.Auth.K8s != nil {
		apiUrl := strings.TrimSuffix(cfg.Auth.K8s.ApiUrl, "/")
		log.Infof("K8s authZ enabled: %s", apiUrl)

		var k8sClient k8sclient.K8SClient
		var err error
		if apiUrl == k8sApiService {
			k8sClient, err = k8sclient.NewK8SClient()
		} else {
			k8sClient, err = k8sclient.NewK8SExternalClient(apiUrl, cfg.Auth.InsecureSkipTlsVerify, cfg.Auth.CACert)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to create k8s client for authZ: %w", err)
		}

		rbacNs := ""
		if cfg.Auth.K8s.RbacNs != nil {
			rbacNs = *cfg.Auth.K8s.RbacNs
		}
		multiAuthZ.k8sAuthZ = &authz.K8sAuthZ{
			K8sClient: k8sClient,
			Namespace: rbacNs,
			Log:       log,
		}
	}

	return multiAuthZ, nil
}
