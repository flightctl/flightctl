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
	metadata  api.ObjectMeta
	spec      api.AapProviderSpec
	aapClient *aap.AAPGatewayClient
	cache     *ttlcache.Cache[string, *AAPIdentity]
}

func NewAapGatewayAuth(metadata api.ObjectMeta, spec api.AapProviderSpec, clientTlsConfig *tls.Config) (*AapGatewayAuth, error) {
	aapClient, err := aap.NewAAPGatewayClient(aap.AAPGatewayClientOptions{
		GatewayUrl:      spec.ApiUrl,
		TLSClientConfig: clientTlsConfig,
	})
	if err != nil {
		return nil, err
	}

	authN := AapGatewayAuth{
		metadata:  metadata,
		spec:      spec,
		aapClient: aapClient,
		cache:     ttlcache.New[string, *AAPIdentity](ttlcache.WithTTL[string, *AAPIdentity](5 * time.Second)),
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

	externalApiUrl := a.spec.ApiUrl
	if a.spec.ExternalApiUrl != nil && *a.spec.ExternalApiUrl != "" {
		externalApiUrl = *a.spec.ExternalApiUrl
	}

	userInfo := &AAPIdentity{
		BaseIdentity:    *common.NewBaseIdentityWithIssuer(aapUserInfo.Username, strconv.Itoa(aapUserInfo.ID), []common.ReportedOrganization{}, identity.NewIssuer(identity.AuthTypeAAP, externalApiUrl)),
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
	provider := api.AuthProvider{
		ApiVersion: api.AuthProviderAPIVersion,
		Kind:       api.AuthProviderKind,
		Metadata:   a.metadata,
		Spec:       api.AuthProviderSpec{},
	}
	_ = provider.Spec.FromAapProviderSpec(a.spec)

	return &api.AuthConfig{
		ApiVersion:           api.AuthConfigAPIVersion,
		DefaultProvider:      a.metadata.Name,
		OrganizationsEnabled: lo.ToPtr(true),
		Providers:            &[]api.AuthProvider{provider},
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

	// Map AAP permissions to roles (global roles that apply to all orgs)
	roles := []string{}
	if userInfo.IsSuperuser() {
		roles = append(roles, api.ExternalRoleAdmin)
	}
	if userInfo.IsPlatformAuditor() {
		roles = append(roles, api.ExternalRoleViewer)
	}

	// Build ReportedOrganization with roles embedded
	// AAP roles are global - apply to all organizations
	orgRoles := map[string][]string{
		"*": roles, // All AAP roles are global
	}
	reportedOrganizations, isSuperAdmin := common.BuildReportedOrganizations(organizations, orgRoles, false)
	userInfo.SetOrganizations(reportedOrganizations)
	userInfo.SetSuperAdmin(isSuperAdmin)

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
