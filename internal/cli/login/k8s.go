package login

import (
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/openshift/osincli"
)

const (
	csrfTokenHeader = "X-CSRF-Token" //nolint:gosec
)

type K8sOauth struct {
	ClientId           string
	CAFile             string
	InsecureSkipVerify bool
	ConfigUrl          string
}

func NewK8sOAuth2Config(caFile, clientId, authUrl string, insecure bool) K8sOauth {
	return K8sOauth{
		CAFile:             caFile,
		InsecureSkipVerify: insecure,
		ClientId:           clientId,
		ConfigUrl:          fmt.Sprintf("%s/.well-known/oauth-authorization-server", authUrl),
	}
}

func (k K8sOauth) getOAuth2Client(callback string) (*osincli.Client, error) {
	oauthServerResponse, err := GetOAuth2Config(k.ConfigUrl, k.CAFile, k.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}

	redirectUrl := callback
	if redirectUrl == "" {
		redirectUrl = oauthServerResponse.TokenEndpoint + "/implicit"
	}

	config := &osincli.ClientConfig{
		ClientId:           k.ClientId,
		AuthorizeUrl:       oauthServerResponse.AuthEndpoint,
		TokenUrl:           oauthServerResponse.TokenEndpoint,
		ErrorsInStatusCode: true,
		RedirectUrl:        redirectUrl,
	}

	client, err := osincli.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create oauth2 client: %w", err)
	}

	tlsConfig, err := getAuthClientTlsConfig(k.CAFile, k.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}
	client.Transport = &http.Transport{
		TLSClientConfig: tlsConfig,
	}

	return client, nil
}

func (k K8sOauth) authHeadless(username, password string) (string, error) {
	client, err := k.getOAuth2Client("")
	if err != nil {
		return "", err
	}
	authorizeRequest := client.NewAuthorizeRequest(osincli.CODE)
	requestURL := authorizeRequest.GetAuthorizeUrl().String()
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(csrfTokenHeader, "1")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)))
	resp, err := client.Transport.RoundTrip(req)

	if err != nil {
		return "", err
	}

	if resp.StatusCode == http.StatusFound {
		redirectURL := resp.Header.Get("Location")

		req, err := http.NewRequest(http.MethodGet, redirectURL, nil)
		if err != nil {
			return "", err
		}

		return getOAuth2AccessToken(client, authorizeRequest, req)
	}
	return "", fmt.Errorf("unexpected http code: %v", resp.StatusCode)
}

func (k K8sOauth) Auth(web bool, username, password string) (string, error) {
	if web {
		return oauth2AuthCodeFlow(k.getOAuth2Client)
	}
	return k.authHeadless(username, password)
}
