package contextutil

import (
	"context"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
)

// GetMappedIdentityFromContext retrieves mapped identity from context
func GetMappedIdentityFromContext(ctx context.Context) (*identity.MappedIdentity, bool) {
	mappedIdentity, ok := ctx.Value(consts.MappedIdentityCtxKey).(*identity.MappedIdentity)
	return mappedIdentity, ok
}
