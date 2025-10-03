package authz

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
)

const (
	// The resources below map to the plural resource name pulled from the url path
	organizationsResource = "organizations"
	listAction            = "list"
)

type MembershipChecker interface {
	IsMemberOf(ctx context.Context, identity common.Identity, orgID uuid.UUID) (bool, error)
}

func isListOrganizations(resource string, op string) bool {
	return resource == organizationsResource && op == listAction
}

type OrgMembershipAuthZ struct {
	membershipChecker MembershipChecker
}

func NewOrgMembershipAuthZ(checker MembershipChecker) *OrgMembershipAuthZ {
	return &OrgMembershipAuthZ{
		membershipChecker: checker,
	}
}

func (o *OrgMembershipAuthZ) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	// Do not check org membership for list operations on the organizations resource
	if isListOrganizations(resource, op) {
		return true, nil
	}

	return o.checkOrgMembership(ctx)
}

func (o *OrgMembershipAuthZ) checkOrgMembership(ctx context.Context) (bool, error) {
	identity, err := common.GetIdentity(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get identity: %w", err)
	}

	orgID, ok := util.GetOrgIdFromContext(ctx)
	if !ok {
		return false, fmt.Errorf("org ID not found in context")
	}

	isMember, err := o.membershipChecker.IsMemberOf(ctx, identity, orgID)
	if err != nil {
		return false, fmt.Errorf("failed to check org membership: %w", err)
	}
	return isMember, nil
}
