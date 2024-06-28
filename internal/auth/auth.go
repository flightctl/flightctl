package auth

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/auth/authz"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
)

const (
	// DisableAuthEnvKey is the environment variable key used to disable auth when developing.
	DisableAuthEnvKey = "FLIGHTCTL_DISABLE_AUTH"
)

type AuthNMiddleware interface {
	AuthHandler(next http.Handler) http.Handler
}

type AuthZMiddleware interface {
	CheckPermission(ctx context.Context, resource string, op string) (bool, error)
}

var auth AuthZMiddleware

func GetAuth() AuthZMiddleware {
	return auth
}

func CreateAuthMiddleware(cfg *config.Config, log logrus.FieldLogger) (AuthNMiddleware, error) {
	value, exists := os.LookupEnv(DisableAuthEnvKey)
	if exists && value != "" {
		log.Warnln("Auth disabled")
		auth = NilAuth{}
		return auth.(AuthNMiddleware), nil
	}

	if cfg.Auth != nil && cfg.Auth.K8sApiUrl != "" {
		log.Println("k8s auth enabled")
		auth = K8sToK8sAuth{K8sAuthZ: authz.K8sAuthZ{ApiUrl: cfg.Auth.K8sApiUrl}}
		return authn.K8sAuthN{}, nil
	}

	return nil, errors.New("no auth provider defined")
}

type K8sToK8sAuth struct {
	authz.K8sAuthZ
}

func (o K8sToK8sAuth) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	k8sTokenVal := ctx.Value(authn.K8sTokenKey)
	if k8sTokenVal == nil {
		return false, nil
	}
	k8sToken := k8sTokenVal.(string)
	return o.K8sAuthZ.CheckPermission(ctx, k8sToken, resource, op)
}
