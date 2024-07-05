package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type OpenShiftAuthN struct {
	OpenShiftApiUrl string
}

type OauthServerResponse struct {
	TokenEndpoint string `json:"token_endpoint"`
}

func (o OpenShiftAuthN) ValidateToken(ctx context.Context, token string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/apis/user.openshift.io/v1/users/~", o.OpenShiftApiUrl), nil)
	if err != nil {
		return false, err
	}

	req.Header = map[string][]string{
		"Authorization": {"Bearer " + token},
		"Content-Type":  {"application/json"},
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}

	return res.StatusCode == http.StatusOK, nil
}

func (o OpenShiftAuthN) GetTokenRequestURL(ctx context.Context) (string, error) {
	res, err := http.Get(fmt.Sprintf("%s/.well-known/oauth-authorization-server", o.OpenShiftApiUrl))
	if err != nil {
		return "", err
	}
	oauthResponse := OauthServerResponse{}
	defer res.Body.Close()
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(bodyBytes, &oauthResponse); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/request", oauthResponse.TokenEndpoint), nil
}
