package enrollmentconfig

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

// Service builds enrollment client configuration (CA bundle + optional issued client cert).
// Extracted from enrollmentrequest so CSR lookups go through certificatesigningrequest.Service
// without an enrollmentrequest ↔ certificatesigningrequest import cycle via tpmcsr.
type Service interface {
	GetEnrollmentConfig(ctx context.Context, orgId uuid.UUID, params domain.GetEnrollmentConfigParams) (*domain.EnrollmentConfig, domain.Status)
}
