package authz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/contextutil"
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
	orgID, ok := ctx.Value(consts.OrganizationIDCtxKey).(string)
	if !ok || orgID == "" {
		osAuth.Log.Debug("OpenShiftAuthZ: no organization ID found in context")
		return false, fmt.Errorf("no organization ID found in context")
	}

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
