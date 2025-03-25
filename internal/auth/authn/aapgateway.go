package authn

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/jellydator/ttlcache/v3"
)

const (
	AdminGroup    = "admin"
	OperatorGroup = "operator"
	AuditorGroup  = "auditor"
)

type AAPUser struct {
	Username          string `json:"username,omitempty"`
	IsSuperuser       bool   `json:"is_superuser,omitempty"`
	IsPlatformAuditor bool   `json:"is_platform_auditor,omitempty"`
}

type AAPUserInfo struct {
	Results []AAPUser `json:"results,omitempty"`
}

type AapGatewayAuth struct {
	gatewayUrl         string
	externalGatewayUrl string
	clientTlsConfig    *tls.Config
	cache              *ttlcache.Cache[string, *AAPUser]
}

func NewAapGatewayAuth(gatewayUrl string, externalGatewayUrl string, clientTlsConfig *tls.Config) AapGatewayAuth {
	authN := AapGatewayAuth{
		gatewayUrl:         gatewayUrl,
		externalGatewayUrl: externalGatewayUrl,
		clientTlsConfig:    clientTlsConfig,
		cache:              ttlcache.New[string, *AAPUser](ttlcache.WithTTL[string, *AAPUser](5 * time.Second)),
	}
	go authN.cache.Start()
	return authN
}

func (a AapGatewayAuth) loadUserInfo(token string) (*AAPUser, error) {
	item := a.cache.Get(token)
	if item != nil {
		return item.Value(), nil
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: a.clientTlsConfig,
		},
	}

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/gateway/v1/me/", a.gatewayUrl), nil)
	if err != nil {
		return nil, err
	}

	req.Header.Add(common.AuthHeader, fmt.Sprintf("Bearer %s", token))

	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unexpected error: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %v", res.StatusCode)
	}

	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	userInfo := &AAPUserInfo{}
	if err := json.Unmarshal(body, userInfo); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(userInfo.Results) == 0 {
		return nil, fmt.Errorf("no user info in response")
	}

	a.cache.Set(token, &userInfo.Results[0], ttlcache.DefaultTTL)
	return &userInfo.Results[0], nil
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

func (a AapGatewayAuth) GetIdentity(ctx context.Context, token string) (*common.Identity, error) {
	userInfo, err := a.loadUserInfo(token)
	if err != nil {
		return nil, fmt.Errorf("failed to get identity: %w", err)
	}

	identity := common.Identity{
		Username: userInfo.Username,
		Groups:   []string{},
	}

	if !userInfo.IsSuperuser && !userInfo.IsPlatformAuditor {
		identity.Groups = append(identity.Groups, OperatorGroup)
	} else {
		if userInfo.IsSuperuser {
			identity.Groups = append(identity.Groups, AdminGroup)
		}
		if userInfo.IsPlatformAuditor {
			identity.Groups = append(identity.Groups, AuditorGroup)
		}
	}

	return &identity, nil
}
