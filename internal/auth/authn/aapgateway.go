package authn

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strconv"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/pkg/aap"
	"github.com/jellydator/ttlcache/v3"
	"github.com/samber/lo"
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

	// Map AAP permissions to roles
	roles := []string{}
	if aapUserInfo.IsSuperuser {
		roles = append(roles, "admin")
	}
	if aapUserInfo.IsPlatformAuditor {
		roles = append(roles, "auditor")
	}
	if len(roles) == 0 {
		roles = append(roles, "user") // default role
	}

	userInfo := &AAPIdentity{
		BaseIdentity:    *common.NewBaseIdentityWithIssuer(aapUserInfo.Username, strconv.Itoa(aapUserInfo.ID), []common.ReportedOrganization{}, roles, identity.NewIssuer(identity.AuthTypeAAP, a.externalGatewayUrl)),
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

func (a AapGatewayAuth) GetAuthConfig() *api.AuthConfig {
	providerType := string(api.AuthProviderInfoTypeAap)
	providerName := string(api.AuthProviderInfoTypeAap)
	provider := api.AuthProviderInfo{
		Name:      &providerName,
		Type:      (*api.AuthProviderInfoType)(&providerType),
		AuthUrl:   &a.externalGatewayUrl,
		IsDefault: lo.ToPtr(true),
		IsStatic:  lo.ToPtr(true),
	}

	return &api.AuthConfig{
		DefaultProvider:      &providerType,
		OrganizationsEnabled: lo.ToPtr(true),
		Providers:            &[]api.AuthProviderInfo{provider},
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

	// Get user organizations from AAP
	organizations, err := a.getUserOrganizations(ctx, token, userInfo)
	if err != nil {
		// Log error but don't fail authentication - organizations are optional
		// TODO: Add proper logging here
		organizations = []string{}
	}
	reportedOrganizations := make([]common.ReportedOrganization, 0, len(organizations))
	for _, org := range organizations {
		reportedOrganizations = append(reportedOrganizations, common.ReportedOrganization{
			Name:         org,
			IsInternalID: false,
			ID:           org,
		})
	}
	userInfo.SetOrganizations(reportedOrganizations)

	return userInfo, nil
}

// getUserOrganizations retrieves organizations for the user from AAP
func (a AapGatewayAuth) getUserOrganizations(ctx context.Context, token string, userInfo *AAPIdentity) ([]string, error) {
	var organizations []string
	var err error

	// Superusers and platform auditors get access to all organizations
	if userInfo.IsSuperuser() || userInfo.IsPlatformAuditor() {
		organizations, err = a.getAllOrganizations(ctx, token)
	} else {
		organizations, err = a.getUserScopedOrganizations(ctx, token, userInfo.GetUID())
	}

	if err != nil {
		return nil, err
	}

	return organizations, nil
}

// getAllOrganizations gets all organizations from AAP
func (a AapGatewayAuth) getAllOrganizations(ctx context.Context, token string) ([]string, error) {
	aapOrganizations, err := a.aapClient.ListOrganizations(token)
	if err != nil {
		return nil, err
	}

	organizations := make([]string, 0, len(aapOrganizations))
	for _, org := range aapOrganizations {
		organizations = append(organizations, org.Name)
	}

	return organizations, nil
}

// getUserScopedOrganizations gets organizations for a specific user
func (a AapGatewayAuth) getUserScopedOrganizations(ctx context.Context, token string, userID string) ([]string, error) {
	// Get user's direct organizations
	aapOrganizations, err := a.aapClient.ListUserOrganizations(token, userID)
	if err != nil {
		return nil, err
	}

	// Get user's teams and their organizations
	aapTeams, err := a.aapClient.ListUserTeams(token, userID)
	if err != nil {
		return nil, err
	}

	// Create a map to deduplicate organizations
	orgMap := make(map[string]bool)

	// Add direct organizations
	for _, org := range aapOrganizations {
		orgMap[org.Name] = true
	}

	// Add organizations from teams
	for _, team := range aapTeams {
		orgMap[team.SummaryFields.Organization.Name] = true
	}

	// Convert map to slice
	organizations := make([]string, 0, len(orgMap))
	for orgName := range orgMap {
		organizations = append(organizations, orgName)
	}

	return organizations, nil
}
