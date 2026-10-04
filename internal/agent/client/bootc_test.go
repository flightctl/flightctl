package client

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestBootcSwitchStagingMode(t *testing.T) {
	const (
		layoutDir = "/tmp/oci-layout"
		image     = "quay.io/flightctl/os:v1"
	)

	registryTarget, err := container.ImageToBootcTarget(image)
	require.NoError(t, err)

	testCases := []struct {
		name         string
		args         []any
		switchTarget func(Bootc) error
	}{
		{
			name: "When staging a reconstructed OCI image it should keep it download-only",
			args: []any{"switch", "--transport", "oci", "--download-only", "--retain", layoutDir},
			switchTarget: func(bootc Bootc) error {
				return bootc.SwitchOCI(context.Background(), layoutDir)
			},
		},
		{
			name: "When re-pinning the registry image it should finalize the deployment",
			args: []any{"switch", "--transport", "registry", "--retain", registryTarget},
			switchTarget: func(bootc Bootc) error {
				return bootc.SwitchRegistry(context.Background(), image)
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockExecuter := executer.NewMockExecuter(ctrl)
			mockExecuter.EXPECT().ExecuteWithContext(gomock.Any(), BootcCmd, testCase.args...).Return("", "", 0)

			bootc := NewBootc(log.NewPrefixLogger("test"), mockExecuter)
			require.NoError(t, testCase.switchTarget(bootc))
		})
	}
}
