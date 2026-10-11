package applications

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func TestManagerStatusDoesNotReportDesiredCachedParentDigest(t *testing.T) {
	m := &manager{log: log.NewPrefixLogger("test"), podmanMonitor: &PodmanMonitor{apps: map[string]Application{}}, kubernetesMonitor: &KubernetesMonitor{monitor: newMonitor(log.NewPrefixLogger("test"), "test")}, ociTargetCache: provider.NewOCITargetCache()}
	status := &v1beta1.DeviceStatus{}
	require.NoError(t, m.Status(context.Background(), status))
	require.Empty(t, status.Applications)
}
