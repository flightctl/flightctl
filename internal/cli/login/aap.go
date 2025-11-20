package login

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/openshift/osincli"
)

type AAPOAuth struct {
	ClientId           string
	CAFile             string
	InsecureSkipVerify bool
	ConfigUrl          string
	Scopes             string
	ServerUrl          string
	ProviderName       string
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
	if resp.StatusCode == http.StatusCreated {
		resp.StatusCode = http.StatusOK
	}
	return resp, nil
}

func NewAAPOAuth2Config(caFile, clientId, authUrl string, insecure bool) AAPOAuth {
	return NewAAPOAuth2ConfigWithScopes(caFile, clientId, authUrl, "", insecure)
}

func NewAAPOAuth2ConfigWithScopes(caFile, clientId, authUrl, scopes string, insecure bool) AAPOAuth {
	return NewAAPOAuth2ConfigWithTokenProxy(caFile, clientId, authUrl, scopes, "", "", insecure)
}

func NewAAPOAuth2ConfigWithTokenProxy(caFile, clientId, authUrl, scopes, serverUrl, providerName string, insecure bool) AAPOAuth {
	return AAPOAuth{
		CAFile:             caFile,
		InsecureSkipVerify: insecure,
		ClientId:           clientId,
		ConfigUrl:          authUrl,
		Scopes:             scopes,
		ServerUrl:          serverUrl,
		ProviderName:       providerName,
	}
}

func (o AAPOAuth) getOAuth2Config() OauthServerResponse {
	tokenEndpoint := fmt.Sprintf("%s/o/token/", o.ConfigUrl)

	if o.ServerUrl != "" && o.ProviderName != "" {
		tokenEndpoint = fmt.Sprintf("%s/api/v1/auth/%s/token", strings.TrimSuffix(o.ServerUrl, "/"), o.ProviderName)
	}

	return OauthServerResponse{
		TokenEndpoint: tokenEndpoint,
		AuthEndpoint:  fmt.Sprintf("%s/o/authorize/", o.ConfigUrl),
	}
}

func (o AAPOAuth) getOAuth2Client(callback string) (*osincli.Client, string, error) {
	oauthServerResponse := o.getOAuth2Config()
	codeVerifier, codeChallenge, err := generatePKCEVerifier()
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate PKCE parameters: %w", err)
	}

	config := &osincli.ClientConfig{
		ClientId:                 o.ClientId,
		AuthorizeUrl:             oauthServerResponse.AuthEndpoint,
		TokenUrl:                 oauthServerResponse.TokenEndpoint,
		ErrorsInStatusCode:       true,
		SendClientSecretInParams: true,
		RedirectUrl:              callback,
		Scope:                    o.getScopes(),
		CodeVerifier:             codeVerifier,
		CodeChallenge:            codeChallenge,
		CodeChallengeMethod:      "S256",
	}

	client, err := osincli.NewClient(config)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create oauth2 client: %w", err)
	}

	tlsConfig, err := getAuthClientTlsConfig(o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return nil, "", err
	}

	client.Transport = &AAPRoundTripper{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	return client, o.ClientId, nil
}

func (o AAPOAuth) getScopes() string {
	if o.Scopes != "" {
		return o.Scopes
	}
	return "read"
}

func (o AAPOAuth) Auth(web bool, username, password string) (AuthInfo, error) {
	// only web flow is supported
	return oauth2AuthCodeFlow(o.getOAuth2Client)
}

func (o AAPOAuth) Renew(refreshToken string) (AuthInfo, error) {
	return oauth2RefreshTokenFlow(refreshToken, o.getOAuth2Client)
}

func (o AAPOAuth) Validate(args ValidateArgs) error {
	if !StrIsEmpty(args.Username) || !StrIsEmpty(args.Password) {
		return fmt.Errorf("--username and --password are not supported for AAP Oauth2")
	}
	if StrIsEmpty(args.AccessToken) && !args.Web {
		fmt.Println("You must provide one of the following options to log in:")
		fmt.Println("  --token=<token>")
		fmt.Println("  --web (to log in via your browser)")
		return fmt.Errorf("not enough options specified")
	}

	if args.Web && StrIsEmpty(args.ClientId) {
		return fmt.Errorf("--client-id must be specified for AAP Gateway auth")
	}

	return nil
}
