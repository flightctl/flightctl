package login

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/openshift/osincli"
)

type OIDCDirectResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    *int64 `json:"expires_in"` // ExpiresIn in seconds
}

type OIDC struct {
	Metadata           api.ObjectMeta
	Spec               api.OIDCProviderSpec
	CAFile             string
	InsecureSkipVerify bool
	ApiServerURL       string
	CallbackPort       int
	Username           string
	Password           string
	Web                bool
}

func NewOIDCConfig(metadata api.ObjectMeta, spec api.OIDCProviderSpec, caFile string, insecure bool, apiServerURL string, callbackPort int, username, password string, web bool) *OIDC {
	return &OIDC{
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

func (o *OIDC) SetInsecureSkipVerify(insecureSkipVerify bool) {
	o.InsecureSkipVerify = insecureSkipVerify
}

func (o *OIDC) getOIDCClient(callback string) (*osincli.Client, error) {
	discoveryUrl := fmt.Sprintf("%s/.well-known/openid-configuration", o.Spec.Issuer)
	oidcDiscovery, err := getOIDCDiscoveryConfig(discoveryUrl, o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}
	codeVerifier, codeChallenge, err := generatePKCEVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE parameters: %w", err)
	}

	// Use the API server's token proxy endpoint instead of the OIDC provider's token endpoint
	if o.Metadata.Name == nil {
		return nil, fmt.Errorf("provider name is required")
	}
	tokenProxyURL := getTokenProxyURL(o.ApiServerURL, *o.Metadata.Name)

	config := &osincli.ClientConfig{
		ClientId:                 o.Spec.ClientId,
		AuthorizeUrl:             oidcDiscovery.AuthorizationEndpoint,
		TokenUrl:                 tokenProxyURL,
		ErrorsInStatusCode:       true,
		RedirectUrl:              callback,
		Scope:                    strings.Join(*o.Spec.Scopes, " "),
		CodeVerifier:             codeVerifier,
		CodeChallenge:            codeChallenge,
		CodeChallengeMethod:      "S256",
		SendClientSecretInParams: true, // this makes sure we send the client id , the secret is not filled
	}

	client, err := osincli.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create oidc client: %w", err)
	}

	tlsConfig, err := getAuthClientTlsConfig(o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}

	client.Transport = &http.Transport{TLSClientConfig: tlsConfig}

	return client, nil
}

func (o *OIDC) Renew(refreshToken string) (AuthInfo, error) {
	return oauth2RefreshTokenFlow(refreshToken, o.getOIDCClient)
}

func (o *OIDC) Auth() (AuthInfo, error) {
	// Use password flow if username/password provided and web flag not set
	if o.Username != "" && o.Password != "" && !o.Web {
		return o.authPasswordFlow()
	}
	// Default to auth code flow
	authInfo, err := oauth2AuthCodeFlow(o.getOIDCClient, o.CallbackPort)
	if err != nil {
		return AuthInfo{}, err
	}
	if o.Spec.Scopes != nil && slices.Contains(*o.Spec.Scopes, "openid") {
		authInfo.TokenToUse = TokenToUseIdToken
	}
	return authInfo, nil
}

func (o *OIDC) authPasswordFlow() (AuthInfo, error) {
	discoveryUrl := fmt.Sprintf("%s/.well-known/openid-configuration", o.Spec.Issuer)
	oidcDiscovery, err := getOIDCDiscoveryConfig(discoveryUrl, o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return AuthInfo{}, err
	}

	authInfo, err := oauth2PasswordFlow(oidcDiscovery.TokenEndpoint, o.Spec.ClientId, o.Username, o.Password, strings.Join(*o.Spec.Scopes, " "), o.CAFile, o.InsecureSkipVerify)
	if err != nil {
		return AuthInfo{}, err
	}
	if o.Spec.Scopes != nil && slices.Contains(*o.Spec.Scopes, "openid") {
		authInfo.TokenToUse = TokenToUseIdToken
	}
	return authInfo, nil
}

func (o *OIDC) Validate(args ValidateArgs) error {
	if o.Metadata.Name == nil {
		return fmt.Errorf("provider name is required")
	}
	if o.ApiServerURL == "" {
		return fmt.Errorf("API server URL is required")
	}
	if o.Spec.Issuer == "" {
		return fmt.Errorf("issuer URL is required")
	}
	if o.Spec.ClientId == "" {
		return fmt.Errorf("client ID is required")
	}
	if o.Spec.Scopes == nil {
		return fmt.Errorf("scopes are required")
	}
	if !o.Web && (o.Username == "" || o.Password == "") {
		return fmt.Errorf("username and password are required for password flow (use --web flag for web-based authentication)")
	}
	return nil
}
