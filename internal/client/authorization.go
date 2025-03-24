package client

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/cli/login"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
)

type accessTokenRefresher struct {
	config         *Config
	renewals       atomic.Int32
	once           sync.Once
	provider       login.AuthProvider
	log            logrus.FieldLogger
	configFilePath string
}

func CreateAuthProvider(authInfo AuthInfo, insecure bool) (login.AuthProvider, error) {
	switch authInfo.AuthType {
	case common.AuthTypeK8s:
		return login.NewK8sOAuth2Config(authInfo.AuthCAFile, authInfo.ClientId, authInfo.AuthURL, insecure), nil
	case common.AuthTypeOIDC:
		return login.NewOIDCConfig(authInfo.AuthCAFile, authInfo.ClientId, authInfo.AuthURL, insecure), nil
	case common.AuthTypeAAP:
		return login.NewAAPOAuth2Config(authInfo.AuthCAFile, authInfo.ClientId, authInfo.AuthURL, insecure), nil
	default:
		return nil, fmt.Errorf("unsupported auth type: %s", authInfo.AuthType)
	}
}

func (c *accessTokenRefresher) init() error {
	var err error
	c.provider, err = CreateAuthProvider(c.config.AuthInfo, c.config.Service.InsecureSkipVerify)
	return err
}

func (c *accessTokenRefresher) parseExpireTime() (time.Time, error) {
	return time.Parse(time.RFC3339Nano, c.config.AuthInfo.AccessTokenExpiry)
}

func (c *accessTokenRefresher) shouldRefresh(expireTime time.Time) bool {
	return time.Now().Add(5 * time.Second).After(expireTime)
}

func (c *accessTokenRefresher) refresh() error {
	authInfo, err := c.provider.Renew(c.config.AuthInfo.RefreshToken)
	if err != nil {
		return fmt.Errorf("failed to renew token: %w", err)
	}
	c.config.AuthInfo.RefreshToken = authInfo.RefreshToken
	c.config.AuthInfo.AccessToken = authInfo.AccessToken
	if authInfo.ExpiresIn != nil {
		c.config.AuthInfo.AccessTokenExpiry = time.Now().Add(time.Duration(*authInfo.ExpiresIn) * time.Second).Format(time.RFC3339Nano)
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
	return c.config.AuthInfo.AccessToken
}

func (c *accessTokenRefresher) rewind() {
	c.renewals.Store(3)
}

func GetAccessToken(config *Config, configFilePath string) string {
	auth := authorizer.GetOrInit(&accessTokenRefresher{
		config:         config,
		configFilePath: configFilePath,
	})
	auth.start()
	auth.rewind()
	return auth.accessToken()
}
