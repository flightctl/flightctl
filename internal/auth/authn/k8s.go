package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/jellydator/ttlcache/v3"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	k8sAuthenticationV1 "k8s.io/api/authentication/v1"
)

type K8sAuthN struct {
	metadata      api.ObjectMeta
	spec          api.K8sProviderSpec
	k8sClient     k8sclient.K8SClient
	cache         *ttlcache.Cache[string, *k8sAuthenticationV1.TokenReview]
	identityCache *ttlcache.Cache[string, common.Identity]
	cancel        context.CancelFunc
	mu            sync.Mutex
	started       bool
	stopOnce      sync.Once
}

func NewK8sAuthN(metadata api.ObjectMeta, spec api.K8sProviderSpec, k8sClient k8sclient.K8SClient) (*K8sAuthN, error) {
	authN := &K8sAuthN{
		metadata:      metadata,
		spec:          spec,
		k8sClient:     k8sClient,
		cache:         ttlcache.New(ttlcache.WithTTL[string, *k8sAuthenticationV1.TokenReview](5 * time.Second)),
		identityCache: ttlcache.New(ttlcache.WithTTL[string, common.Identity](30 * time.Second)),
	}
	return authN, nil
}

func (o *K8sAuthN) IsEnabled() bool {
	return o.spec.Enabled != nil && *o.spec.Enabled
}

// Start starts the cache background cleanup
// Creates a child context that can be independently canceled via Stop()
func (o *K8sAuthN) Start(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.started {
		return fmt.Errorf("K8sAuthN provider already started")
	}

	// Create a child context so this provider can be stopped independently
	providerCtx, cancel := context.WithCancel(ctx)
	o.cancel = cancel

	// Start caches in goroutines (cache.Start() blocks waiting for cleanup events)
	go o.cache.Start()
	go o.identityCache.Start()

	go func() {
		<-providerCtx.Done()
		o.cache.Stop()
		o.identityCache.Stop()
		logrus.Debugf("K8sAuthN caches stopped")
	}()

	logrus.Debugf("K8sAuthN caches started")
	o.started = true
	return nil
}

// Stop stops the caches and cancels the provider's context
func (o *K8sAuthN) Stop() {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Only stop if we were started
	if !o.started {
		return
	}

	o.stopOnce.Do(func() {
		if o.cancel != nil {
			logrus.Debugf("Stopping K8sAuthN provider")
			o.cancel()
		}
	})
}

func (o *K8sAuthN) loadTokenReview(ctx context.Context, token string) (*k8sAuthenticationV1.TokenReview, error) {
	item := o.cache.Get(token)
	if item != nil {
		return item.Value(), nil
	}
	// Standard TokenReview without audiences; API server validates bound SA tokens
	body, err := json.Marshal(k8sAuthenticationV1.TokenReview{
		Spec: k8sAuthenticationV1.TokenReviewSpec{
			Token: token,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling resource: %w", err)
	}
	res, err := o.k8sClient.PostCRD(ctx, "authentication.k8s.io/v1/tokenreviews", body)
	if err != nil {
		logrus.WithError(err).Warn("TokenReview request failed")
		return nil, err
	}

	review := &k8sAuthenticationV1.TokenReview{}
	if err := json.Unmarshal(res, review); err != nil {
		logrus.WithError(err).Warn("TokenReview unmarshal failed")
		return nil, err
	}
	// Debug log the TokenReview status (without logging the token)
	logrus.WithFields(logrus.Fields{
		"authenticated": review.Status.Authenticated,
		"user":          review.Status.User.Username,
		"audiences":     review.Status.Audiences,
		"error":         review.Status.Error,
	}).Debug("TokenReview status")
	o.cache.Set(token, review, ttlcache.DefaultTTL)
	return review, nil
}

func (o *K8sAuthN) ValidateToken(ctx context.Context, token string) error {
	review, err := o.loadTokenReview(ctx, token)
	if err != nil {
		return err
	}
	if !review.Status.Authenticated {
		return fmt.Errorf("user is not authenticated")
	}
	return nil
}

func (o *K8sAuthN) GetAuthToken(r *http.Request) (string, error) {
	return common.ExtractBearerToken(r)
}

func (o *K8sAuthN) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	review, err := o.loadTokenReview(ctx, token)
	if err != nil {
		return nil, err
	}

	// Compute cache key: prefer UID, fallback to Username
	cacheKey := review.Status.User.UID
	if cacheKey == "" {
		cacheKey = review.Status.User.Username
	}

	// Check identity cache first using computed key
	if cacheKey != "" {
		if item := o.identityCache.Get(cacheKey); item != nil {
			logrus.WithFields(logrus.Fields{
				"user":     review.Status.User.Username,
				"uid":      review.Status.User.UID,
				"cacheKey": cacheKey,
			}).Debug("K8s identity retrieved from cache")
			return item.Value(), nil
		}
	}

	// Always use the default organization
	organizations := []string{org.DefaultExternalID}

	// Fetch role bindings from the rbac namespace
	var roles []string
	if o.spec.RbacNs != nil && *o.spec.RbacNs != "" {
		var err error
		roles, err = o.k8sClient.ListRoleBindingsForUser(ctx, *o.spec.RbacNs, review.Status.User.Username)
		if err != nil {
			logrus.WithError(err).WithField("namespace", *o.spec.RbacNs).Warn("Failed to list role bindings")
			roles = []string{}
		}
		// Normalize role names by stripping release suffix if present
		roles = normalizeRoleNames(roles, o.spec.RoleSuffix)
	}

	logrus.WithFields(logrus.Fields{
		"user":          review.Status.User.Username,
		"organizations": organizations,
		"roles":         roles,
		"rbacNs":        o.spec.RbacNs,
	}).Debug("K8s identity created")

	// Create issuer with K8s cluster information
	issuer := identity.NewIssuer(identity.AuthTypeK8s, "k8s-cluster") // TODO: Get actual cluster name from config

	// Build ReportedOrganization with roles embedded
	// K8s roles are global - apply to all organizations
	orgRoles := map[string][]string{
		"*": roles, // All K8s roles are global
	}
	reportedOrganizations, isSuperAdmin := common.BuildReportedOrganizations(organizations, orgRoles, false)

	// Get rbac namespace, default to empty string if not set
	rbacNs := ""
	if o.spec.RbacNs != nil {
		rbacNs = *o.spec.RbacNs
	}

	k8sIdentity := common.NewK8sIdentity(review.Status.User.Username, review.Status.User.UID, reportedOrganizations, issuer, o.spec.ApiUrl, rbacNs)
	k8sIdentity.SetSuperAdmin(isSuperAdmin)

	// Cache the identity using the same key logic (skip if both UID and Username are empty)
	if cacheKey != "" {
		o.identityCache.Set(cacheKey, k8sIdentity, ttlcache.DefaultTTL)
	}

	return k8sIdentity, nil
}

func (o *K8sAuthN) GetAuthConfig() *api.AuthConfig {
	provider := api.AuthProvider{
		ApiVersion: api.AuthProviderAPIVersion,
		Kind:       api.AuthProviderKind,
		Metadata:   o.metadata,
		Spec:       api.AuthProviderSpec{},
	}
	_ = provider.Spec.FromK8sProviderSpec(o.spec)

	return &api.AuthConfig{
		ApiVersion:           api.AuthConfigAPIVersion,
		DefaultProvider:      o.metadata.Name,
		OrganizationsEnabled: lo.ToPtr(true),
		Providers:            &[]api.AuthProvider{provider},
	}
}
