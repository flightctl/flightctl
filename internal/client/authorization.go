package client

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/internal/cli/login"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
)

type accessTokenRefresher struct {
	authInfo           AuthInfo
	insecureSkipVerify bool
	renewals           atomic.Int32
	once               sync.Once
	provider           login.AuthProvider
	log                logrus.FieldLogger
}

func (c *accessTokenRefresher) init() error {
	switch c.authInfo.AuthType {
	case "k8s":
		c.provider = login.NewK8sOAuth2Config(c.authInfo.AuthCAFile, c.authInfo.ClientId, c.authInfo.AuthURL, c.insecureSkipVerify)
	case "OIDC":
		c.provider = login.NewOIDCConfig(c.authInfo.AuthCAFile, c.authInfo.ClientId, c.authInfo.AuthURL, c.insecureSkipVerify)
	default:
		return fmt.Errorf("unsupported auth type: %s", c.authInfo.AuthType)
	}
	return nil
}

func (c *accessTokenRefresher) parseExpireTime() (time.Time, error) {
	return time.Parse(time.RFC3339Nano, c.authInfo.AccessTokenExpiry)
}

func (c *accessTokenRefresher) shouldRefresh(expireTime time.Time) bool {
	return time.Now().Add(5 * time.Second).After(expireTime)
}

func (c *accessTokenRefresher) refresh() error {
	authInfo, err := c.provider.Renew(c.authInfo.RefreshToken)
	if err != nil {
		return fmt.Errorf("failed to renew token: %w", err)
	}
	c.authInfo.RefreshToken = authInfo.RefreshToken
	c.authInfo.AccessToken = authInfo.AccessToken
	if authInfo.ExpiresIn != nil {
		c.authInfo.AccessTokenExpiry = time.Now().Add(time.Duration(*authInfo.ExpiresIn) * time.Second).Format(time.RFC3339Nano)
	}
	return nil
}

func (c *accessTokenRefresher) waitDuration() time.Duration {
	waitDuration := time.Second
	if c.authInfo.AccessTokenExpiry != "" {
		expireTime, err := c.parseExpireTime()
		if err != nil {
			c.log.Errorf("failed to parse time %s: %v", c.authInfo.AccessTokenExpiry, err)
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
		if c.authInfo.RefreshToken == "" {
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
	return c.authInfo.AccessToken
}

func (c *accessTokenRefresher) rewind() {
	c.renewals.Store(3)
}

func GetAccessToken(config *Config) string {
	auth := authorizer.GetOrInit(&accessTokenRefresher{
		authInfo:           config.AuthInfo,
		insecureSkipVerify: config.Service.InsecureSkipVerify,
	})
	auth.start()
	auth.rewind()
	return auth.accessToken()
}
