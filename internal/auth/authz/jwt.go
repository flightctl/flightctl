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

type JWTAuthZ struct {
	membershipChecker MembershipChecker
}

func NewJWTAuthZ(checker MembershipChecker) *JWTAuthZ {
	return &JWTAuthZ{
		membershipChecker: checker,
	}
}

func (j *JWTAuthZ) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	// Do not check org membership for list operations on the organizations resource
	if resource == organizationsResource && op == listAction {
		return true, nil
	}

	return j.checkOrgMembership(ctx)
}

func (j *JWTAuthZ) checkOrgMembership(ctx context.Context) (bool, error) {
	identity, err := common.GetIdentity(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to get identity: %w", err)
	}

	orgID, ok := util.GetOrgIdFromContext(ctx)
	if !ok {
		return false, fmt.Errorf("org ID not found in context")
	}

	isMember, err := j.membershipChecker.IsMemberOf(ctx, identity, orgID)
	if err != nil {
		return false, fmt.Errorf("failed to check org membership: %w", err)
	}
	return isMember, nil
}
