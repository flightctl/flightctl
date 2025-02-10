package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/jellydator/ttlcache/v3"
	k8sAuthenticationV1 "k8s.io/api/authentication/v1"
)

type K8sAuthN struct {
	k8sClient               k8sclient.K8SClient
	externalOpenShiftApiUrl string
	cache                   *ttlcache.Cache[string, *k8sAuthenticationV1.TokenReview]
}

func NewK8sAuthN(k8sClient k8sclient.K8SClient, externalOpenShiftApiUrl string) (*K8sAuthN, error) {
	authN := &K8sAuthN{
		k8sClient:               k8sClient,
		externalOpenShiftApiUrl: externalOpenShiftApiUrl,
		cache:                   ttlcache.New[string, *k8sAuthenticationV1.TokenReview](ttlcache.WithTTL[string, *k8sAuthenticationV1.TokenReview](time.Minute)),
	}
	go authN.cache.Start()
	return authN, nil
}

func (o K8sAuthN) loadTokenReview(ctx context.Context, token string) (*k8sAuthenticationV1.TokenReview, error) {
	item := o.cache.Get(token)
	if item != nil {
		return item.Value(), nil
	}
	body, err := json.Marshal(k8sAuthenticationV1.TokenReview{
		Spec: k8sAuthenticationV1.TokenReviewSpec{
			Token: token,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling resource: %w", err)
	}
	res, err := o.k8sClient.PostCRD(ctx, "authentication.k8s.io/v1/tokenreviews", body)
	if err != nil {
		return nil, err
	}

	review := &k8sAuthenticationV1.TokenReview{}
	if err := json.Unmarshal(res, review); err != nil {
		return nil, err
	}
	o.cache.Set(token, review, 5*time.Second)
	return review, nil
}

func (o K8sAuthN) ValidateToken(ctx context.Context, token string) error {
	review, err := o.loadTokenReview(ctx, token)
	if err != nil {
		return err
	}
	if !review.Status.Authenticated {
		return fmt.Errorf("user is not authenticated")
	}
	return nil
}

func (o K8sAuthN) GetAuthToken(r *http.Request) (string, error) {
	return common.ExtractBearerToken(r)
}

func (o K8sAuthN) GetIdentity(ctx context.Context, token string) (*common.Identity, error) {
	review, err := o.loadTokenReview(ctx, token)
	if err != nil {
		return nil, err
	}
	return &common.Identity{
		Username: review.Status.User.Username,
		UID:      review.Status.User.UID,
		Groups:   review.Status.User.Groups,
	}, nil
}

func (o K8sAuthN) GetAuthConfig() common.AuthConfig {
	return common.AuthConfig{
		Type: common.AuthTypeK8s,
		Url:  o.externalOpenShiftApiUrl,
	}
}
