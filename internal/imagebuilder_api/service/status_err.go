package service

import (
	"errors"
	"net/http"

	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
)

// statusToErr maps a core service domain.Status into an error suitable for
// existing imagebuilder error checks (errors.Is with flterrors).
func statusToErr(status coredomain.Status) error {
	if status.Code >= 200 && status.Code < 300 {
		return nil
	}
	switch status.Code {
	case http.StatusNotFound:
		return flterrors.ErrResourceNotFound
	case http.StatusConflict:
		return flterrors.ErrDuplicateName
	default:
		if status.Message != "" {
			return errors.New(status.Message)
		}
		return errors.New(http.StatusText(int(status.Code)))
	}
}
