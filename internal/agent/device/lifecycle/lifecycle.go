package lifecycle

import (
	"context"

	"github.com/flightctl/flightctl/api/core/v1beta1"
)

type Manager interface {
	Sync(ctx context.Context, current, desired *v1beta1.DeviceSpec) error
	AfterUpdate(ctx context.Context, current, desired *v1beta1.DeviceSpec) error
}

type Initializer interface {
	// Initialize ensures that the lifecycle manager is initialized.
	Initialize(ctx context.Context, status *v1beta1.DeviceStatus) error
	// IsInitialized returns true if the lifecycle manager has been initialized.
	IsInitialized() bool
}
