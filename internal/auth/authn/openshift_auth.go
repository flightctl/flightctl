package authn

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/jellydator/ttlcache/v3"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	k8sAuthenticationV1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OpenShiftAuth implements OpenShift OAuth authentication using SelfSubjectReview validation
type OpenShiftAuth struct {
	metadata      api.ObjectMeta
	spec          api.OpenShiftProviderSpec
	k8sClient     k8sclient.K8SClient
	tlsConfig     *tls.Config
	cache         *ttlcache.Cache[string, *k8sAuthenticationV1.UserInfo]
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
	clientType := ""
	if spec.ClientType != nil {
		clientType = string(*spec.ClientType)
	}
	switch clientType {
	case "", string(api.Oauthclient):
		if spec.ClientSecret == nil || *spec.ClientSecret == "" {
			return nil, fmt.Errorf("clientSecret is required")
		}
	case string(api.Serviceaccount):
		if spec.ClientSecretFile == nil || *spec.ClientSecretFile == "" {
			spec.ClientSecretFile = lo.ToPtr("/var/run/secrets/kubernetes.io/serviceaccount/token")
		}
		if spec.Scopes == nil || len(*spec.Scopes) == 0 {
			spec.Scopes = &[]string{"user:info", "user:check-access", "user:list-projects"}
		}
	default:
		return nil, fmt.Errorf("unsupported clientType %q", clientType)
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
		cache:         ttlcache.New(ttlcache.WithTTL[string, *k8sAuthenticationV1.UserInfo](5 * time.Second)),
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

// GetOpenShiftSpec returns the internal OpenShift spec with the client secret
// intact (for internal use only). For service-account clients the token is
// reread on every call so credential rotation is picked up.
func (o *OpenShiftAuth) GetOpenShiftSpec() api.OpenShiftProviderSpec {
	spec := o.spec
	if o.isServiceAccountClient() {
		secret, err := o.readClientSecret()
		if err != nil {
			o.log.WithError(err).Warn("Failed to read OpenShift OAuth client token")
			spec.ClientSecret = nil
		} else {
			spec.ClientSecret = &secret
		}
	}
	return spec
}

func (o *OpenShiftAuth) isServiceAccountClient() bool {
	return o.spec.ClientType != nil && *o.spec.ClientType == api.Serviceaccount
}

func (o *OpenShiftAuth) readClientSecret() (string, error) {
	if o.spec.ClientSecretFile == nil || *o.spec.ClientSecretFile == "" {
		return "", fmt.Errorf("clientSecretFile is required")
	}
	data, err := os.ReadFile(*o.spec.ClientSecretFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// GetAuthToken extracts the Bearer token from the HTTP request
func (o *OpenShiftAuth) GetAuthToken(r *http.Request) (string, error) {
	return common.ExtractBearerToken(r)
}

// GetAuthConfig returns the OpenShift authentication configuration
func (o *OpenShiftAuth) GetAuthConfig() (*api.AuthConfig, error) {
	publicSpec := o.spec
	if o.isServiceAccountClient() {
		publicSpec.ClientSecret = nil
		publicSpec.ClientSecretFile = nil
	}
	provider := api.AuthProvider{
		ApiVersion: api.AuthProviderAPIVersion,
		Kind:       api.AuthProviderKind,
		Metadata:   o.metadata,
		Spec:       api.AuthProviderSpec{},
	}

	if err := provider.Spec.FromOpenShiftProviderSpec(publicSpec); err != nil {
		return nil, fmt.Errorf("failed to marshal OpenShift auth provider spec: %w", err)
	}

	return &api.AuthConfig{
		ApiVersion:           api.AuthConfigAPIVersion,
		DefaultProvider:      o.metadata.Name,
		OrganizationsEnabled: lo.ToPtr(true),
		Providers:            &[]api.AuthProvider{provider},
	}, nil
}

// ValidateToken validates an OpenShift OAuth token using the compatible identity endpoint.
func (o *OpenShiftAuth) ValidateToken(ctx context.Context, token string) error {
	_, err := o.loadUserInfo(ctx, token)
	return err
}

// GetIdentity extracts user identity, gets projects, and fetches roles per project.
func (o *OpenShiftAuth) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	// Check identity cache first
	tokenHash := hashToken(token)
	if cachedItem := o.identityCache.Get(tokenHash); cachedItem != nil {
		o.log.Debug("Identity cache hit")
		return cachedItem.Value(), nil
	}

	o.log.Debug("Identity cache miss")

	userInfo, err := o.loadUserInfo(ctx, token)
	if err != nil {
		return nil, err
	}

	username := userInfo.Username
	uid := userInfo.UID

	// Get projects (organizations) for the user
	rawProjects, err := o.getProjectsForUser(ctx, token)
	if err != nil {
		o.log.WithError(err).Warn("Failed to get projects for user")
		rawProjects = []string{}
	}

	// Apply prefix for identity; use raw names for API calls
	projects := make([]string, len(rawProjects))
	for i := range rawProjects {
		projects[i] = ApplyOrgPrefix(rawProjects[i], o.spec.OrganizationNamePrefix)
	}

	o.log.WithFields(logrus.Fields{
		"user":     username,
		"projects": projects,
	}).Debug("Extracted projects for user")

	// Get roles per project (use raw project name for cluster API)
	orgRoles := make(map[string][]string)
	for i, rawProject := range rawProjects {
		roles, err := o.getRolesForUserInProject(ctx, rawProject, username, userInfo.Groups)
		if err != nil {
			o.log.WithError(err).WithField("project", rawProject).Warn("Failed to get roles for project")
			continue
		}
		if len(roles) > 0 {
			orgRoles[projects[i]] = roles
		}
	}

	// Check if user is in system:cluster-admins group and grant global admin role
	for _, group := range userInfo.Groups {
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

// loadSelfSubjectReview calls the K8s SelfSubjectReview API with caching
func (o *OpenShiftAuth) loadSelfSubjectReview(ctx context.Context, token string) (*k8sAuthenticationV1.UserInfo, error) {
	cacheKey := hashToken(token)
	item := o.cache.Get(cacheKey)
	if item != nil {
		return item.Value(), nil
	}

	// SelfSubjectReview is authorized for every authenticated user, so validating
	// the caller's token does not require cluster-scoped tokenreviews permission.
	body, err := json.Marshal(k8sAuthenticationV1.SelfSubjectReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "SelfSubjectReview"},
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling SelfSubjectReview: %w", err)
	}

	res, err := o.k8sClient.PostCRD(ctx, "authentication.k8s.io/v1/selfsubjectreviews", body, k8sclient.WithToken(token))
	if err != nil {
		// A scope-constrained OpenShift OAuth token cannot create SelfSubjectReview.
		// The caller falls back to the OpenShift users/~ endpoint in that case.
		if !apierrors.IsForbidden(err) {
			o.log.WithError(err).Warn("SelfSubjectReview request failed")
		}
		return nil, err
	}

	review := &k8sAuthenticationV1.SelfSubjectReview{}
	if err := json.Unmarshal(res, review); err != nil {
		o.log.WithError(err).Warn("SelfSubjectReview unmarshal failed")
		return nil, err
	}

	userInfo := review.Status.UserInfo
	if userInfo.Username == "" {
		return nil, fmt.Errorf("user is not authenticated")
	}
	// Debug log the identity (without logging the token)
	o.log.WithFields(logrus.Fields{
		"user": userInfo.Username,
		"uid":  userInfo.UID,
	}).Debug("SelfSubjectReview status")

	o.cache.Set(cacheKey, &userInfo, ttlcache.DefaultTTL)
	return &userInfo, nil
}

type openShiftCurrentUser struct {
	Metadata struct {
		Name string `json:"name"`
		UID  string `json:"uid"`
	} `json:"metadata"`
	Groups []string `json:"groups"`
}

// loadUserInfo returns the caller identity using the most compatible endpoint.
// SelfSubjectReview works for ordinary Kubernetes credentials and OpenShift
// tokens with the user:full scope. Scope-constrained OpenShift OAuth tokens are
// rejected there and must use the OpenShift users/~ endpoint instead.
func (o *OpenShiftAuth) loadUserInfo(ctx context.Context, token string) (*k8sAuthenticationV1.UserInfo, error) {
	if o.isServiceAccountClient() {
		return o.loadCurrentUser(ctx, token)
	}
	userInfo, err := o.loadSelfSubjectReview(ctx, token)
	if err == nil {
		return userInfo, nil
	}
	if !apierrors.IsForbidden(err) {
		return nil, err
	}
	return o.loadCurrentUser(ctx, token)
}

func (o *OpenShiftAuth) loadCurrentUser(ctx context.Context, token string) (*k8sAuthenticationV1.UserInfo, error) {
	cacheKey := hashToken(token)
	if item := o.cache.Get(cacheKey); item != nil {
		return item.Value(), nil
	}

	res, err := o.k8sClient.GetCurrentUser(ctx, token)
	if err != nil {
		o.log.WithError(err).Warn("OpenShift current user request failed")
		return nil, err
	}

	var user openShiftCurrentUser
	if err := json.Unmarshal(res, &user); err != nil {
		o.log.WithError(err).Warn("OpenShift current user unmarshal failed")
		return nil, err
	}
	if user.Metadata.Name == "" {
		return nil, fmt.Errorf("user is not authenticated")
	}

	userInfo := &k8sAuthenticationV1.UserInfo{
		Username: user.Metadata.Name,
		UID:      user.Metadata.UID,
		Groups:   user.Groups,
	}
	o.log.WithFields(logrus.Fields{
		"user": userInfo.Username,
		"uid":  userInfo.UID,
	}).Debug("OpenShift current user status")
	o.cache.Set(cacheKey, userInfo, ttlcache.DefaultTTL)
	return userInfo, nil
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
func (o *OpenShiftAuth) getRolesForUserInProject(ctx context.Context, project, username string, groups []string) ([]string, error) {
	roles, err := o.k8sClient.ListRoleBindingsForUser(ctx, project, username, groups)
	if err != nil {
		return nil, err
	}
	// Normalize role names by stripping release suffix if present
	return normalizeRoleNames(roles, o.spec.RoleSuffix), nil
}
