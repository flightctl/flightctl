package authn

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

type JWTAuth struct {
	oidcAuthority         string
	externalOIDCAuthority string
	jwksUri               string
	clientTlsConfig       *tls.Config
}

type OIDCServerResponse struct {
	TokenEndpoint string `json:"token_endpoint"`
	JwksUri       string `json:"jwks_uri"`
}

func NewJWTAuth(oidcAuthority string, externalOIDCAuthority string, clientTlsConfig *tls.Config) (JWTAuth, error) {
	jwtAuth := JWTAuth{
		oidcAuthority:         oidcAuthority,
		externalOIDCAuthority: externalOIDCAuthority,
		clientTlsConfig:       clientTlsConfig,
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: clientTlsConfig,
		},
	}

	res, err := client.Get(fmt.Sprintf("%s/.well-known/openid-configuration", oidcAuthority))
	if err != nil {
		return jwtAuth, err
	}
	oidcResponse := OIDCServerResponse{}
	defer res.Body.Close()
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return jwtAuth, err
	}
	if err := json.Unmarshal(bodyBytes, &oidcResponse); err != nil {
		return jwtAuth, err
	}
	jwtAuth.jwksUri = oidcResponse.JwksUri
	return jwtAuth, nil
}

func (j JWTAuth) ValidateToken(ctx context.Context, token string) (bool, error) {
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: j.clientTlsConfig,
	}}
	jwkSet, err := jwk.Fetch(ctx, j.jwksUri, jwk.WithHTTPClient(client))
	if err != nil {
		return false, err
	}
	_, err = jwt.Parse([]byte(token), jwt.WithKeySet(jwkSet), jwt.WithValidate(true))
	if err != nil {
		return false, err
	}

	return true, nil
}

func (j JWTAuth) GetIdentity(ctx context.Context, token string) (*common.Identity, error) {
	// TODO return filled identity information
	return &common.Identity{}, nil
}

func (j JWTAuth) GetAuthConfig() common.AuthConfig {
	return common.AuthConfig{
		Type: "OIDC",
		Url:  j.externalOIDCAuthority,
	}
}
