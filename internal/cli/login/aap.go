package login

import (
	"fmt"
	"net/http"

	"github.com/openshift/osincli"
)

type AAPOAuth struct {
	ClientId           string
	CAFile             string
	InsecureSkipVerify bool
	ConfigUrl          string
}

type AAPRoundTripper struct {
	Transport http.RoundTripper
}

func (c *AAPRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.Transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// AAPGateway returns 201 on success, but osincli expects 200
	if resp.StatusCode == 201 {
		resp.StatusCode = 200
	}
	return resp, nil
}

func NewAAPOAuth2Config(caFile, clientId, authUrl string, insecure bool) AAPOAuth {
	return AAPOAuth{
		CAFile:             caFile,
		InsecureSkipVerify: insecure,
		ClientId:           clientId,
		ConfigUrl:          authUrl,
	}
}

func (o AAPOAuth) getOAuth2Config() (OauthServerResponse, error) {
	return OauthServerResponse{
		TokenEndpoint: fmt.Sprintf("%s/o/token/", o.ConfigUrl),
		AuthEndpoint:  fmt.Sprintf("%s/o/authorize/", o.ConfigUrl),
	}, nil
}

func (o AAPOAuth) getOAuth2Client(callback string) (*osincli.Client, error) {
	oauthServerResponse, err := o.getOAuth2Config()
	if err != nil {
		return nil, err
	}

	config := &osincli.ClientConfig{
		ClientId:                 o.ClientId,
		AuthorizeUrl:             oauthServerResponse.AuthEndpoint,
		TokenUrl:                 oauthServerResponse.TokenEndpoint,
		ErrorsInStatusCode:       true,
		SendClientSecretInParams: true,
		RedirectUrl:              callback,
		Scope:                    "read",
	}

	client, err := osincli.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create oauth2 client: %w", err)
	}

	tlsConfig, err := getAuthClientTlsConfig(o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}

	client.Transport = &AAPRoundTripper{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	return client, nil
}

func (o AAPOAuth) Auth(web bool, username, password string) (string, error) {
	return oauth2AuthCodeFlow(o.getOAuth2Client)
}
