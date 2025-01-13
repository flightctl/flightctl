package lifecycle

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
)

type Manager interface {
	Sync(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error
	AfterUpdate(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error
}

type Initializer interface {
	// Initialize ensures that the lifecycle manager is initialized.
	Initialize(ctx context.Context, status *v1alpha1.DeviceStatus) error
	// IsInitialized returns true if the lifecycle manager has been initialized.
	IsInitialized() bool
}
