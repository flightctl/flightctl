package client

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
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

type accessTokenRefresher struct {
	config         *Config
	renewals       atomic.Int32
	once           sync.Once
	provider       login.AuthProvider
	log            logrus.FieldLogger
	configFilePath string
	callbackPort   int
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

func (c *accessTokenRefresher) init() error {
	var err error
	c.provider, err = CreateAuthProvider(c.config.AuthInfo, c.config.Service.InsecureSkipVerify, c.config.Service.Server, c.callbackPort)
	return err
}

func (c *accessTokenRefresher) parseExpireTime() (time.Time, error) {
	if c.config.AuthInfo.AccessTokenExpiry == "" {
		return time.Time{}, fmt.Errorf("no access token expiry found")
	}
	return time.Parse(time.RFC3339Nano, c.config.AuthInfo.AccessTokenExpiry)
}

func (c *accessTokenRefresher) shouldRefresh(expireTime time.Time) bool {
	return time.Now().Add(5 * time.Second).After(expireTime)
}

func (c *accessTokenRefresher) refresh() error {
	if c.config.AuthInfo.RefreshToken == "" {
		return fmt.Errorf("no refresh token found")
	}
	authInfo, err := c.provider.Renew(c.config.AuthInfo.RefreshToken)
	if err != nil {
		return fmt.Errorf("failed to renew token: %w", err)
	}
	if authInfo.RefreshToken != "" {
		c.config.AuthInfo.RefreshToken = authInfo.RefreshToken
	}
	c.config.AuthInfo.AccessToken = authInfo.AccessToken
	if authInfo.ExpiresIn != nil {
		expiryTime := time.Now().Add(time.Duration(*authInfo.ExpiresIn) * time.Second)
		c.config.AuthInfo.AccessTokenExpiry = expiryTime.Format(time.RFC3339Nano)
	}
	if authInfo.IdToken != "" {
		c.config.AuthInfo.IdToken = authInfo.IdToken
	}
	return c.config.Persist(c.configFilePath)
}

func (c *accessTokenRefresher) waitDuration() time.Duration {
	waitDuration := time.Second
	if c.config.AuthInfo.AccessTokenExpiry != "" {
		expireTime, err := c.parseExpireTime()
		if err != nil {
			c.log.Errorf("failed to parse time %s: %v", c.config.AuthInfo.AccessTokenExpiry, err)
		} else {
			waitDuration = util.Max(time.Until(expireTime)-5*time.Second, time.Second)
		}
	}
	return waitDuration
}

func (c *accessTokenRefresher) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(c.waitDuration())
	defer ticker.Stop()
	for c.renewals.Add(-1) >= 0 {
		select {
		case <-ticker.C:
			if err := c.refresh(); err != nil {
				c.log.Errorf("failed to renew token: %v", err)
				return
			}
			c.log.Info("renewed access token")
			ticker.Reset(c.waitDuration())
		case <-ctx.Done():
			return
		}
	}
}

func (c *accessTokenRefresher) start() {
	c.once.Do(func() {
		c.log = flightlog.InitLogs()
		if c.config.AuthInfo.RefreshToken == "" {
			return
		}
		if err := c.init(); err != nil {
			c.log.WithError(err).Error("failed to initialize authorizer")
			return
		}
		expireTime, err := c.parseExpireTime()
		if err != nil || c.shouldRefresh(expireTime) {
			if err := c.refresh(); err != nil {
				c.log.WithError(err).Error("failed to refresh access token")
				return
			}
		}
		go c.refreshLoop(context.TODO())
	})
}

var authorizer util.Singleton[accessTokenRefresher]

func (c *accessTokenRefresher) accessToken() string {
	if c.config.AuthInfo.TokenToUse == TokenToUseIdToken {
		return c.config.AuthInfo.IdToken
	}
	return c.config.AuthInfo.AccessToken
}

func (c *accessTokenRefresher) rewind() {
	c.renewals.Store(3)
}

func GetAccessToken(config *Config, configFilePath string) string {
	auth := authorizer.GetOrInit(&accessTokenRefresher{
		config:         config,
		configFilePath: configFilePath,
		callbackPort:   8080,
	})
	auth.start()
	auth.rewind()
	token := auth.accessToken()
	return token
}
