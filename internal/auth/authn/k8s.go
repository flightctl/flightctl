package authn

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	k8sAuthenticationV1 "k8s.io/api/authentication/v1"
)

type K8sAuthN struct {
	k8sClient               k8sclient.K8SClient
	externalOpenShiftApiUrl string
}

func NewK8sAuthN(k8sClient k8sclient.K8SClient, externalOpenShiftApiUrl string) (*K8sAuthN, error) {
	authN := &K8sAuthN{
		k8sClient:               k8sClient,
		externalOpenShiftApiUrl: externalOpenShiftApiUrl,
	}
	return authN, nil
}

func (o K8sAuthN) ValidateToken(ctx context.Context, token string) (bool, error) {
	body, err := json.Marshal(k8sAuthenticationV1.TokenReview{
		Spec: k8sAuthenticationV1.TokenReviewSpec{
			Token: token,
		},
	})
	if err != nil {
		return false, fmt.Errorf("marshalling resource: %w", err)
	}
	res, err := o.k8sClient.PostCRD(ctx, "authentication.k8s.io/v1/tokenreviews", body)
	if err != nil {
		return false, err
	}

	review := &k8sAuthenticationV1.TokenReview{}
	if err := json.Unmarshal(res, review); err != nil {
		return false, err
	}
	return review.Status.Authenticated, nil
}

func (o K8sAuthN) GetAuthConfig() common.AuthConfig {
	return common.AuthConfig{
		Type: "k8s",
		Url:  o.externalOpenShiftApiUrl,
	}
}
