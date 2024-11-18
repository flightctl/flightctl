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
	"github.com/lestrrat-go/jwx/v2/jwt"
)

const (
	AAP_GATEWAY_AUTH_HEADER = "X-Dab-Jw-Token"
)

type AapGatewayAuth struct {
	GatewayUrl      string
	PublicKey       any
	ClientTlsConfig *tls.Config
}

func NewAapGatewayAuth(gatewayUrl string, clientTlsConfig *tls.Config) (AapGatewayAuth, error) {
	aapGatewayAuth := AapGatewayAuth{
		GatewayUrl:      gatewayUrl,
		ClientTlsConfig: clientTlsConfig,
	}
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: clientTlsConfig,
	}}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/jwt_key/", gatewayUrl), nil)
	if err != nil {
		return aapGatewayAuth, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return aapGatewayAuth, err
	}

	defer resp.Body.Close()
	publicKeyPem, err := io.ReadAll(resp.Body)
	if err != nil {
		return aapGatewayAuth, err
	}

	block, _ := pem.Decode(publicKeyPem)
	if block == nil || block.Type != "PUBLIC KEY" {
		return aapGatewayAuth, fmt.Errorf("error decoding public key")
	}

	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return aapGatewayAuth, fmt.Errorf("error parsing public key: %w", err)
	}

	aapGatewayAuth.PublicKey = publicKey
	return aapGatewayAuth, nil
}

func (a AapGatewayAuth) ValidateToken(ctx context.Context, token string) (bool, error) {
	_, err := jwt.Parse(
		[]byte(token),
		jwt.WithKey(jwa.RS256, a.PublicKey),
		jwt.WithIssuer("ansible-issuer"),
		jwt.WithAudience("ansible-services"),
	)
	return err == nil, err
}

func (a AapGatewayAuth) GetAuthConfig() common.AuthConfig {
	return common.AuthConfig{
		Type: "AAPGateway",
		Url:  a.GatewayUrl,
	}
}

func (AapGatewayAuth) GetAuthToken(r *http.Request) (string, bool) {
	jwtHeader := r.Header.Get(AAP_GATEWAY_AUTH_HEADER)
	return jwtHeader, jwtHeader != ""
}
