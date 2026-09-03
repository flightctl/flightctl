package client

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	mu             sync.RWMutex
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
	return CreateAuthProviderWithCredentials(authInfo, insecure, apiServerURL, callbackPort, "", "", false, false)
}

func CreateAuthProviderWithCredentials(authInfo AuthInfo, insecure bool, apiServerURL string, callbackPort int, username, password string, web, noBrowser bool) (login.AuthProvider, error) {
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
		return login.NewOIDCConfig(provider.Metadata, oidcSpec, caFile, authInsecure, apiServerURL, callbackPort, username, password, web, noBrowser), nil

	case string(api.Oauth2):
		oauth2Spec, err := provider.Spec.AsOAuth2ProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("failed to parse OAuth2 provider spec: %w", err)
		}
		return login.NewOAuth2Config(provider.Metadata, oauth2Spec, caFile, authInsecure, apiServerURL, callbackPort, username, password, web, noBrowser), nil

	case string(api.Openshift):
		openshiftSpec, err := provider.Spec.AsOpenShiftProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("failed to parse OpenShift provider spec: %w", err)
		}
		return login.NewOpenShiftConfig(provider.Metadata, openshiftSpec, caFile, authInsecure, apiServerURL, callbackPort, username, password, web, noBrowser), nil

	case string(api.Aap):
		aapSpec, err := provider.Spec.AsAapProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("failed to parse AAP provider spec: %w", err)
		}
		return login.NewAAPOAuth2Config(provider.Metadata, aapSpec, caFile, authInsecure, apiServerURL, callbackPort, username, password, web, noBrowser), nil

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
	accessTokenExpiry := r.accessTokenExpiry()
	if accessTokenExpiry == "" {
		return time.Time{}, fmt.Errorf("no access token expiry found")
	}
	return time.Parse(time.RFC3339Nano, accessTokenExpiry)
}

func (r *AccessTokenRefresher) shouldRefresh(expireTime time.Time) bool {
	return time.Now().Add(5 * time.Second).After(expireTime)
}

func isExpiredTokenError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "invalid_grant")
}

func (r *AccessTokenRefresher) refresh() error {
	refreshToken := r.getRefreshToken()
	if refreshToken == "" {
		return fmt.Errorf("no refresh token found")
	}
	authInfo, err := r.provider.Renew(refreshToken)
	if err != nil {
		return fmt.Errorf("failed to renew token: %w", err)
	}

	r.setRefreshToken(authInfo.RefreshToken)
	r.setAccessToken(authInfo.AccessToken)
	r.setAccessTokenExpiry(authInfo.ExpiresIn)
	r.setIdToken(authInfo.IdToken)
	var snapshot *Config
	if r.configFilePath != "" {
		snapshot = r.config.DeepCopy()
	}

	if snapshot != nil {
		return snapshot.Persist(r.configFilePath)
	}
	return nil
}

func (r *AccessTokenRefresher) waitDuration() time.Duration {
	accessTokenExpiry := r.accessTokenExpiry()
	if accessTokenExpiry == "" {
		return time.Second
	}
	expireTime, err := time.Parse(time.RFC3339Nano, accessTokenExpiry)
	if err != nil {
		r.log.Errorf("failed to parse time %s: %v", accessTokenExpiry, err)
		return time.Second
	}
	return util.Max(time.Until(expireTime)-5*time.Second, time.Second)
}

func (r *AccessTokenRefresher) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(r.waitDuration())
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := r.refresh(); err != nil {
				if isExpiredTokenError(err) {
					fmt.Fprintln(os.Stderr, "Error: Your session has expired. Please log in again using: flightctl login <server>")
				} else {
					r.log.Errorf("failed to renew token: %v", err)
				}
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
		hasRefreshToken := r.getRefreshToken() != ""
		if !hasRefreshToken {
			r.log.Info("no refresh token found, skipping token refresh")
			return
		}
		if err := r.init(); err != nil {
			r.log.WithError(err).Error("failed to initialize authorizer")
			return
		}
		expireTime, err := r.parseExpireTime()
		if err != nil || r.shouldRefresh(expireTime) {
			if err := r.refresh(); err != nil {
				if isExpiredTokenError(err) {
					fmt.Fprintln(os.Stderr, "Error: Your session has expired. Please log in again using: flightctl login <server>")
				} else {
					r.log.WithError(err).Error("failed to refresh access token")
				}
				return
			}
		}
		ctx, cancel := context.WithCancel(ctx)
		r.setCancel(cancel)
		go r.refreshLoop(ctx)
	})
}

// Stop stops the token refresh loop gracefully
func (r *AccessTokenRefresher) Stop() {
	cancel := r.getCancel()
	if cancel != nil {
		cancel()
	}
}

func (r *AccessTokenRefresher) accessToken() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
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

func (r *AccessTokenRefresher) getRefreshToken() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.AuthInfo.RefreshToken
}

func (r *AccessTokenRefresher) accessTokenExpiry() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.AuthInfo.AccessTokenExpiry
}

func (r *AccessTokenRefresher) setCancel(cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancel = cancel
}

func (r *AccessTokenRefresher) getCancel() context.CancelFunc {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cancel
}

func (r *AccessTokenRefresher) setAccessToken(accessToken string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.config.AuthInfo.AccessToken = accessToken
}

func (r *AccessTokenRefresher) setAccessTokenExpiry(accessTokenExpiry *int64) {
	if accessTokenExpiry == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	expiryTime := time.Now().Add(time.Duration(*accessTokenExpiry) * time.Second)
	r.config.AuthInfo.AccessTokenExpiry = expiryTime.Format(time.RFC3339Nano)

}

func (r *AccessTokenRefresher) setRefreshToken(refreshToken string) {
	if refreshToken == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.config.AuthInfo.RefreshToken = refreshToken
}

func (r *AccessTokenRefresher) setIdToken(idToken string) {
	if idToken == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.config.AuthInfo.IdToken = idToken
}
