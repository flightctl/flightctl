package policy

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
)

type Type string

const (
	Update   Type = "update"
	Download Type = "download"
)

// Manager manages device policies for scheduling updates.
type Manager interface {
	Sync(ctx context.Context, device *v1alpha1.Device) error
	IsReady(ctx context.Context, policyType Type, device *v1alpha1.Device) bool
	IsVersionReady(ctx context.Context, device *v1alpha1.Device) (bool, *time.Time, error)
	SetCurrentDevice(ctx context.Context, device *v1alpha1.Device) error
}
