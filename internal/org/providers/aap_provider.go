package providers

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strconv"

	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/org"
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/pkg/aap"
)

type AAPClientInterface interface {
	GetOrganization(token string, organizationID string) (*aap.AAPOrganization, error)
	ListUserTeams(token string, userID string) ([]*aap.AAPTeam, error)
	ListOrganizations(token string) ([]*aap.AAPOrganization, error)
	ListUserOrganizations(token string, userID string) ([]*aap.AAPOrganization, error)
}

type AAPProvider struct {
	client AAPClientInterface
	cache  cache.Membership
}

func cacheKey(userID string, orgID string) string {
	return fmt.Sprintf("%s:%s", userID, orgID)
}

func NewAAPProvider(apiUrl string, tlsConfig *tls.Config, cache cache.Membership) (*AAPProvider, error) {
	aapClient, err := aap.NewAAPGatewayClient(aap.AAPGatewayClientOptions{
		GatewayUrl:      apiUrl,
		TLSClientConfig: tlsConfig,
	})
	if err != nil {
		return nil, err
	}

	if cache == nil {
		return nil, fmt.Errorf("AAP organization provider requires a membership cache")
	}

	return &AAPProvider{
		client: aapClient,
		cache:  cache,
	}, nil
}

func (p *AAPProvider) GetUserOrganizations(ctx context.Context, identity common.Identity) ([]org.ExternalOrganization, error) {
	aapIdentity, ok := identity.(authn.AAPGatewayUserIdentity)
	if !ok {
		return nil, fmt.Errorf("cannot get organizations claims from a non-token identity (got %T)", identity)
	}

	userID := aapIdentity.GetUID()
	if userID == "" {
		return nil, fmt.Errorf("user ID is required")
	}

	var orgs []org.ExternalOrganization
	var err error
	if aapIdentity.IsSuperuser() || aapIdentity.IsPlatformAuditor() {
		orgs, err = p.getAllOrganizations(ctx)
	} else {
		orgs, err = p.getUserScopedOrganizations(ctx, userID)
	}

	if err != nil {
		return nil, err
	}
	p.updateCacheFromOrgs(userID, orgs)
	return orgs, nil
}

func (p *AAPProvider) getAllOrganizations(ctx context.Context) ([]org.ExternalOrganization, error) {
	token, ok := ctx.Value(consts.TokenCtxKey).(string)
	if !ok {
		return nil, fmt.Errorf("token is required")
	}

	organizations, err := p.client.ListOrganizations(token)
	if err != nil {
		return nil, err
	}
	externalOrgs := make([]org.ExternalOrganization, 0, len(organizations))
	for _, organization := range organizations {
		externalOrgs = append(externalOrgs, org.ExternalOrganization{
			ID:   strconv.Itoa(organization.ID),
			Name: organization.Name,
		})
	}

	return externalOrgs, nil
}

func (p *AAPProvider) getUserScopedOrganizations(ctx context.Context, userID string) ([]org.ExternalOrganization, error) {
	token, ok := ctx.Value(consts.TokenCtxKey).(string)
	if !ok {
		return nil, fmt.Errorf("token is required")
	}

	aapOrganizations, err := p.client.ListUserOrganizations(token, userID)
	if err != nil {
		return nil, err
	}

	aapTeams, err := p.client.ListUserTeams(token, userID)
	if err != nil {
		return nil, err
	}

	aapOrganizationsMap := make(map[int]*aap.AAPOrganization, len(aapOrganizations))
	for _, organization := range aapOrganizations {
		aapOrganizationsMap[organization.ID] = organization
	}

	for _, team := range aapTeams {
		aapOrganizationsMap[team.SummaryFields.Organization.ID] = &team.SummaryFields.Organization
	}

	externalOrgs := make([]org.ExternalOrganization, 0, len(aapOrganizationsMap))
	for _, organization := range aapOrganizationsMap {
		externalOrgs = append(externalOrgs, org.ExternalOrganization{
			ID:   strconv.Itoa(organization.ID),
			Name: organization.Name,
		})
	}

	return externalOrgs, nil
}

func (p *AAPProvider) IsMemberOf(ctx context.Context, identity common.Identity, externalOrgID string) (bool, error) {
	aapIdentity, ok := identity.(authn.AAPGatewayUserIdentity)
	if !ok {
		return false, fmt.Errorf("cannot get organizations claims from a non-token identity (got %T)", identity)
	}

	userID := aapIdentity.GetUID()
	if userID == "" {
		return false, fmt.Errorf("user ID is required")
	}

	if present, isMember := p.cache.Get(cacheKey(userID, externalOrgID)); present {
		return isMember, nil
	}

	var isMember bool
	var err error
	if aapIdentity.IsSuperuser() || aapIdentity.IsPlatformAuditor() {
		isMember, err = p.organizationExists(ctx, externalOrgID)
	} else {
		isMember, err = p.userHasMembership(ctx, userID, externalOrgID)
	}

	if err != nil {
		return false, err
	}

	p.updateCache(userID, externalOrgID, isMember)
	return isMember, nil
}

func (p *AAPProvider) organizationExists(ctx context.Context, externalOrgID string) (bool, error) {
	token, ok := ctx.Value(consts.TokenCtxKey).(string)
	if !ok {
		return false, fmt.Errorf("token is required")
	}

	_, err := p.client.GetOrganization(token, externalOrgID)
	if err != nil {
		return false, err
	}

	return true, nil
}

func (p *AAPProvider) userHasMembership(ctx context.Context, userID string, externalOrgID string) (bool, error) {
	token, ok := ctx.Value(consts.TokenCtxKey).(string)
	if !ok {
		return false, fmt.Errorf("token is required")
	}

	org, err := p.client.GetOrganization(token, externalOrgID)
	if err == nil && org != nil {
		// If we can get the organization directly, the user has access
		return true, nil
	}
	if err != nil && !errors.Is(err, aap.ErrNotFound) && !errors.Is(err, aap.ErrForbidden) {
		return false, err
	}

	// If we can't get the organization directly, we have to double check team-based membership
	teams, err := p.client.ListUserTeams(token, userID)
	if err != nil {
		return false, err
	}

	for _, team := range teams {
		if strconv.Itoa(team.SummaryFields.Organization.ID) == externalOrgID {
			return true, nil
		}
	}

	return false, nil
}

func (p *AAPProvider) updateCache(userID string, externalOrgID string, isMember bool) {
	key := cacheKey(userID, externalOrgID)
	p.cache.Set(key, isMember)
}

func (p *AAPProvider) updateCacheFromOrgs(userID string, orgs []org.ExternalOrganization) {
	for _, org := range orgs {
		p.updateCache(userID, org.ID, true)
	}
}
