package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	k8sAuthorizationV1 "k8s.io/api/authorization/v1"
)

type K8sAuthZ struct {
	ApiUrl string
}

func createSSAR(resource string, verb string) ([]byte, error) {
	ssar := k8sAuthorizationV1.SelfSubjectAccessReview{
		Spec: k8sAuthorizationV1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &k8sAuthorizationV1.ResourceAttributes{
				Verb:     verb,
				Group:    "flightctl.io",
				Resource: resource,
			},
		},
	}
	return json.Marshal(ssar)
}

func (k8sAuth K8sAuthZ) CheckPermission(ctx context.Context, k8sToken string, resource string, op string) (bool, error) {
	body, err := createSSAR(resource, op)
	if err != nil {
		return false, err
	}

	ssarUrl := k8sAuth.ApiUrl + "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ssarUrl, bytes.NewReader(body))
	if err != nil {
		return false, err
	}

	req.Header = map[string][]string{
		"Authorization": {"Bearer " + k8sToken},
		"Content-Type":  {"application/json"},
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}

	if res.StatusCode != 201 {
		return false, nil
	}

	ssar := k8sAuthorizationV1.SelfSubjectAccessReview{}

	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(bodyBytes, &ssar); err != nil {
		return false, err
	}
	return ssar.Status.Allowed, nil
}
