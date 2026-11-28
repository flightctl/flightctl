package tasks

import (
	"errors"

	"github.com/flightctl/flightctl/internal/util/validation"
)

var errUnsupportedConfigResources = errors.New("Device does not report support for registry configResources. Upgrade the agent before applying this configuration.")

func isPermanentRenderError(err error) bool {
	if err == nil {
		return false
	}

	switch {
	case errors.Is(err, errUnsupportedConfigResources):
		return true
	case errors.Is(err, ErrUnknownConfigName):
		return true
	case errors.Is(err, ErrUnknownApplicationType):
		return true
	case errors.Is(err, validation.ErrForbiddenDevicePath):
		return true
	}

	return false
}
