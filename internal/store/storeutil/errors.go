package storeutil

import (
	"errors"

	"github.com/flightctl/flightctl/internal/flterrors"
	"gorm.io/gorm"
)

// ErrorFromGormError translates well-known gorm errors into domain-specific errors.
func ErrorFromGormError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, flterrors.ErrDuplicateOIDCProvider), errors.Is(err, flterrors.ErrDuplicateOAuth2Provider):
		// Our custom dialector has already detected specific authprovider constraint violations
		return err
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, gorm.ErrForeignKeyViolated):
		return flterrors.ErrResourceNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return flterrors.ErrDuplicateName
	default:
		return err
	}
}
