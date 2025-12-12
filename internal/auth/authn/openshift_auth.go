package authn

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/jellydator/ttlcache/v3"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	k8sAuthenticationV1 "k8s.io/api/authentication/v1"
)

// OpenShiftAuth implements OpenShift OAuth authentication using TokenReview validation
type OpenShiftAuth struct {
	metadata      api.ObjectMeta
	spec          api.OpenShiftProviderSpec
	k8sClient     k8sclient.K8SClient
	tlsConfig     *tls.Config
	cache         *ttlcache.Cache[string, *k8sAuthenticationV1.TokenReview]
	identityCache *ttlcache.Cache[string, common.Identity]
	log           logrus.FieldLogger
	cancel        context.CancelFunc
	mu            sync.Mutex
	started       bool
	stopOnce      sync.Once
}

// NewOpenShiftAuth creates a new OpenShift authentication instance
func NewOpenShiftAuth(metadata api.ObjectMeta, spec api.OpenShiftProviderSpec, k8sClient k8sclient.K8SClient, tlsConfig *tls.Config, log logrus.FieldLogger) (*OpenShiftAuth, error) {
	if spec.AuthorizationUrl == nil || *spec.AuthorizationUrl == "" {
		return nil, fmt.Errorf("authorizationUrl is required")
	}
	if spec.TokenUrl == nil || *spec.TokenUrl == "" {
		return nil, fmt.Errorf("tokenUrl is required")
	}
	if spec.ClientId == nil || *spec.ClientId == "" {
		return nil, fmt.Errorf("clientId is required")
	}
	if spec.ClientSecret == nil || *spec.ClientSecret == "" {
		return nil, fmt.Errorf("clientSecret is required")
	}
	if spec.ClusterControlPlaneUrl == nil || *spec.ClusterControlPlaneUrl == "" {
		return nil, fmt.Errorf("clusterControlPlaneUrl is required")
	}

	// Use authorizationUrl as issuer if issuer is not provided
	if spec.Issuer == nil || *spec.Issuer == "" {
		spec.Issuer = spec.AuthorizationUrl
	}

	auth := &OpenShiftAuth{
		metadata:      metadata,
		spec:          spec,
		k8sClient:     k8sClient,
		tlsConfig:     tlsConfig,
		cache:         ttlcache.New(ttlcache.WithTTL[string, *k8sAuthenticationV1.TokenReview](5 * time.Second)),
		identityCache: ttlcache.New(ttlcache.WithTTL[string, common.Identity](5 * time.Minute)),
		log:           log,
	}
	return auth, nil
}

func (o *OpenShiftAuth) IsEnabled() bool {
	return o.spec.Enabled != nil && *o.spec.Enabled
}

// Start starts the cache background cleanup
// Creates a child context that can be independently canceled via Stop()
func (o *OpenShiftAuth) Start(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.started {
		return fmt.Errorf("OpenShiftAuth provider already started")
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
		o.log.Debugf("OpenShiftAuth caches stopped")
	}()

	o.log.Debugf("OpenShiftAuth caches started")
	o.started = true
	return nil
}

// Stop stops the caches and cancels the provider's context
func (o *OpenShiftAuth) Stop() {
	o.mu.Lock()
	defer o.mu.Unlock()

	// Only stop if we were started
	if !o.started {
		return
	}

	o.stopOnce.Do(func() {
		if o.cancel != nil {
			o.log.Debugf("Stopping OpenShiftAuth provider")
			o.cancel()
		}
	})
}

// hashToken creates a SHA256 hash of the token for cache key
func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// GetOpenShiftSpec returns the internal OpenShift spec with client secret intact (for internal use only)
func (o *OpenShiftAuth) GetOpenShiftSpec() api.OpenShiftProviderSpec {
	return o.spec
}

// GetAuthToken extracts the Bearer token from the HTTP request
func (o *OpenShiftAuth) GetAuthToken(r *http.Request) (string, error) {
	return common.ExtractBearerToken(r)
}

// GetAuthConfig returns the OpenShift authentication configuration
func (o *OpenShiftAuth) GetAuthConfig() *api.AuthConfig {
	provider := api.AuthProvider{
		ApiVersion: api.AuthProviderAPIVersion,
		Kind:       api.AuthProviderKind,
		Metadata:   o.metadata,
		Spec:       api.AuthProviderSpec{},
	}

	_ = provider.Spec.FromOpenShiftProviderSpec(o.spec)

	return &api.AuthConfig{
		ApiVersion:           api.AuthConfigAPIVersion,
		DefaultProvider:      o.metadata.Name,
		OrganizationsEnabled: lo.ToPtr(true),
		Providers:            &[]api.AuthProvider{provider},
	}
}

// ValidateToken validates an OpenShift OAuth token using K8s TokenReview
func (o *OpenShiftAuth) ValidateToken(ctx context.Context, token string) error {
	review, err := o.loadTokenReview(ctx, token)
	if err != nil {
		return err
	}
	if !review.Status.Authenticated {
		return fmt.Errorf("user is not authenticated")
	}
	return nil
}

// GetIdentity extracts user identity from TokenReview, gets projects, and fetches roles per project
func (o *OpenShiftAuth) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	// Check identity cache first
	tokenHash := hashToken(token)
	if cachedItem := o.identityCache.Get(tokenHash); cachedItem != nil {
		o.log.Debug("Identity cache hit")
		return cachedItem.Value(), nil
	}

	o.log.Debug("Identity cache miss")

	review, err := o.loadTokenReview(ctx, token)
	if err != nil {
		return nil, err
	}

	if review == nil || !review.Status.Authenticated {
		return nil, fmt.Errorf("user is not authenticated")
	}

	username := review.Status.User.Username
	uid := review.Status.User.UID

	// Get projects (organizations) for the user
	projects, err := o.getProjectsForUser(ctx, token)
	if err != nil {
		o.log.WithError(err).Warn("Failed to get projects for user")
		projects = []string{}
	}

	o.log.WithFields(logrus.Fields{
		"user":     username,
		"projects": projects,
	}).Debug("Extracted projects for user")

	// Get roles per project
	orgRoles := make(map[string][]string)
	for _, project := range projects {
		roles, err := o.getRolesForUserInProject(ctx, project, username)
		if err != nil {
			o.log.WithError(err).WithField("project", project).Warn("Failed to get roles for project")
			continue
		}
		if len(roles) > 0 {
			orgRoles[project] = roles
		}
	}

	// Check if user is in system:cluster-admins group and grant global admin role
	for _, group := range review.Status.User.Groups {
		if group == "system:cluster-admins" {
			if orgRoles["*"] == nil {
				orgRoles["*"] = []string{}
			}
			orgRoles["*"] = append(orgRoles["*"], api.ExternalRoleAdmin)
			o.log.WithField("user", username).Debug("User is in system:cluster-admins, granting global admin role")
			break
		}
	}

	o.log.WithFields(logrus.Fields{
		"user":     username,
		"orgRoles": orgRoles,
	}).Debug("Extracted roles per organization")

	// Build ReportedOrganization with roles embedded
	reportedOrganizations, isSuperAdmin := common.BuildReportedOrganizations(projects, orgRoles, false)

	// Create issuer with OpenShift cluster information
	issuer := identity.NewIssuer(identity.AuthTypeOpenShift, *o.spec.Issuer)

	o.log.WithFields(logrus.Fields{
		"user":          username,
		"uid":           uid,
		"organizations": projects,
		"orgRoles":      orgRoles,
	}).Debug("OpenShift identity created")

	// Create OpenShift identity
	openshiftIdentity := common.NewOpenShiftIdentity(username, uid, reportedOrganizations, issuer, *o.spec.ClusterControlPlaneUrl)
	openshiftIdentity.SetSuperAdmin(isSuperAdmin)

	// Store identity in cache
	o.identityCache.Set(tokenHash, openshiftIdentity, ttlcache.DefaultTTL)

	return openshiftIdentity, nil
}

// loadTokenReview calls K8s TokenReview API with caching
func (o *OpenShiftAuth) loadTokenReview(ctx context.Context, token string) (*k8sAuthenticationV1.TokenReview, error) {
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
		return nil, fmt.Errorf("marshaling TokenReview: %w", err)
	}

	res, err := o.k8sClient.PostCRD(ctx, "authentication.k8s.io/v1/tokenreviews", body)
	if err != nil {
		o.log.WithError(err).Warn("TokenReview request failed")
		return nil, err
	}

	review := &k8sAuthenticationV1.TokenReview{}
	if err := json.Unmarshal(res, review); err != nil {
		o.log.WithError(err).Warn("TokenReview unmarshal failed")
		return nil, err
	}

	// Debug log the TokenReview status (without logging the token)
	o.log.WithFields(logrus.Fields{
		"authenticated": review.Status.Authenticated,
		"user":          review.Status.User.Username,
		"audiences":     review.Status.Audiences,
		"error":         review.Status.Error,
	}).Debug("TokenReview status")

	o.cache.Set(token, review, ttlcache.DefaultTTL)
	return review, nil
}

// getProjectsForUser lists OpenShift projects accessible to the user
func (o *OpenShiftAuth) getProjectsForUser(ctx context.Context, token string) ([]string, error) {
	// Build label selector if specified (enables server-side filtering)
	var labelSelector string
	if o.spec.ProjectLabelFilter != nil && *o.spec.ProjectLabelFilter != "" {
		labelSelector = *o.spec.ProjectLabelFilter
	}

	// Call OpenShift projects API with optional label selector for server-side filtering
	var opts []k8sclient.ListProjectsOption
	if labelSelector != "" {
		opts = append(opts, k8sclient.WithLabelSelector(labelSelector))
	}

	res, err := o.k8sClient.ListProjects(ctx, token, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}

	// Parse the project list response
	var projectList struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}

	if err := json.Unmarshal(res, &projectList); err != nil {
		return nil, fmt.Errorf("failed to parse project list: %w", err)
	}

	var projects []string
	for _, item := range projectList.Items {
		if item.Metadata.Name != "" {
			projects = append(projects, item.Metadata.Name)
		}
	}

	return projects, nil
}

// getRolesForUserInProject gets roles from RoleBindings in a project
func (o *OpenShiftAuth) getRolesForUserInProject(ctx context.Context, project, username string) ([]string, error) {
	roles, err := o.k8sClient.ListRoleBindingsForUser(ctx, project, username)
	if err != nil {
		return nil, err
	}
	// Normalize role names by stripping release suffix if present
	return normalizeRoleNames(roles, o.spec.RoleSuffix), nil
}
