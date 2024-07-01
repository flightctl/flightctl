package authn

import (
	"context"
	"net/http"
	"strings"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

type tokenKeyType string

const (
	AuthHeaderKey              = "Authorization"
	BearerScheme               = "Bearer "
	tokenKeyValue tokenKeyType = "token"
)

type JWTAuth struct {
	JwksUrl string
}

func (j JWTAuth) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	//TODO implement me
	panic("implement me")
}

func (j JWTAuth) AuthHandler(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get(AuthHeaderKey)
		if authHeader == "" || !strings.HasPrefix(authHeader, BearerScheme) {
			http.Error(w, "missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}

		tokenStr := strings.TrimPrefix(authHeader, BearerScheme)

		jwkSet, err := jwk.Fetch(r.Context(), j.JwksUrl)
		if err != nil {
			http.Error(w, "failed to fetch JWK set", http.StatusInternalServerError)
			return
		}

		token, err := jwt.Parse([]byte(tokenStr), jwt.WithKeySet(jwkSet), jwt.WithValidate(true))
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), tokenKeyValue, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
	return http.HandlerFunc(fn)
}
