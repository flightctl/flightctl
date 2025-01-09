package authz

import (
	"context"
	"encoding/json"

	"github.com/flightctl/flightctl/pkg/k8sclient"
	k8sAuthorizationV1 "k8s.io/api/authorization/v1"
)

type K8sAuthZ struct {
	K8sClient k8sclient.K8SClient
	Namespace string
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
	body, err := createSSAR(resource, op, k8sAuth.Namespace)
	if err != nil {
		return false, err
	}

	res, err := k8sAuth.K8sClient.PostCRD(ctx, "authorization.k8s.io/v1/selfsubjectaccessreviews", body, k8sclient.WithToken(k8sToken))
	if err != nil {
		return false, err
	}

	ssar := &k8sAuthorizationV1.SelfSubjectAccessReview{}
	if err := json.Unmarshal(res, ssar); err != nil {
		return false, err
	}
	return ssar.Status.Allowed, nil
}
