package client

import (
	"context"
	"fmt"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/cli/login"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
)

const (
	AuthUrlKey               = "server"
	AuthCAFileKey            = "certificate-authority"
	AuthRefreshTokenKey      = "refresh-token"
	AuthAccessTokenExpiryKey = "access-token-expiry"
	AuthClientIdKey          = "client-id"
)

// AccessTokenRefresher manages OAuth2/OIDC token refresh for a client configuration
type AccessTokenRefresher struct {
	config         *Config
	once           sync.Once
	provider       login.AuthProvider
	log            logrus.FieldLogger
	configFilePath string
	callbackPort   int
	cancel         context.CancelFunc
}

// NewAccessTokenRefresher creates a new AccessTokenRefresher instance
func NewAccessTokenRefresher(config *Config, configFilePath string, callbackPort int) *AccessTokenRefresher {
	return &AccessTokenRefresher{
		config:         config,
		configFilePath: configFilePath,
		callbackPort:   callbackPort,
	}
}

func CreateAuthProvider(authInfo AuthInfo, insecure bool, apiServerURL string, callbackPort int) (login.AuthProvider, error) {
	return CreateAuthProviderWithCredentials(authInfo, insecure, apiServerURL, callbackPort, "", "", false)
}

func CreateAuthProviderWithCredentials(authInfo AuthInfo, insecure bool, apiServerURL string, callbackPort int, username, password string, web bool) (login.AuthProvider, error) {
	if authInfo.AuthProvider == nil {
		return nil, fmt.Errorf("no auth provider defined (try logging in again)")
	}

	provider := &authInfo.AuthProvider.AuthProvider
	caFile := authInfo.AuthProvider.CAFile

	// Get the provider type from the spec
	providerType, err := provider.Spec.Discriminator()
	if err != nil {
		return nil, fmt.Errorf("failed to determine provider type: %w", err)
	}

	authInsecure := insecure || authInfo.AuthProvider.InsecureSkipVerify
	switch providerType {
	case string(api.Oidc):
		oidcSpec, err := provider.Spec.AsOIDCProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("failed to parse OIDC provider spec: %w", err)
		}
		return login.NewOIDCConfig(provider.Metadata, oidcSpec, caFile, authInsecure, apiServerURL, callbackPort, username, password, web), nil

	case string(api.Oauth2):
		oauth2Spec, err := provider.Spec.AsOAuth2ProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("failed to parse OAuth2 provider spec: %w", err)
		}
		return login.NewOAuth2Config(provider.Metadata, oauth2Spec, caFile, authInsecure, apiServerURL, callbackPort, username, password, web), nil

	case string(api.Openshift):
		openshiftSpec, err := provider.Spec.AsOpenShiftProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("failed to parse OpenShift provider spec: %w", err)
		}
		return login.NewOpenShiftConfig(provider.Metadata, openshiftSpec, caFile, authInsecure, apiServerURL, callbackPort, username, password, web), nil

	case string(api.Aap):
		aapSpec, err := provider.Spec.AsAapProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("failed to parse AAP provider spec: %w", err)
		}
		return login.NewAAPOAuth2Config(provider.Metadata, aapSpec, caFile, authInsecure, apiServerURL, callbackPort, username, password, web), nil

	case string(api.K8s):
		return nil, fmt.Errorf("k8s auth requires providing --token flag")
	default:
		return nil, fmt.Errorf("unsupported auth provider type: %s", providerType)
	}
}

func (r *AccessTokenRefresher) init() error {
	var err error
	r.provider, err = CreateAuthProvider(r.config.AuthInfo, r.config.Service.InsecureSkipVerify, r.config.Service.Server, r.callbackPort)
	return err
}

func (r *AccessTokenRefresher) parseExpireTime() (time.Time, error) {
	if r.config.AuthInfo.AccessTokenExpiry == "" {
		return time.Time{}, fmt.Errorf("no access token expiry found")
	}
	return time.Parse(time.RFC3339Nano, r.config.AuthInfo.AccessTokenExpiry)
}

func (r *AccessTokenRefresher) shouldRefresh(expireTime time.Time) bool {
	return time.Now().Add(5 * time.Second).After(expireTime)
}

func (r *AccessTokenRefresher) refresh() error {
	if r.config.AuthInfo.RefreshToken == "" {
		return fmt.Errorf("no refresh token found")
	}
	authInfo, err := r.provider.Renew(r.config.AuthInfo.RefreshToken)
	if err != nil {
		return fmt.Errorf("failed to renew token: %w", err)
	}
	if authInfo.RefreshToken != "" {
		r.config.AuthInfo.RefreshToken = authInfo.RefreshToken
	}
	r.config.AuthInfo.AccessToken = authInfo.AccessToken
	if authInfo.ExpiresIn != nil {
		expiryTime := time.Now().Add(time.Duration(*authInfo.ExpiresIn) * time.Second)
		r.config.AuthInfo.AccessTokenExpiry = expiryTime.Format(time.RFC3339Nano)
	}
	if authInfo.IdToken != "" {
		r.config.AuthInfo.IdToken = authInfo.IdToken
	}
	// Only persist if configFilePath is provided
	if r.configFilePath != "" {
		return r.config.Persist(r.configFilePath)
	}
	return nil
}

func (r *AccessTokenRefresher) waitDuration() time.Duration {
	waitDuration := time.Second
	if r.config.AuthInfo.AccessTokenExpiry != "" {
		expireTime, err := r.parseExpireTime()
		if err != nil {
			r.log.Errorf("failed to parse time %s: %v", r.config.AuthInfo.AccessTokenExpiry, err)
		} else {
			waitDuration = util.Max(time.Until(expireTime)-5*time.Second, time.Second)
		}
	}
	return waitDuration
}

func (r *AccessTokenRefresher) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(r.waitDuration())
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := r.refresh(); err != nil {
				r.log.Errorf("failed to renew token: %v", err)
				return
			}
			r.log.Info("renewed access token")
			ticker.Reset(r.waitDuration())
		case <-ctx.Done():
			return
		}
	}
}

// Start initializes and starts the token refresh loop if not already started.
// The provided context is used as the parent context for the refresh loop.
// When the context is cancelled, the refresh loop will stop.
func (r *AccessTokenRefresher) Start(ctx context.Context) {
	r.once.Do(func() {
		r.log = flightlog.InitLogs()
		if r.config.AuthInfo.RefreshToken == "" {
			return
		}
		if err := r.init(); err != nil {
			r.log.WithError(err).Error("failed to initialize authorizer")
			return
		}
		expireTime, err := r.parseExpireTime()
		if err != nil || r.shouldRefresh(expireTime) {
			if err := r.refresh(); err != nil {
				r.log.WithError(err).Error("failed to refresh access token")
				return
			}
		}
		ctx, cancel := context.WithCancel(ctx)
		r.cancel = cancel
		go r.refreshLoop(ctx)
	})
}

// Stop stops the token refresh loop gracefully
func (r *AccessTokenRefresher) Stop() {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

func (r *AccessTokenRefresher) accessToken() string {
	if r.config.AuthInfo.TokenToUse == TokenToUseIdToken {
		return r.config.AuthInfo.IdToken
	}
	return r.config.AuthInfo.AccessToken
}

// GetAccessToken returns the current access token.
// Start() must be called before calling this method to initialize the refresh loop.
func (r *AccessTokenRefresher) GetAccessToken() string {
	return r.accessToken()
}
