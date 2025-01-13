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
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/sirupsen/logrus"
)

const (
	// DisableAuthEnvKey is the environment variable key used to disable auth when developing.
	DisableAuthEnvKey = "FLIGHTCTL_DISABLE_AUTH"
	k8sCACertPath     = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	k8sApiService     = "https://kubernetes.default.svc"
)

type AuthNMiddleware interface {
	ValidateToken(ctx context.Context, token string) (bool, error)
	GetIdentity(ctx context.Context, token string) (*common.Identity, error)
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

func initK8sAuth(cfg *config.Config, log logrus.FieldLogger) error {
	apiUrl := strings.TrimSuffix(cfg.Auth.K8s.ApiUrl, "/")
	externalOpenShiftApiUrl := strings.TrimSuffix(cfg.Auth.K8s.ExternalOpenShiftApiUrl, "/")
	log.Infof("k8s auth enabled: %s", apiUrl)
	var k8sClient k8sclient.K8SClient
	var err error
	if apiUrl == k8sApiService {
		k8sClient, err = k8sclient.NewK8SClient()
	} else {
		k8sClient, err = k8sclient.NewK8SExternalClient(apiUrl, cfg.Auth.InsecureSkipTlsVerify, cfg.Auth.CACert)
	}
	if err != nil {
		return fmt.Errorf("failed to create k8s client: %w", err)
	}
	authZ = K8sToK8sAuth{K8sAuthZ: authz.K8sAuthZ{K8sClient: k8sClient, Namespace: cfg.Auth.K8s.RBACNs}}
	authN, err = authn.NewK8sAuthN(k8sClient, externalOpenShiftApiUrl)
	if err != nil {
		return fmt.Errorf("failed to create k8s AuthN: %w", err)
	}
	return nil
}

func initOIDCAuth(cfg *config.Config, log logrus.FieldLogger) error {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: cfg.Auth.InsecureSkipTlsVerify, //nolint:gosec
	}
	if cfg.Auth.CACert != "" {
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM([]byte(cfg.Auth.CACert))
		tlsConfig.RootCAs = caCertPool
	}
	oidcUrl := strings.TrimSuffix(cfg.Auth.OIDC.OIDCAuthority, "/")
	externalOidcUrl := strings.TrimSuffix(cfg.Auth.OIDC.ExternalOIDCAuthority, "/")
	log.Infof("OIDC auth enabled: %s", oidcUrl)
	authZ = NilAuth{}
	var err error
	authN, err = authn.NewJWTAuth(oidcUrl, externalOidcUrl, tlsConfig)
	if err != nil {
		return fmt.Errorf("failed to create OIDC AuthN: %w", err)
	}
	return nil
}

func CreateAuthMiddleware(cfg *config.Config, log logrus.FieldLogger) (func(http.Handler) http.Handler, error) {
	value, exists := os.LookupEnv(DisableAuthEnvKey)
	if exists && value != "" {
		log.Warnln("Auth disabled")
		authZ = NilAuth{}
		authN = authZ.(AuthNMiddleware)
	} else if cfg.Auth != nil {
		var err error
		if cfg.Auth.K8s != nil {
			err = initK8sAuth(cfg, log)
		} else if cfg.Auth.OIDC != nil {
			err = initOIDCAuth(cfg, log)
		}

		if err != nil {
			return nil, err
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
			identity, err := authN.GetIdentity(ctx, authToken)
			if err != nil {
				log.WithError(err).Error("failed to get identity")
			} else {
				ctx = context.WithValue(ctx, common.IdentityCtxKey, identity)
			}
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
