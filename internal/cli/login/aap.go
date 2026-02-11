package login

import (
	"fmt"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/openshift/osincli"
)

type AAPOAuth struct {
	Metadata           api.ObjectMeta
	Spec               api.AapProviderSpec
	CAFile             string
	InsecureSkipVerify bool
	ApiServerURL       string
	CallbackPort       int
	Username           string
	Password           string
	Web                bool
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

func NewAAPOAuth2Config(metadata api.ObjectMeta, spec api.AapProviderSpec, caFile string, insecure bool, apiServerURL string, callbackPort int, username, password string, web bool) *AAPOAuth {
	return &AAPOAuth{
		Metadata:           metadata,
		Spec:               spec,
		CAFile:             caFile,
		InsecureSkipVerify: insecure,
		CallbackPort:       callbackPort,
		ApiServerURL:       apiServerURL,
		Username:           username,
		Password:           password,
		Web:                web,
	}
}

func (o *AAPOAuth) SetInsecureSkipVerify(insecureSkipVerify bool) {
	o.InsecureSkipVerify = insecureSkipVerify
}

func (o *AAPOAuth) getOAuth2Client(callback string) (*osincli.Client, error) {
	codeVerifier, codeChallenge, err := generatePKCEVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE parameters: %w", err)
	}

	// Use the API server's token proxy endpoint instead of the AAP provider's token endpoint
	if o.Metadata.Name == nil {
		return nil, fmt.Errorf("provider name is required")
	}
	tokenProxyURL := getTokenProxyURL(o.ApiServerURL, *o.Metadata.Name)

	config := &osincli.ClientConfig{
		ClientId:                 o.Spec.ClientId,
		ClientSecret:             "fake-secret",
		AuthorizeUrl:             o.Spec.AuthorizationUrl,
		TokenUrl:                 tokenProxyURL,
		ErrorsInStatusCode:       true,
		SendClientSecretInParams: true, // this makes sure we send the client id , the secret is not filled
		RedirectUrl:              callback,
		Scope:                    strings.Join(o.Spec.Scopes, " "),
		CodeVerifier:             codeVerifier,
		CodeChallenge:            codeChallenge,
		CodeChallengeMethod:      "S256",
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

func (o *AAPOAuth) Auth() (AuthInfo, error) {
	authInfo, err := oauth2AuthCodeFlow(o.getOAuth2Client, o.CallbackPort)
	if err != nil {
		return AuthInfo{}, err
	}
	authInfo.TokenToUse = TokenToUseAccessToken
	// AAP returns expires_in in nanoseconds, convert to seconds
	if authInfo.ExpiresIn != nil {
		expiresInSeconds := *authInfo.ExpiresIn / 1_000_000_000
		authInfo.ExpiresIn = &expiresInSeconds
	}
	return authInfo, nil
}

func (o *AAPOAuth) Renew(refreshToken string) (AuthInfo, error) {
	authInfo, err := oauth2RefreshTokenFlow(refreshToken, o.getOAuth2Client)
	if err != nil {
		return authInfo, err
	}
	// AAP returns expires_in in nanoseconds, convert to seconds
	if authInfo.ExpiresIn != nil {
		expiresInSeconds := *authInfo.ExpiresIn / 1_000_000_000
		authInfo.ExpiresIn = &expiresInSeconds
	}
	return authInfo, nil
}

func (o *AAPOAuth) Validate(args ValidateArgs) error {
	if o.Metadata.Name == nil || *o.Metadata.Name == "" {
		return fmt.Errorf("AAP auth: missing Metadata.Name")
	}

	if o.Spec.ClientId == "" {
		return fmt.Errorf("AAP auth: missing Spec.ClientId")
	}

	if o.Spec.AuthorizationUrl == "" {
		return fmt.Errorf("AAP auth: missing Spec.AuthorizationUrl")
	}

	if o.Spec.TokenUrl == "" {
		return fmt.Errorf("AAP auth: missing Spec.TokenUrl")
	}

	if len(o.Spec.Scopes) == 0 {
		return fmt.Errorf("AAP auth: missing Spec.Scopes")
	}

	if !o.Web && (o.Username == "" || o.Password == "") {
		return fmt.Errorf("username and password are required for password flow (use --web flag for web-based authentication)")
	}

	return nil
}
