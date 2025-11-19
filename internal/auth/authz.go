package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	v1alpha1 "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/authz"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/jellydator/ttlcache/v3"
	"github.com/sirupsen/logrus"
)

// MultiAuthZ routes authorization requests based on the identity's issuer type
type MultiAuthZ struct {
	k8sAuthZCache         *ttlcache.Cache[string, *authz.K8sAuthZ]
	openshiftAuthZCache   *ttlcache.Cache[string, *authz.OpenShiftAuthZ]
	insecureSkipTlsVerify bool
	caCert                string
	staticAuthZ           *authz.StaticAuthZ
	staticAuthZOnce       sync.Once
	log                   logrus.FieldLogger
	ctx                   context.Context
	started               bool
}

// getK8sAuthZ lazily initializes and returns k8sAuthZ for a given URL and namespace
func (m *MultiAuthZ) getK8sAuthZ(apiUrl string, rbacNs string) (*authz.K8sAuthZ, error) {
	// Create cache key from URL and namespace
	cacheKey := fmt.Sprintf("%s:%s", apiUrl, rbacNs)

	// Check cache first
	if item := m.k8sAuthZCache.Get(cacheKey); item != nil {
		return item.Value(), nil
	}

	m.log.Infof("Lazy-initializing k8s authZ for %s (namespace: %s)", apiUrl, rbacNs)

	var k8sClient k8sclient.K8SClient
	var err error
	if apiUrl == k8sApiService {
		k8sClient, err = k8sclient.NewK8SClient()
	} else {
		k8sClient, err = k8sclient.NewK8SExternalClient(
			apiUrl,
			m.insecureSkipTlsVerify,
			m.caCert,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s client for authZ: %w", err)
	}

	k8sAuthZ := &authz.K8sAuthZ{
		K8sClient: k8sClient,
		Namespace: rbacNs,
		Log:       m.log,
	}

	// Store in cache
	m.k8sAuthZCache.Set(cacheKey, k8sAuthZ, ttlcache.DefaultTTL)

	return k8sAuthZ, nil
}

// getOpenShiftAuthZ lazily initializes and returns openshiftAuthZ for a given URL
func (m *MultiAuthZ) getOpenShiftAuthZ(apiUrl string) (*authz.OpenShiftAuthZ, error) {
	// Check cache first
	if item := m.openshiftAuthZCache.Get(apiUrl); item != nil {
		return item.Value(), nil
	}

	m.log.Infof("Lazy-initializing OpenShift authZ for %s", apiUrl)

	var k8sClient k8sclient.K8SClient
	var err error
	if apiUrl == k8sApiService {
		k8sClient, err = k8sclient.NewK8SClient()
	} else {
		k8sClient, err = k8sclient.NewK8SExternalClient(
			apiUrl,
			m.insecureSkipTlsVerify,
			m.caCert,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create k8s client for OpenShift authZ: %w", err)
	}

	openshiftAuthZ := authz.NewOpenShiftAuthZ(m.ctx, k8sClient, m.log)

	// Store in cache with TTL
	m.openshiftAuthZCache.Set(apiUrl, openshiftAuthZ, ttlcache.DefaultTTL)

	return openshiftAuthZ, nil
}

// getStaticAuthZ lazily initializes and returns staticAuthZ
func (m *MultiAuthZ) getStaticAuthZ() *authz.StaticAuthZ {
	m.staticAuthZOnce.Do(func() {
		m.log.Debug("Lazy-initializing static authZ")
		m.staticAuthZ = authz.NewStaticAuthZ(m.log)
	})
	return m.staticAuthZ
}

// Start initializes the MultiAuthZ with the given context for cache lifecycle management
func (m *MultiAuthZ) Start(ctx context.Context) {
	m.ctx = ctx
	m.started = true

	// Start k8s authZ cache if configured
	if m.k8sAuthZCache != nil {
		go func() {
			<-ctx.Done()
			m.k8sAuthZCache.Stop()
		}()
		go m.k8sAuthZCache.Start()
		m.log.Debug("Started k8s authZ cache with context-based lifecycle")
	}

	// Start OpenShift authZ cache if configured
	if m.openshiftAuthZCache != nil {
		go func() {
			<-ctx.Done()
			m.openshiftAuthZCache.Stop()
		}()
		go m.openshiftAuthZCache.Start()
		m.log.Debug("Started OpenShift authZ cache with context-based lifecycle")
	}
}

// CheckPermission checks permission based on the identity's issuer type
func (m *MultiAuthZ) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	// Get identity from context
	identityVal := ctx.Value(consts.IdentityCtxKey)
	if identityVal == nil {
		m.log.Debug("No identity in context, using static authZ")
		return m.getStaticAuthZ().CheckPermission(ctx, resource, op)
	}

	ident, ok := identityVal.(common.Identity)
	if !ok {
		m.log.Warnf("Identity in context has incorrect type: %T", identityVal)
		return m.getStaticAuthZ().CheckPermission(ctx, resource, op)
	}

	// Check issuer type
	issuer := ident.GetIssuer()
	if issuer == nil {
		m.log.Debug("Identity has no issuer, using static authZ")
		return m.getStaticAuthZ().CheckPermission(ctx, resource, op)
	}

	// Check if this is an OpenShift identity
	if openshiftIdent, ok := ident.(*common.OpenShiftIdentity); ok && m.openshiftAuthZCache != nil {
		m.log.Debugf("Using OpenShift authZ for identity from issuer: %s", issuer.String())

		// Get token from context
		tokenVal := ctx.Value(consts.TokenCtxKey)
		if tokenVal == nil {
			m.log.Warn("OpenShift identity but no token in context")
			return false, nil
		}
		token, ok := tokenVal.(string)
		if !ok {
			m.log.Warnf("OpenShift token in context has incorrect type: %T", tokenVal)
			return false, nil
		}

		// Get control plane URL from identity
		controlPlaneUrl := openshiftIdent.GetControlPlaneUrl()
		if controlPlaneUrl == "" {
			m.log.Warn("OpenShift identity has no control plane URL")
			return false, nil
		}

		// Get or create openshiftAuthZ for this control plane
		openshiftAuthZ, err := m.getOpenShiftAuthZ(controlPlaneUrl)
		if err != nil {
			m.log.WithError(err).Errorf("Failed to initialize OpenShift authZ for %s", controlPlaneUrl)
			return false, err
		}

		return openshiftAuthZ.CheckPermission(ctx, token, resource, op)
	}

	// Check if this is a K8s identity
	if k8sIdent, ok := ident.(*common.K8sIdentity); ok && m.k8sAuthZCache != nil {
		m.log.Debugf("Using K8s authZ for identity from issuer: %s", issuer.String())

		// Get token from context
		tokenVal := ctx.Value(consts.TokenCtxKey)
		if tokenVal == nil {
			m.log.Warn("K8s identity but no token in context")
			return false, nil
		}
		token, ok := tokenVal.(string)
		if !ok {
			m.log.Warnf("K8s token in context has incorrect type: %T", tokenVal)
			return false, nil
		}

		// Get control plane URL from identity
		controlPlaneUrl := k8sIdent.GetControlPlaneUrl()
		if controlPlaneUrl == "" {
			m.log.Warn("K8s identity has no control plane URL")
			return false, nil
		}

		// Get rbac namespace from identity
		rbacNs := k8sIdent.GetRbacNs()

		// Get or create k8sAuthZ for this control plane
		k8sAuthZ, err := m.getK8sAuthZ(controlPlaneUrl, rbacNs)
		if err != nil {
			m.log.WithError(err).Errorf("Failed to initialize k8s authZ for %s", controlPlaneUrl)
			return false, err
		}

		return k8sAuthZ.CheckPermission(ctx, token, resource, op)
	}

	// For all other issuer types, use static authZ
	m.log.Debugf("Using static authZ for identity from issuer: %s", issuer.String())
	return m.getStaticAuthZ().CheckPermission(ctx, resource, op)
}

// GetUserPermissions gets all permissions for the user based on the identity's issuer type
func (m *MultiAuthZ) GetUserPermissions(ctx context.Context) (*v1alpha1.PermissionList, error) {
	// Get identity from context
	identityVal := ctx.Value(consts.IdentityCtxKey)
	if identityVal == nil {
		m.log.Debug("No identity in context, using static authZ")
		return m.getStaticAuthZ().GetUserPermissions(ctx)
	}

	ident, ok := identityVal.(common.Identity)
	if !ok {
		m.log.Warnf("Identity in context has incorrect type: %T", identityVal)
		return m.getStaticAuthZ().GetUserPermissions(ctx)
	}

	// Check issuer type
	issuer := ident.GetIssuer()
	if issuer == nil {
		m.log.Debug("Identity has no issuer, using static authZ")
		return m.getStaticAuthZ().GetUserPermissions(ctx)
	}

	// Check if this is an OpenShift identity
	if openshiftIdent, ok := ident.(*common.OpenShiftIdentity); ok && m.openshiftAuthZCache != nil {
		m.log.Debugf("Using OpenShift authZ for identity from issuer: %s", issuer.String())

		// Get token from context
		tokenVal := ctx.Value(consts.TokenCtxKey)
		if tokenVal == nil {
			m.log.Warn("OpenShift identity but no token in context")
			return nil, fmt.Errorf("no OpenShift token in context")
		}
		token, ok := tokenVal.(string)
		if !ok {
			m.log.Warnf("OpenShift token in context has incorrect type: %T", tokenVal)
			return nil, fmt.Errorf("OpenShift token has incorrect type")
		}

		// Get control plane URL from identity
		controlPlaneUrl := openshiftIdent.GetControlPlaneUrl()
		if controlPlaneUrl == "" {
			m.log.Warn("OpenShift identity has no control plane URL")
			return nil, fmt.Errorf("OpenShift identity has no control plane URL")
		}

		// Get or create openshiftAuthZ for this control plane
		openshiftAuthZ, err := m.getOpenShiftAuthZ(controlPlaneUrl)
		if err != nil {
			m.log.WithError(err).Errorf("Failed to initialize OpenShift authZ for %s", controlPlaneUrl)
			return nil, err
		}

		return openshiftAuthZ.GetUserPermissions(ctx, token)
	}

	// Check if this is a K8s identity
	if k8sIdent, ok := ident.(*common.K8sIdentity); ok && m.k8sAuthZCache != nil {
		m.log.Debugf("Using K8s authZ for identity from issuer: %s", issuer.String())

		// Get token from context
		tokenVal := ctx.Value(consts.TokenCtxKey)
		if tokenVal == nil {
			m.log.Warn("K8s identity but no token in context")
			return nil, fmt.Errorf("no k8s token in context")
		}
		token, ok := tokenVal.(string)
		if !ok {
			m.log.Warnf("K8s token in context has incorrect type: %T", tokenVal)
			return nil, fmt.Errorf("k8s token has incorrect type")
		}

		// Get control plane URL from identity
		controlPlaneUrl := k8sIdent.GetControlPlaneUrl()
		if controlPlaneUrl == "" {
			m.log.Warn("K8s identity has no control plane URL")
			return nil, fmt.Errorf("K8s identity has no control plane URL")
		}

		// Get rbac namespace from identity
		rbacNs := k8sIdent.GetRbacNs()

		// Get or create k8sAuthZ for this control plane
		k8sAuthZ, err := m.getK8sAuthZ(controlPlaneUrl, rbacNs)
		if err != nil {
			m.log.WithError(err).Errorf("Failed to initialize k8s authZ for %s", controlPlaneUrl)
			return nil, err
		}

		return k8sAuthZ.GetUserPermissions(ctx, token)
	}

	// For all other issuer types, use static authZ
	m.log.Debugf("Using static authZ for identity from issuer: %s", issuer.String())
	return m.getStaticAuthZ().GetUserPermissions(ctx)
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
		log:                   log,
		insecureSkipTlsVerify: cfg.Auth.InsecureSkipTlsVerify,
		caCert:                cfg.Auth.CACert,
	}

	// Configure K8s authZ if K8s auth is configured
	if cfg.Auth.K8s != nil {
		// Initialize k8s authZ cache with 5 minute TTL
		multiAuthZ.k8sAuthZCache = ttlcache.New(ttlcache.WithTTL[string, *authz.K8sAuthZ](5 * time.Minute))

		log.Infof("K8s authZ configured (lazy-init)")
	}

	// Configure OpenShift authZ if OpenShift auth is configured
	if cfg.Auth.OpenShift != nil {
		// Initialize OpenShift authZ cache with 5 minute TTL
		multiAuthZ.openshiftAuthZCache = ttlcache.New(ttlcache.WithTTL[string, *authz.OpenShiftAuthZ](5 * time.Minute))

		log.Infof("OpenShift authZ configured (lazy-init)")
	}

	return multiAuthZ, nil
}
