package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
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
	GetAuthConfig() common.AuthConfig
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
	} else if cfg.Auth != nil {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: cfg.Auth.InsecureSkipTlsVerify, //nolint:gosec
		}
		if cfg.Auth.CACert != "" {
			caCertPool := x509.NewCertPool()
			caCertPool.AppendCertsFromPEM([]byte(cfg.Auth.CACert))
			tlsConfig.RootCAs = caCertPool
		}
		if cfg.Auth.OpenShiftApiUrl != "" {
			apiUrl := strings.TrimSuffix(cfg.Auth.OpenShiftApiUrl, "/")
			log.Println(fmt.Sprintf("OpenShift auth enabled: %s", apiUrl))
			authZ = K8sToK8sAuth{K8sAuthZ: authz.K8sAuthZ{ApiUrl: apiUrl, ClientTlsConfig: tlsConfig}}
			authN = authn.OpenShiftAuthN{OpenShiftApiUrl: apiUrl, ClientTlsConfig: tlsConfig}
		} else if cfg.Auth.OIDCAuthority != "" {
			oidcUrl := strings.TrimSuffix(cfg.Auth.OIDCAuthority, "/")
			internalOidcUrl := strings.TrimSuffix(cfg.Auth.InternalOIDCAuthority, "/")
			log.Println(fmt.Sprintf("OIDC auth enabled: %s", oidcUrl))
			authZ = NilAuth{}
			var err error
			authN, err = authn.NewJWTAuth(oidcUrl, internalOidcUrl, tlsConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to create JWT AuthN: %w", err)
			}
		}
	}

	if authN == nil {
		return nil, errors.New("no authN provider defined")
	}
	if authZ == nil {
		return nil, errors.New("no authZ provider defined")
	}

	handler := func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/auth/config" || r.URL.Path == "/api/v1/auth/validate" {
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
