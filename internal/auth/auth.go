package auth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/auth/authz"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
)

const (
	// DisableAuthEnvKey is the environment variable key used to disable auth when developing.
	DisableAuthEnvKey = "FLIGHTCTL_DISABLE_AUTH"
)

type AuthNMiddleware interface {
	ValidateToken(ctx context.Context, token string) (bool, error)
	GetTokenRequestURL(ctx context.Context) (string, error)
}

type AuthZMiddleware interface {
	CheckPermission(ctx context.Context, resource string, op string) (bool, error)
}

var authZ AuthZMiddleware
var authN AuthNMiddleware

func GetAuthZ() AuthZMiddleware {
	return authZ
}

func GetAuthN() AuthNMiddleware {
	return authN
}

func ParseAuthHeader(authHeader string) (string, bool) {
	authToken := strings.Split(authHeader, "Bearer ")
	if len(authToken) != 2 {
		return "", false
	}
	return authToken[1], true
}

func getAuthToken(r *http.Request) (string, bool) {
	if _, isAuthDisabled := authN.(NilAuth); isAuthDisabled {
		return "", true
	}
	authHeader := r.Header.Get(common.AuthHeader)
	if authHeader == "" {
		return "", false
	}
	return ParseAuthHeader(authHeader)
}

func CreateAuthMiddleware(cfg *config.Config, log logrus.FieldLogger) (func(http.Handler) http.Handler, error) {
	value, exists := os.LookupEnv(DisableAuthEnvKey)
	if exists && value != "" {
		log.Warnln("Auth disabled")
		authZ = NilAuth{}
		authN = authZ.(AuthNMiddleware)
	} else if cfg.Auth != nil && cfg.Auth.K8sApiUrl != "" {
		log.Println("k8s auth enabled")
		authZ = K8sToK8sAuth{K8sAuthZ: authz.K8sAuthZ{ApiUrl: cfg.Auth.K8sApiUrl}}
		authN = authn.OpenShiftAuthN{OpenShiftApiUrl: cfg.Auth.K8sApiUrl}
	}

	if authN == nil {
		return nil, errors.New("no auth provider defined")
	}

	handler := func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/token/request" || r.URL.Path == "/api/v1/token/validate" {
				next.ServeHTTP(w, r)
				return
			}
			authToken, ok := getAuthToken(r)
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			valid, err := authN.ValidateToken(r.Context(), authToken)
			if err != nil || !valid {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), common.TokenCtxKey, authToken)
			next.ServeHTTP(w, r.WithContext(ctx))
		}
		return http.HandlerFunc(fn)
	}
	return handler, nil
}

type K8sToK8sAuth struct {
	authz.K8sAuthZ
}

func (o K8sToK8sAuth) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	k8sTokenVal := ctx.Value(common.TokenCtxKey)
	if k8sTokenVal == nil {
		return false, nil
	}
	k8sToken := k8sTokenVal.(string)
	return o.K8sAuthZ.CheckPermission(ctx, k8sToken, resource, op)
}
