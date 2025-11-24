package login

import (
	"fmt"
	"net/http"
	"net/url"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/openshift/osincli"
)

type AAPOAuth struct {
	Metadata           api.ObjectMeta
	Spec               api.AapProviderSpec
	CAFile             string
	InsecureSkipVerify bool
	ClientId           string
	ApiServerURL       string
	CallbackPort       int
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

func NewAAPOAuth2Config(metadata api.ObjectMeta, spec api.AapProviderSpec, clientId, caFile string, insecure bool, apiServerURL string, callbackPort int) *AAPOAuth {
	return &AAPOAuth{
		Metadata:           metadata,
		Spec:               spec,
		ClientId:           clientId,
		CAFile:             caFile,
		InsecureSkipVerify: insecure,
		ApiServerURL:       apiServerURL,
		CallbackPort:       callbackPort,
	}
}

func (o *AAPOAuth) SetInsecureSkipVerify(insecureSkipVerify bool) {
	o.InsecureSkipVerify = insecureSkipVerify
}

func (o *AAPOAuth) getOAuth2Client(callback string) (*osincli.Client, error) {
	authUrl := o.Spec.ApiUrl
	if o.Spec.ExternalApiUrl != nil {
		authUrl = *o.Spec.ExternalApiUrl
	}

	codeVerifier, codeChallenge, err := generatePKCEVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE parameters: %w", err)
	}

	// Use the API server's token proxy endpoint instead of the AAP provider's token endpoint
	if o.Metadata.Name == nil {
		return nil, fmt.Errorf("provider name is required")
	}

	config := &osincli.ClientConfig{
		ClientId:                 o.ClientId,
		AuthorizeUrl:             fmt.Sprintf("%s/o/authorize/", authUrl),
		TokenUrl:                 fmt.Sprintf("%s/o/token/", authUrl),
		ErrorsInStatusCode:       true,
		SendClientSecretInParams: true, // this makes sure we send the client id , the secret is not filled
		RedirectUrl:              callback,
		Scope:                    "read",
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
	return authInfo, nil
}

func (o *AAPOAuth) Renew(refreshToken string) (AuthInfo, error) {
	return oauth2RefreshTokenFlow(refreshToken, o.getOAuth2Client)
}

func (o *AAPOAuth) Validate(args ValidateArgs) error {
	if o.Metadata.Name == nil || *o.Metadata.Name == "" {
		return fmt.Errorf("AAP auth: missing Metadata.Name")
	}

	if o.ClientId == "" {
		return fmt.Errorf("AAP auth: missing ClientId")
	}

	if o.Spec.ApiUrl == "" {
		return fmt.Errorf("AAP auth: missing Spec.ApiUrl")
	}

	if _, err := url.Parse(o.Spec.ApiUrl); err != nil {
		return fmt.Errorf("AAP auth: Spec.ApiUrl is invalid: %w", err)
	}

	if o.ApiServerURL == "" {
		return fmt.Errorf("AAP auth: missing ApiServerURL")
	}

	if _, err := url.Parse(o.ApiServerURL); err != nil {
		return fmt.Errorf("AAP auth: ApiServerURL is invalid: %w", err)
	}

	return nil
}
