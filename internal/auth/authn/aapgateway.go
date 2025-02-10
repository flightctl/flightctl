package authn

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jws"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

const (
	AAP_GATEWAY_AUTH_HEADER = "X-Dab-Jw-Token"
	AAP_ISSUER              = "ansible-issuer"
	AAP_AUDIENCE            = "ansible-services"
)

type AapGatewayAuth struct {
	gatewayUrl         string
	externalGatewayUrl string
	publicKey          any
	clientTlsConfig    *tls.Config
}

func NewAapGatewayAuth(gatewayUrl string, externalGatewayUrl string, clientTlsConfig *tls.Config) (AapGatewayAuth, error) {
	aapGatewayAuth := AapGatewayAuth{
		gatewayUrl:         gatewayUrl,
		externalGatewayUrl: externalGatewayUrl,
		clientTlsConfig:    clientTlsConfig,
	}
	publicKey, err := aapGatewayAuth.fetchPublicKey()
	aapGatewayAuth.publicKey = publicKey
	return aapGatewayAuth, err
}

func (a AapGatewayAuth) fetchPublicKey() (any, error) {
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/jwt_key/", a.gatewayUrl), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: a.clientTlsConfig,
	}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWT Public key: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected code %d when fetching public key: %w", resp.StatusCode, err)
	}

	defer resp.Body.Close()
	publicKeyPem, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	block, _ := pem.Decode(publicKeyPem)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("error decoding public key")
	}

	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("error parsing public key: %w", err)
	}
	return publicKey, nil
}

func (a *AapGatewayAuth) verifyJWS(token string) error {
	_, err := jws.Verify([]byte(token), jws.WithKey(jwa.RS256, a.publicKey))
	if err != nil {
		if jws.IsVerificationError(err) {
			// refetch public key and try again
			publicKey, err := a.fetchPublicKey()
			if err != nil {
				return fmt.Errorf("failed to fetch new public key: %w", err)
			}
			a.publicKey = publicKey
			_, err = jws.Verify([]byte(token), jws.WithKey(jwa.RS256, a.publicKey))
			if err != nil {
				return fmt.Errorf("failed to verify JWS: %w", err)
			}
			return nil
		}
		return fmt.Errorf("failed to verify JWS: %w", err)
	}
	return nil
}

func (a AapGatewayAuth) ValidateToken(ctx context.Context, token string) error {
	if err := a.verifyJWS(token); err != nil {
		return err
	}
	_, err := jwt.Parse(
		[]byte(token),
		jwt.WithVerify(false),
		jwt.WithValidate(true),
		jwt.WithIssuer(AAP_ISSUER),
		jwt.WithAudience(AAP_AUDIENCE),
	)
	if err != nil {
		return fmt.Errorf("failed to parse JWT token: %w", err)
	}
	return nil
}

func (a AapGatewayAuth) GetAuthConfig() common.AuthConfig {
	return common.AuthConfig{
		Type: common.AuthTypeAAP,
		Url:  a.externalGatewayUrl,
	}
}

func (AapGatewayAuth) GetAuthToken(r *http.Request) (string, error) {
	jwtHeader := r.Header.Get(AAP_GATEWAY_AUTH_HEADER)
	if jwtHeader == "" {
		return "", fmt.Errorf("empty %s header", AAP_GATEWAY_AUTH_HEADER)
	}
	return jwtHeader, nil
}

func (a AapGatewayAuth) GetIdentity(ctx context.Context, token string) (*common.Identity, error) {
	jwtToken, err := jwt.ParseInsecure([]byte(token))
	if err != nil {
		return nil, fmt.Errorf("failed to parse JWT token: %w", err)
	}

	userDataField, found := jwtToken.Get("user_data")
	if !found {
		return nil, fmt.Errorf("user_data field not found in JWT token")
	}
	userData, ok := userDataField.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected user_data format")
	}

	username, err := getValueAsString(userData, "username")
	if err != nil {
		return nil, fmt.Errorf("failed to get user identity: %w", err)
	}

	return &common.Identity{
		Username: username,
	}, nil
}

func getValueAsString(data map[string]interface{}, key string) (string, error) {
	val, ok := data[key]
	if !ok {
		return "", fmt.Errorf("'%s' key not found", key)
	}
	valStr, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("unexpected type of '%s'", key)
	}
	return valStr, nil
}
