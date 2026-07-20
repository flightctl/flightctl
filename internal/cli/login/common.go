package login

import (
	"crypto/tls"
	"fmt"
	"net/url"
	"os"
	"strings"

	certutil "k8s.io/client-go/util/cert"
)

// OIDCDiscoveryResponse represents the OpenID Connect Discovery metadata
// as defined in the Flight Control PAM issuer OpenAPI spec
type OIDCDiscoveryResponse struct {
	// Required fields
	Issuer                           string   `json:"issuer"`
	AuthorizationEndpoint            string   `json:"authorization_endpoint"`
	TokenEndpoint                    string   `json:"token_endpoint"`
	JwksUri                          string   `json:"jwks_uri"`
	SubjectTypesSupported            []string `json:"subject_types_supported"`
	ResponseTypesSupported           []string `json:"response_types_supported"`
	IdTokenSigningAlgValuesSupported []string `json:"id_token_signing_alg_values_supported"`

	// Optional fields
	UserinfoEndpoint                  string   `json:"userinfo_endpoint,omitempty"`
	GrantTypesSupported               []string `json:"grant_types_supported,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	ClaimsSupported                   []string `json:"claims_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
}

func getAuthClientTlsConfig(authCAFile string, insecureSkipVerify bool) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: insecureSkipVerify, //nolint:gosec
	}

	if authCAFile != "" {
		caData, err := os.ReadFile(authCAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read Auth CA file: %w", err)
		}
		caPool, err := certutil.NewPoolFromBytes(caData)
		if err != nil {
			return nil, fmt.Errorf("failed parsing Auth CA certs: %w", err)
		}

		tlsConfig.RootCAs = caPool
	}

	return tlsConfig, nil
}

type TokenToUseType string

const (
	TokenToUseAccessToken TokenToUseType = "access"
	TokenToUseIdToken     TokenToUseType = "id"
)

type AuthInfo struct {
	AccessToken  string
	RefreshToken string
	IdToken      string
	TokenToUse   TokenToUseType
	ExpiresIn    *int64
}

type ValidateArgs struct {
	ApiUrl      string
	AccessToken string
}

// getTokenProxyURL returns the Flight Control API server's token proxy endpoint
// for the given provider name. Returns an error if apiServerURL is not a valid URL.
func getTokenProxyURL(apiServerURL, providerName string) (string, error) {
	u, err := url.Parse(apiServerURL)
	if err != nil {
		return "", fmt.Errorf("invalid API server URL %q: %w", apiServerURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid API server URL %q: missing scheme or host", apiServerURL)
	}
	return u.JoinPath("api", "v1", "auth", providerName, "token").String(), nil
}

type AuthProvider interface {
	Auth() (AuthInfo, error)
	Renew(refreshToken string) (AuthInfo, error)
	Validate(args ValidateArgs) error
	SetInsecureSkipVerify(insecureSkipVerify bool)
}

func StrIsEmpty(str string) bool {
	return len(strings.TrimSpace(str)) == 0
}
