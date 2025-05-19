package policy

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
)

type Type string

const (
	Update   Type = "update"
	Download Type = "download"
)

type Manager interface {
	Sync(ctx context.Context, desired *v1alpha1.DeviceSpec) error
	IsReady(ctx context.Context, policyType Type) bool
}
