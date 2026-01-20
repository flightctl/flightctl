package authz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/jellydator/ttlcache/v3"
	"github.com/sirupsen/logrus"
	k8sAuthorizationV1 "k8s.io/api/authorization/v1"
)

type OpenShiftAuthZ struct {
	K8sClient k8sclient.K8SClient
	Log       logrus.FieldLogger
	Cache     *ttlcache.Cache[string, bool]
}

func NewOpenShiftAuthZ(ctx context.Context, k8sClient k8sclient.K8SClient, log logrus.FieldLogger) *OpenShiftAuthZ {
	cache := ttlcache.New[string, bool](
		ttlcache.WithTTL[string, bool](5 * time.Minute),
	)

	// Start cache in a goroutine that stops when context is cancelled
	go func() {
		<-ctx.Done()
		cache.Stop()
	}()
	go cache.Start()

	return &OpenShiftAuthZ{
		K8sClient: k8sClient,
		Log:       log,
		Cache:     cache,
	}
}

// hashToken creates a SHA256 hash of the token for cache key
func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// createCacheKey creates a unique cache key from token hash, resource, operation, and namespace
func createCacheKey(tokenHash, resource, op, namespace string) string {
	return fmt.Sprintf("%s:%s:%s:%s", tokenHash, resource, op, namespace)
}

func (osAuth OpenShiftAuthZ) CheckPermission(ctx context.Context, token string, resource string, op string) (bool, error) {
	osAuth.Log.Debugf("OpenShiftAuthZ: checking permission for resource=%s, op=%s", resource, op)

	// 1. Get mapped identity from context
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok {
		osAuth.Log.Debug("OpenShiftAuthZ: no mapped identity found in context")
		return false, fmt.Errorf("no mapped identity found in context")
	}

	// 2. Super admins have access to everything
	if mappedIdentity.IsSuperAdmin() {
		osAuth.Log.Debugf("OpenShiftAuthZ: permission granted for super admin user=%s, resource=%s, op=%s",
			mappedIdentity.GetUsername(), resource, op)
		return true, nil
	}

	// 3. Get organization ID from context
	orgUUID, ok := util.GetOrgIdFromContext(ctx)
	if !ok {
		osAuth.Log.Debug("OpenShiftAuthZ: no organization ID found in context")
		return false, fmt.Errorf("no organization ID found in context")
	}
	orgID := orgUUID.String()

	// 4. Verify user has access to the selected organization
	roles := mappedIdentity.GetRolesForOrg(orgID)
	if len(roles) == 0 {
		osAuth.Log.Debugf("OpenShiftAuthZ: user=%s has no roles in organization=%s",
			mappedIdentity.GetUsername(), orgID)
		return false, nil
	}

	// 5. Get the organization's external ID (namespace) from mapped identity
	organizations := mappedIdentity.GetOrganizations()
	var namespace string
	for _, org := range organizations {
		if org.ID.String() == orgID {
			namespace = org.ExternalID
			break
		}
	}

	if namespace == "" {
		osAuth.Log.Debugf("OpenShiftAuthZ: no external ID (namespace) found for organization=%s", orgID)
		return false, fmt.Errorf("no external ID found for organization %s", orgID)
	}

	osAuth.Log.Debugf("OpenShiftAuthZ: using namespace=%s for organization=%s", namespace, orgID)

	// 6. Check cache first
	tokenHash := hashToken(token)
	cacheKey := createCacheKey(tokenHash, resource, op, namespace)

	if cachedItem := osAuth.Cache.Get(cacheKey); cachedItem != nil {
		osAuth.Log.Debugf("OpenShiftAuthZ: cache hit for resource=%s, op=%s, namespace=%s", resource, op, namespace)
		return cachedItem.Value(), nil
	}

	osAuth.Log.Debugf("OpenShiftAuthZ: cache miss for resource=%s, op=%s, namespace=%s", resource, op, namespace)

	// 7. Delegate to K8s API for authorization check in the organization's namespace
	body, err := createSSAR(resource, op, namespace)
	if err != nil {
		osAuth.Log.Debugf("OpenShiftAuthZ: failed to create SelfSubjectAccessReview: %v", err)
		return false, err
	}

	osAuth.Log.Debugf("OpenShiftAuthZ: posting SelfSubjectAccessReview to K8s API for namespace=%s", namespace)
	res, err := osAuth.K8sClient.PostCRD(ctx, "authorization.k8s.io/v1/selfsubjectaccessreviews", body, k8sclient.WithToken(token))
	if err != nil {
		osAuth.Log.Debugf("OpenShiftAuthZ: K8s API call failed: %v", err)
		return false, err
	}

	ssar := &k8sAuthorizationV1.SelfSubjectAccessReview{}
	if err := json.Unmarshal(res, ssar); err != nil {
		osAuth.Log.Debugf("OpenShiftAuthZ: failed to unmarshal response: %v", err)
		return false, err
	}

	// 8. Store result in cache
	osAuth.Cache.Set(cacheKey, ssar.Status.Allowed, ttlcache.DefaultTTL)

	osAuth.Log.Debugf("OpenShiftAuthZ: permission check result for resource=%s, op=%s, namespace=%s: allowed=%v",
		resource, op, namespace, ssar.Status.Allowed)
	return ssar.Status.Allowed, nil
}

func (osAuth OpenShiftAuthZ) GetUserPermissions(ctx context.Context, token string) (*v1beta1.PermissionList, error) {
	osAuth.Log.Debug("OpenShiftAuthZ: getting user permissions")

	// 1. Get mapped identity from context
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok {
		osAuth.Log.Debug("OpenShiftAuthZ: no mapped identity found in context")
		return nil, fmt.Errorf("no mapped identity found in context")
	}

	// 2. Super admins have all permissions
	if mappedIdentity.IsSuperAdmin() {
		osAuth.Log.Debugf("OpenShiftAuthZ: user=%s is super admin, granting all permissions", mappedIdentity.GetUsername())
		return &v1beta1.PermissionList{
			Permissions: []v1beta1.Permission{
				{Resource: "*", Operations: []string{"*"}},
			},
		}, nil
	}

	// 3. Get organization ID from context
	orgUUID, ok := util.GetOrgIdFromContext(ctx)
	if !ok {
		osAuth.Log.Debug("OpenShiftAuthZ: no organization ID found in context")
		return nil, fmt.Errorf("no organization ID found in context")
	}
	orgID := orgUUID.String()

	// 4. Verify user has access to the selected organization
	roles := mappedIdentity.GetRolesForOrg(orgID)
	if len(roles) == 0 {
		osAuth.Log.Debugf("OpenShiftAuthZ: user=%s has no roles in organization=%s",
			mappedIdentity.GetUsername(), orgID)
		return &v1beta1.PermissionList{Permissions: []v1beta1.Permission{}}, nil
	}

	// 5. Get the organization's external ID (namespace) from mapped identity
	organizations := mappedIdentity.GetOrganizations()
	var namespace string
	for _, org := range organizations {
		if org.ID.String() == orgID {
			namespace = org.ExternalID
			break
		}
	}

	if namespace == "" {
		osAuth.Log.Debugf("OpenShiftAuthZ: no external ID (namespace) found for organization=%s", orgID)
		return nil, fmt.Errorf("no external ID found for organization %s", orgID)
	}

	osAuth.Log.Debugf("OpenShiftAuthZ: getting permissions for namespace=%s, organization=%s", namespace, orgID)

	// 6. Create SelfSubjectRulesReview to get all rules for the user
	ssrr := k8sAuthorizationV1.SelfSubjectRulesReview{
		Spec: k8sAuthorizationV1.SelfSubjectRulesReviewSpec{
			Namespace: namespace,
		},
	}

	body, err := json.Marshal(ssrr)
	if err != nil {
		osAuth.Log.Debugf("OpenShiftAuthZ: failed to create SelfSubjectRulesReview: %v", err)
		return nil, err
	}

	osAuth.Log.Debugf("OpenShiftAuthZ: posting SelfSubjectRulesReview to K8s API")
	res, err := osAuth.K8sClient.PostCRD(ctx, "authorization.k8s.io/v1/selfsubjectrulesreviews", body, k8sclient.WithToken(token))
	if err != nil {
		osAuth.Log.Debugf("OpenShiftAuthZ: K8s API call failed: %v", err)
		return nil, err
	}

	ssrrResponse := &k8sAuthorizationV1.SelfSubjectRulesReview{}
	if err := json.Unmarshal(res, ssrrResponse); err != nil {
		osAuth.Log.Debugf("OpenShiftAuthZ: failed to unmarshal response: %v", err)
		return nil, err
	}

	// 7. Convert K8s rules to our permission format
	permissions := make(map[string][]string)
	for _, rule := range ssrrResponse.Status.ResourceRules {
		// Only process rules for our API group (flightctl.io)
		if len(rule.APIGroups) > 0 && rule.APIGroups[0] != "flightctl.io" {
			continue
		}

		// Add permissions for each resource
		for _, resource := range rule.Resources {
			if existingVerbs, exists := permissions[resource]; exists {
				// Merge verbs, avoiding duplicates
				verbsMap := make(map[string]bool)
				for _, verb := range existingVerbs {
					verbsMap[verb] = true
				}
				for _, verb := range rule.Verbs {
					verbsMap[verb] = true
				}
				mergedVerbs := make([]string, 0, len(verbsMap))
				for verb := range verbsMap {
					mergedVerbs = append(mergedVerbs, verb)
				}
				permissions[resource] = mergedVerbs
			} else {
				// Copy verbs slice to avoid sharing
				verbsCopy := make([]string, len(rule.Verbs))
				copy(verbsCopy, rule.Verbs)
				permissions[resource] = verbsCopy
			}
		}
	}

	// 8. Convert to API format with sorted resources
	resources := make([]string, 0, len(permissions))
	for resource := range permissions {
		resources = append(resources, resource)
	}
	sort.Strings(resources)

	apiPermissions := make([]v1beta1.Permission, 0, len(permissions))
	for _, resource := range resources {
		ops := permissions[resource]
		// Sort operations for consistent output
		sort.Strings(ops)

		apiPermissions = append(apiPermissions, v1beta1.Permission{
			Resource:   resource,
			Operations: ops,
		})
	}

	osAuth.Log.Debugf("OpenShiftAuthZ: returning %d permissions for user=%s, org=%s", len(apiPermissions), mappedIdentity.GetUsername(), orgID)
	return &v1beta1.PermissionList{
		Permissions: apiPermissions,
	}, nil
}
