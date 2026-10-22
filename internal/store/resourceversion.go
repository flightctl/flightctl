package store

import (
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
)

// CheckResourceVersionConflict rejects a client-supplied resourceVersion that does not match
// the current stored value. A nil incoming resourceVersion skips the check (server-side CAS
// still applies on persist). When the client supplies a value (including "0") and the current
// resourceVersion is unset, the request conflicts — do not treat nil current as matching "0".
func CheckResourceVersionConflict(current, incoming *domain.ObjectMeta) error {
	if current == nil || incoming == nil || incoming.ResourceVersion == nil {
		return nil
	}
	if current.ResourceVersion == nil {
		return flterrors.ErrResourceVersionConflict
	}
	if *current.ResourceVersion != *incoming.ResourceVersion {
		return flterrors.ErrResourceVersionConflict
	}
	return nil
}
