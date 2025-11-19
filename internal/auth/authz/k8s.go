package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
	k8sAuthorizationV1 "k8s.io/api/authorization/v1"
)

type K8sAuthZ struct {
	K8sClient k8sclient.K8SClient
	Namespace string
	Log       logrus.FieldLogger
}

func createSSAR(resource string, verb string, ns string) ([]byte, error) {
	ssar := k8sAuthorizationV1.SelfSubjectAccessReview{
		Spec: k8sAuthorizationV1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &k8sAuthorizationV1.ResourceAttributes{
				Verb:      verb,
				Group:     "flightctl.io",
				Resource:  resource,
				Namespace: ns,
			},
		},
	}
	return json.Marshal(ssar)
}

func (k8sAuth K8sAuthZ) CheckPermission(ctx context.Context, k8sToken string, resource string, op string) (bool, error) {
	k8sAuth.Log.Debugf("K8sAuthZ: checking permission for resource=%s, op=%s, namespace=%s", resource, op, k8sAuth.Namespace)

	// 1. Get mapped identity from context
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok {
		k8sAuth.Log.Debug("K8sAuthZ: no mapped identity found in context")
		return false, fmt.Errorf("no mapped identity found in context")
	}

	// 2. Super admins have access to everything
	if mappedIdentity.IsSuperAdmin() {
		k8sAuth.Log.Debugf("K8sAuthZ: permission granted for super admin user=%s, resource=%s, op=%s",
			mappedIdentity.GetUsername(), resource, op)
		return true, nil
	}

	// 3. Verify user has access to the selected organization
	orgID, ok := ctx.Value(consts.OrganizationIDCtxKey).(string)
	if !ok || orgID == "" {
		k8sAuth.Log.Debug("K8sAuthZ: no organization ID found in context")
		return false, fmt.Errorf("no organization ID found in context")
	}

	roles := mappedIdentity.GetRolesForOrg(orgID)
	if len(roles) == 0 {
		k8sAuth.Log.Debugf("K8sAuthZ: user=%s has no roles in organization=%s",
			mappedIdentity.GetUsername(), orgID)
		return false, nil
	}

	// 4. Delegate to K8s API for authorization check
	body, err := createSSAR(resource, op, k8sAuth.Namespace)
	if err != nil {
		k8sAuth.Log.Debugf("K8sAuthZ: failed to create SelfSubjectAccessReview: %v", err)
		return false, err
	}

	k8sAuth.Log.Debugf("K8sAuthZ: posting SelfSubjectAccessReview to K8s API")
	res, err := k8sAuth.K8sClient.PostCRD(ctx, "authorization.k8s.io/v1/selfsubjectaccessreviews", body, k8sclient.WithToken(k8sToken))
	if err != nil {
		k8sAuth.Log.Debugf("K8sAuthZ: K8s API call failed: %v", err)
		return false, err
	}

	ssar := &k8sAuthorizationV1.SelfSubjectAccessReview{}
	if err := json.Unmarshal(res, ssar); err != nil {
		k8sAuth.Log.Debugf("K8sAuthZ: failed to unmarshal response: %v", err)
		return false, err
	}

	k8sAuth.Log.Debugf("K8sAuthZ: permission check result for resource=%s, op=%s: allowed=%v", resource, op, ssar.Status.Allowed)
	return ssar.Status.Allowed, nil
}

func (k8sAuth K8sAuthZ) GetUserPermissions(ctx context.Context, k8sToken string) (*v1alpha1.PermissionList, error) {
	k8sAuth.Log.Debugf("K8sAuthZ: getting user permissions for namespace=%s", k8sAuth.Namespace)

	// Create SelfSubjectRulesReview to get all rules for the user
	ssrr := k8sAuthorizationV1.SelfSubjectRulesReview{
		Spec: k8sAuthorizationV1.SelfSubjectRulesReviewSpec{
			Namespace: k8sAuth.Namespace,
		},
	}

	body, err := json.Marshal(ssrr)
	if err != nil {
		k8sAuth.Log.Debugf("K8sAuthZ: failed to create SelfSubjectRulesReview: %v", err)
		return nil, err
	}

	k8sAuth.Log.Debugf("K8sAuthZ: posting SelfSubjectRulesReview to K8s API")
	res, err := k8sAuth.K8sClient.PostCRD(ctx, "authorization.k8s.io/v1/selfsubjectrulesreviews", body, k8sclient.WithToken(k8sToken))
	if err != nil {
		k8sAuth.Log.Debugf("K8sAuthZ: K8s API call failed: %v", err)
		return nil, err
	}

	ssrrResponse := &k8sAuthorizationV1.SelfSubjectRulesReview{}
	if err := json.Unmarshal(res, ssrrResponse); err != nil {
		k8sAuth.Log.Debugf("K8sAuthZ: failed to unmarshal response: %v", err)
		return nil, err
	}

	// Convert K8s rules to our permission format
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

	// Convert to API format with sorted resources
	resources := make([]string, 0, len(permissions))
	for resource := range permissions {
		resources = append(resources, resource)
	}
	sort.Strings(resources)

	apiPermissions := make([]v1alpha1.Permission, 0, len(permissions))
	for _, resource := range resources {
		ops := permissions[resource]
		// Sort operations for consistent output
		sort.Strings(ops)

		apiPermissions = append(apiPermissions, v1alpha1.Permission{
			Resource:   resource,
			Operations: ops,
		})
	}

	k8sAuth.Log.Debugf("K8sAuthZ: returning %d permissions", len(apiPermissions))
	return &v1alpha1.PermissionList{
		Permissions: apiPermissions,
	}, nil
}
