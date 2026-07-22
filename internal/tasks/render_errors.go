package tasks

import (
	"errors"

	"github.com/flightctl/flightctl/internal/util/validation"
)

func isPermanentRenderError(err error) bool {
	if err == nil {
		return false
	}

	switch {
	case errors.Is(err, ErrUnknownConfigName):
		return true
	case errors.Is(err, ErrUnknownApplicationType):
		return true
	case errors.Is(err, validation.ErrForbiddenDevicePath):
		return true
	}

	return false
}
