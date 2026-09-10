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
	persistMu      sync.Mutex // serializes config updates with Persist so an older refresh cannot overwrite a newer file
	provider       login.AuthProvider
	log            logrus.FieldLogger
	configFilePath string
	callbackPort   int
	cancel         context.CancelFunc
	stopped        bool
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

// init creates the auth provider from client config. If a provider is already
// set (for example in tests), it is left unchanged.
func (r *AccessTokenRefresher) init() error {
	if r.provider != nil {
		return nil
	}
	var err error
	r.provider, err = CreateAuthProvider(r.config.AuthInfo, r.config.Service.InsecureSkipVerify, r.config.Service.Server, r.callbackPort)
	return err
}

// parseExpireTime returns the current access-token expiry as a timestamp.
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

// refresh renews the access token with the auth provider and persists the
// updated config when a config file path is set.
func (r *AccessTokenRefresher) refresh() error {
	refreshToken := r.getRefreshToken()
	if refreshToken == "" {
		return fmt.Errorf("no refresh token found")
	}
	authInfo, err := r.provider.Renew(refreshToken)
	if err != nil {
		return fmt.Errorf("failed to renew token: %w", err)
	}

	r.persistMu.Lock()
	defer r.persistMu.Unlock()
	snapshot := r.refreshAuthInfo(authInfo)
	if snapshot != nil {
		return snapshot.Persist(r.configFilePath)
	}
	return nil
}

// waitDuration returns how long to wait before the next refresh, targeting
// five seconds before expiry with a minimum of one second.
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
// If Stop has already been called, Start does not launch the loop.
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
		if !r.setCancel(cancel) {
			cancel()
			return
		}
		go r.refreshLoop(ctx)
	})
}

// Stop stops the token refresh loop. It records a stopped state even if the
// loop has not started yet, so a later Start will not launch it.
func (r *AccessTokenRefresher) Stop() {
	if cancel := r.takeCancel(); cancel != nil {
		cancel()
	}
}

// accessToken returns the token currently used for API calls.
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

// getRefreshToken returns the stored refresh token.
func (r *AccessTokenRefresher) getRefreshToken() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.AuthInfo.RefreshToken
}

// accessTokenExpiry returns the stored access-token expiry string.
func (r *AccessTokenRefresher) accessTokenExpiry() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.AuthInfo.AccessTokenExpiry
}

// setCancel stores the refresh-loop cancel function. It returns false if
// Stop has already been called, in which case the caller must not start the loop.
func (r *AccessTokenRefresher) setCancel(cancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return false
	}
	r.cancel = cancel
	return true
}

// takeCancel marks the refresher stopped and returns the stored cancel
// function, if any, so Stop can cancel an in-flight refresh loop.
func (r *AccessTokenRefresher) takeCancel() context.CancelFunc {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	cancel := r.cancel
	r.cancel = nil
	return cancel
}

// refreshAuthInfo writes renewed tokens into the in-memory config. If a
// config file path is set, it returns a snapshot to persist; otherwise it returns nil.
func (r *AccessTokenRefresher) refreshAuthInfo(authInfo login.AuthInfo) *Config {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	var snapshot *Config
	if r.configFilePath != "" {
		snapshot = r.config.DeepCopy()
	}
	return snapshot
}
