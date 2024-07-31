package authn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

type JWTAuth struct {
	JwksUrl          string
	OidcDiscoveryUrl string
}

func (j JWTAuth) ValidateToken(ctx context.Context, token string) (bool, error) {
	jwkSet, err := jwk.Fetch(ctx, j.JwksUrl)
	if err != nil {
		return false, err
	}
	_, err = jwt.Parse([]byte(token), jwt.WithKeySet(jwkSet), jwt.WithValidate(true))
	if err != nil {
		return false, err
	}

	return true, nil
}

func (j JWTAuth) GetTokenRequestURL(ctx context.Context) (string, error) {
	res, err := http.Get(j.OidcDiscoveryUrl)
	if err != nil {
		return fmt.Sprintf("failed to fetch odic url: %v", j.OidcDiscoveryUrl), err
	}
	oauthResponse := OauthServerResponse{}
	defer res.Body.Close()
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Sprintf("failed to read response from the odic url: %v", j.OidcDiscoveryUrl), err
	}
	if err := json.Unmarshal(bodyBytes, &oauthResponse); err != nil {
		return fmt.Sprintf("failed parse the json response from the odic server: %v", j.OidcDiscoveryUrl), err
	}
	return fmt.Sprintf("%s/request", oauthResponse.TokenEndpoint), nil
}
