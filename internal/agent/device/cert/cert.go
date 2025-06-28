package cert

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
)

type Manager interface {
	Sync(ctx context.Context, current, desired *v1alpha1.DeviceSpec) error
	SetClient(managementClient client.Management)
}
