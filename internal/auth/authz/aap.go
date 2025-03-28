package authz

import (
	"context"
	"slices"

	"github.com/flightctl/flightctl/internal/auth/common"
)

var viewerOps = []string{"list", "get"}
var viewerResources = []string{"devices", "fleets", "resourcesyncs"}

type AapAuthZ struct{}

func (aapAuth AapAuthZ) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	identity, err := common.GetIdentity(ctx)
	if err != nil {
		return false, err
	}

	if slices.Contains(identity.Groups, "admin") {
		return true, nil
	}

	return slices.Contains(viewerOps, op) && slices.Contains(viewerResources, resource), nil
}
