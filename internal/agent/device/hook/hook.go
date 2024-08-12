package hook

import (
	"errors"
)

const (
	SystemdActionType    = "systemd"
	ExecutableActionType = "executable"

	// FilePathKey is a placeholder which will be replaced with the file path
	FilePathKey = "FilePath"
	noValueKey  = "<no value>"
)

var (
	ErrInvalidTokenFormat             = errors.New("invalid token: formatting")
	ErrTokenNotSupported              = errors.New("invalid token: not supported")
	ErrActionTypeNotFound             = errors.New("failed to find action type")
	ErrUnsupportedFilesystemOperation = errors.New("unsupported filesystem operation")
)
