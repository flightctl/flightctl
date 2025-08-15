package authn

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

type TokenIdentity interface {
	common.Identity
	GetClaim(string) (interface{}, bool)
}

// JWTIdentity extends common.Identity with JWT-specific fields
type JWTIdentity struct {
	common.BaseIdentity
	parsedToken jwt.Token
}

// Ensure JWTIdentity implements TokenIdentity
var _ TokenIdentity = (*JWTIdentity)(nil)

func (i *JWTIdentity) GetClaim(claim string) (interface{}, bool) {
	return i.parsedToken.Get(claim)
}

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
	_, err := j.parseAndCreateIdentity(ctx, token)
	return err
}

func (j JWTAuth) parseAndCreateIdentity(ctx context.Context, token string) (*JWTIdentity, error) {
	// Check if we already have a parsed identity in the context
	if existingIdentity, ok := ctx.Value(consts.IdentityCtxKey).(*JWTIdentity); ok {
		return existingIdentity, nil
	}

	jwkSet, err := jwk.Fetch(ctx, j.jwksUri, jwk.WithHTTPClient(j.client))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWK set: %w", err)
	}

	parsedToken, err := jwt.Parse([]byte(token), jwt.WithKeySet(jwkSet), jwt.WithValidate(true))
	if err != nil {
		return nil, fmt.Errorf("failed to parse JWT token: %w", err)
	}

	identity := &JWTIdentity{
		BaseIdentity: common.BaseIdentity{},
	}
	identity.parsedToken = parsedToken

	if sub, exists := parsedToken.Get("sub"); exists {
		if uid, ok := sub.(string); ok {
			identity.SetUID(uid)
		}
	}

	if preferredUsername, exists := parsedToken.Get("preferred_username"); exists {
		if username, ok := preferredUsername.(string); ok {
			identity.SetUsername(username)
		}
	}

	return identity, nil
}

func (j JWTAuth) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	identity, err := j.parseAndCreateIdentity(ctx, token)
	if err != nil {
		return nil, err
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
