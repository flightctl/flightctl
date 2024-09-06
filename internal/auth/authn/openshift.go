package authn

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/auth/common"
)

type OpenShiftAuthN struct {
	OpenShiftApiUrl string
	ClientTlsConfig *tls.Config
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

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: o.ClientTlsConfig,
	}}
	res, err := client.Do(req)
	if err != nil {
		return false, err
	}

	return res.StatusCode == http.StatusOK, nil
}

func (o OpenShiftAuthN) GetAuthConfig() common.AuthConfig {
	return common.AuthConfig{
		Type: "OpenShift",
		Url:  o.OpenShiftApiUrl,
	}
}
