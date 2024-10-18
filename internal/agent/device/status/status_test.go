package status

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// go test -benchmem -run=^$ -bench ^BenchmarkManager$ -memprofile memprofile.prof github.com/flightctl/flightctl/internal/agent/device/status
func BenchmarkAggregateDeviceStatus(b *testing.B) {
	require := require.New(b)
	log := log.NewPrefixLogger("test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctrl := gomock.NewController(b)
	execMock := executer.NewMockExecuter(ctrl)
	resourceManagerMock := resource.NewMockManager(ctrl)
	hookManagerMock := hook.NewMockManager(ctrl)
	applicationsManagerMock := applications.NewMockManager(ctrl)
	execMock.EXPECT().LookPath("crictl").Return("/usr/bin/crictl", nil).AnyTimes()
	execMock.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/crictl", "ps", "-a", "--output", "json").Return(crioListResult, "", 0).AnyTimes()
	execMock.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "list-units", "--all", "--output", "json", "crio.service").Return(systemdUnitListResult, "", 0).AnyTimes()

	manager := NewManager("test", resourceManagerMock, hookManagerMock, applicationsManagerMock, execMock, log)
	systemdPatterns := []string{"crio.service"}

	spec := &v1alpha1.RenderedDeviceSpec{
		Systemd: &struct {
			MatchPatterns *[]string `json:"matchPatterns,omitempty"`
		}{
			MatchPatterns: &systemdPatterns,
		},
	}

	manager.SetProperties(spec)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		status := manager.Get(ctx)
		require.NotNil(status)
	}
}
