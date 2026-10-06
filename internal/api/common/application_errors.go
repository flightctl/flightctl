package common

import "errors"

var (
	ErrNoComposeFile     = errors.New("no valid compose file found")
	ErrNoComposeServices = errors.New("no services found in compose spec")
	ErrNoQuadletFile     = errors.New("no quadlet file found")
)
