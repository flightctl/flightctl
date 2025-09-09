package authn

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

type JWTAuth struct {
	oidcAuthority         string
	externalOIDCAuthority string
	jwksUri               string
	clientTlsConfig       *tls.Config
	client                *http.Client
	orgConfig             *common.AuthOrganizationsConfig
}

type OIDCServerResponse struct {
	TokenEndpoint string `json:"token_endpoint"`
	JwksUri       string `json:"jwks_uri"`
}

func NewJWTAuth(oidcAuthority string, externalOIDCAuthority string, clientTlsConfig *tls.Config, orgConfig *common.AuthOrganizationsConfig) (JWTAuth, error) {
	jwtAuth := JWTAuth{
		oidcAuthority:         oidcAuthority,
		externalOIDCAuthority: externalOIDCAuthority,
		clientTlsConfig:       clientTlsConfig,
		client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: clientTlsConfig,
			},
		},
		orgConfig: orgConfig,
	}

	res, err := jwtAuth.client.Get(fmt.Sprintf("%s/.well-known/openid-configuration", oidcAuthority))
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

func (j JWTAuth) ValidateToken(ctx context.Context, token string) error {
	jwkSet, err := jwk.Fetch(ctx, j.jwksUri, jwk.WithHTTPClient(j.client))
	if err != nil {
		return err
	}
	_, err = jwt.Parse([]byte(token), jwt.WithKeySet(jwkSet), jwt.WithValidate(true))
	return err
}

func (j JWTAuth) GetIdentity(ctx context.Context, token string) (*common.Identity, error) {
	// Validate input
	if token == "" || len(strings.TrimSpace(token)) == 0 {
		return nil, fmt.Errorf("empty token provided")
	}

	// Parse the JWT token to extract claims
	parsedToken, err := jwt.Parse([]byte(token), jwt.WithVerify(false)) // Skip verification as it's done elsewhere
	if err != nil {
		return nil, fmt.Errorf("failed to parse JWT token: %w", err)
	}

	// Extract claims
	claims, err := parsedToken.AsMap(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to extract claims from JWT token: %w", err)
	}

	// Build Identity from claims using common utilities
	identity := &common.Identity{
		Username: common.ExtractUsernameFromJWTClaims(claims),
		UID:      common.ExtractUIDFromJWTClaims(claims),
		Groups:   common.ExtractGroupsFromJWTClaims(claims),
	}

	// Ensure we have at least a username (fallback to UID if needed)
	if identity.Username == "" && identity.UID != "" {
		identity.Username = identity.UID
	}

	// Validate that we have some form of identity
	if identity.Username == "" {
		return nil, fmt.Errorf("no valid identity claims found in JWT token")
	}

	return identity, nil
}

func (j JWTAuth) GetAuthConfig() common.AuthConfig {
	orgConfig := common.AuthOrganizationsConfig{}
	if j.orgConfig != nil {
		orgConfig = *j.orgConfig
	}

	return common.AuthConfig{
		Type:                common.AuthTypeOIDC,
		Url:                 j.externalOIDCAuthority,
		OrganizationsConfig: orgConfig,
	}
}

func (j JWTAuth) GetAuthToken(r *http.Request) (string, error) {
	return common.ExtractBearerToken(r)
}
