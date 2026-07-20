package restore

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/stretchr/testify/require"
)

func TestNewDeviceAwaitingReconnectPrepareParams(t *testing.T) {
	t.Run("When building device prepare params it should match today's restore product constants", func(t *testing.T) {
		got := NewDeviceAwaitingReconnectPrepareParams()
		require.Equal(t, domain.DeviceAnnotationAwaitingReconnect, got.AnnotationKey)
		require.Equal(t, string(domain.DeviceSummaryStatusAwaitingReconnect), got.SummaryStatus)
		require.Equal(t, common.DeviceStatusInfoAwaitingReconnect, got.SummaryInfo)
		require.Equal(t, string(domain.DeviceUpdatedStatusUnknown), got.UpdatedStatus)
		require.Equal(t, []string{
			string(domain.DeviceLifecycleStatusDecommissioned),
			string(domain.DeviceLifecycleStatusDecommissioning),
		}, got.ExcludedLifecycleStatuses)
	})
}

func TestNewEnrollmentAwaitingReconnectPrepareParams(t *testing.T) {
	t.Run("When building enrollment prepare params it should use the awaiting reconnect annotation key", func(t *testing.T) {
		got := NewEnrollmentAwaitingReconnectPrepareParams()
		require.Equal(t, domain.DeviceAnnotationAwaitingReconnect, got.AnnotationKey)
	})
}
