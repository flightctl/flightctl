package authn

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/pkg/aap"
	"github.com/jellydator/ttlcache/v3"
)

type AAPGatewayUserIdentity interface {
	common.Identity
	IsSuperuser() bool
	IsPlatformAuditor() bool
}

// AAPIdentity extends common.Identity with AAP-specific fields
type AAPIdentity struct {
	common.BaseIdentity
	superUser       bool
	platformAuditor bool
}

// Ensure AAPIdentity implements AAPGatewayUserIdentity
var _ AAPGatewayUserIdentity = (*AAPIdentity)(nil)

func (a *AAPIdentity) IsSuperuser() bool {
	return a.superUser
}

func (a *AAPIdentity) IsPlatformAuditor() bool {
	return a.platformAuditor
}

type AapGatewayAuth struct {
	externalGatewayUrl string
	aapClient          *aap.AAPGatewayClient
	cache              *ttlcache.Cache[string, *AAPIdentity]
}

func NewAapGatewayAuth(gatewayUrl string, externalGatewayUrl string, clientTlsConfig *tls.Config) (*AapGatewayAuth, error) {
	aapClient, err := aap.NewAAPGatewayClient(aap.AAPGatewayClientOptions{
		GatewayUrl:      gatewayUrl,
		TLSClientConfig: clientTlsConfig,
	})
	if err != nil {
		return nil, err
	}

	authN := AapGatewayAuth{
		aapClient:          aapClient,
		externalGatewayUrl: externalGatewayUrl,
		cache:              ttlcache.New[string, *AAPIdentity](ttlcache.WithTTL[string, *AAPIdentity](5 * time.Second)),
	}
	go authN.cache.Start()
	return &authN, nil
}

func (a AapGatewayAuth) loadUserInfo(token string) (*AAPIdentity, error) {
	item := a.cache.Get(token)
	if item != nil {
		return item.Value(), nil
	}

	aapUserInfo, err := a.aapClient.GetMe(token)
	if err != nil {
		return nil, err
	}

	userInfo := &AAPIdentity{
		BaseIdentity:    *common.NewBaseIdentity(aapUserInfo.Username, strconv.Itoa(aapUserInfo.ID), []string{}),
		superUser:       aapUserInfo.IsSuperuser,
		platformAuditor: aapUserInfo.IsPlatformAuditor,
	}

	a.cache.Set(token, userInfo, ttlcache.DefaultTTL)
	return userInfo, nil
}

func (a AapGatewayAuth) ValidateToken(ctx context.Context, token string) error {
	_, err := a.loadUserInfo(token)
	return err
}

func (a AapGatewayAuth) GetAuthConfig() common.AuthConfig {
	return common.AuthConfig{
		Type: common.AuthTypeAAP,
		Url:  a.externalGatewayUrl,
	}
}

func (AapGatewayAuth) GetAuthToken(r *http.Request) (string, error) {
	return common.ExtractBearerToken(r)
}

func (a AapGatewayAuth) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	userInfo, err := a.loadUserInfo(token)
	if err != nil {
		return nil, fmt.Errorf("failed to get identity: %w", err)
	}

	return userInfo, nil
}
