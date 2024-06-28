package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/flightctl/flightctl/internal/auth/common"
)

type K8sAuthN struct {
	K8sApiUrl string
}

type OauthServerResponse struct {
	TokenEndpoint string `json:"token_endpoint"`
}

func (k8s K8sAuthN) ValidateToken(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/apis/user.openshift.io/v1/users/~", k8s.K8sApiUrl), nil)
	if err != nil {
		return false, err
	}

	k8sTokenVal := ctx.Value(common.TokenCtxKey)
	if k8sTokenVal == nil {
		return false, nil
	}
	k8sToken := k8sTokenVal.(string)

	req.Header = map[string][]string{
		"Authorization": {"Bearer " + k8sToken},
		"Content-Type":  {"application/json"},
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}

	return res.StatusCode == http.StatusOK, nil
}

func (k8s K8sAuthN) GetTokenRequestURL(ctx context.Context) (string, error) {
	res, err := http.Get(fmt.Sprintf("%s/.well-known/oauth-authorization-server", k8s.K8sApiUrl))
	if err != nil {
		return "", err
	}
	oauthResponse := OauthServerResponse{}
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(bodyBytes, &oauthResponse); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/request", oauthResponse.TokenEndpoint), nil
}
