package login

import (
	"fmt"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/openshift/osincli"
)

type OAuth2 struct {
	Metadata           api.ObjectMeta
	Spec               api.OAuth2ProviderSpec
	CAFile             string
	InsecureSkipVerify bool
	ApiServerURL       string
	CallbackPort       int
	Username           string
	Password           string
	Web                bool
}

func NewOAuth2Config(metadata api.ObjectMeta, spec api.OAuth2ProviderSpec, caFile string, insecure bool, apiServerURL string, callbackPort int, username, password string, web bool) *OAuth2 {
	return &OAuth2{
		Metadata:           metadata,
		Spec:               spec,
		CAFile:             caFile,
		InsecureSkipVerify: insecure,
		ApiServerURL:       apiServerURL,
		CallbackPort:       callbackPort,
		Username:           username,
		Password:           password,
		Web:                web,
	}
}

func (o *OAuth2) SetInsecureSkipVerify(insecureSkipVerify bool) {
	o.InsecureSkipVerify = insecureSkipVerify
}

func (o *OAuth2) getOAuth2Client(callback string) (*osincli.Client, error) {
	codeVerifier, codeChallenge, err := generatePKCEVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE parameters: %w", err)
	}

	// Use the API server's token proxy endpoint instead of the OAuth2 provider's token endpoint
	if o.Metadata.Name == nil {
		return nil, fmt.Errorf("provider name is required")
	}
	tokenProxyURL := getTokenProxyURL(o.ApiServerURL, *o.Metadata.Name)

	config := &osincli.ClientConfig{
		ClientId:                 o.Spec.ClientId,
		AuthorizeUrl:             o.Spec.AuthorizationUrl,
		TokenUrl:                 tokenProxyURL,
		ErrorsInStatusCode:       true,
		SendClientSecretInParams: true, // this makes sure we send the client id , the secret is not filled
		RedirectUrl:              callback,
		Scope:                    strings.Join(*o.Spec.Scopes, " "),
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

	client.Transport = &http.Transport{
		TLSClientConfig: tlsConfig,
	}

	return client, nil
}

func (o *OAuth2) Auth() (AuthInfo, error) {
	// Use password flow if username/password provided and web flag not set
	if o.Username != "" && o.Password != "" && !o.Web {
		return o.authPasswordFlow()
	}
	// Default to auth code flow
	authInfo, err := oauth2AuthCodeFlow(o.getOAuth2Client, o.CallbackPort)
	if err != nil {
		return AuthInfo{}, err
	}
	authInfo.TokenToUse = TokenToUseAccessToken
	return authInfo, nil
}

func (o *OAuth2) authPasswordFlow() (AuthInfo, error) {
	if o.Metadata.Name == nil {
		return AuthInfo{}, fmt.Errorf("provider name is required")
	}

	authInfo, err := oauth2PasswordFlow(o.Spec.TokenUrl, o.Spec.ClientId, o.Username, o.Password, strings.Join(*o.Spec.Scopes, " "), o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return AuthInfo{}, err
	}
	authInfo.TokenToUse = TokenToUseAccessToken
	return authInfo, nil
}

func (o *OAuth2) Renew(refreshToken string) (AuthInfo, error) {
	return oauth2RefreshTokenFlow(refreshToken, o.getOAuth2Client)
}

func (o *OAuth2) Validate(args ValidateArgs) error {
	if o.Metadata.Name == nil {
		return fmt.Errorf("provider name is required")
	}
	if o.ApiServerURL == "" {
		return fmt.Errorf("API server URL is required")
	}
	if o.Spec.ClientId == "" {
		return fmt.Errorf("client ID is required")
	}
	if o.Spec.AuthorizationUrl == "" {
		return fmt.Errorf("authorization URL is required")
	}
	if o.Spec.Scopes == nil {
		return fmt.Errorf("scopes are required")
	}
	if !o.Web && (o.Username == "" || o.Password == "") {
		return fmt.Errorf("username and password are required for password flow (use --web flag for web-based authentication)")
	}
	return nil
}
