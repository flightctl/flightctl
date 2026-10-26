package enrollmentrequest

import (
	"context"

	"github.com/flightctl/flightctl/internal/auth/common"
)

type enrollmentCSRNameProvider interface {
	GetEnrollmentCSRName() *string
}

// enrollmentCSRNameFromContext returns the name resolved by enrollment authentication.
// Resolution belongs to the authentication boundary, not to Enrollment Request persistence.
func enrollmentCSRNameFromContext(ctx context.Context) *string {
	identity, err := common.GetIdentity(ctx)
	if err != nil {
		return nil
	}
	provider, ok := identity.(enrollmentCSRNameProvider)
	if !ok {
		return nil
	}
	return provider.GetEnrollmentCSRName()
}
