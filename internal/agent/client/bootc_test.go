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

func TestBootcSwitchUsesContainersStorage(t *testing.T) {
	const image = "quay.io/flightctl/os:v1"

	target, err := container.ImageToBootcTarget(image)
	require.NoError(t, err)

	ctrl := gomock.NewController(t)
	mockExecuter := executer.NewMockExecuter(ctrl)
	mockExecuter.EXPECT().ExecuteWithContext(
		gomock.Any(),
		BootcCmd,
		"switch", "--transport", "containers-storage", "--retain", target,
	).Return("", "", 0)

	bootc := NewBootc(log.NewPrefixLogger("test"), mockExecuter)
	require.NoError(t, bootc.Switch(context.Background(), image))
}

func TestBootcSwitchOCIUsesDownloadOnlyWhenSupported(t *testing.T) {
	const layoutDir = "/var/tmp/os-delta/image"
	testCases := []struct {
		name               string
		helpOutput         string
		helpError          string
		helpExitCode       int
		expectedSwitchArgs []any
	}{
		{
			name:         "When Bootc supports download-only it should use the flag for the OCI switch",
			helpOutput:   "Usage: bootc switch [OPTIONS]...\n      --download-only",
			helpExitCode: 0,
			expectedSwitchArgs: []any{
				"switch", "--transport", "oci", "--retain", "--download-only", layoutDir,
			},
		},
		{
			name:         "When Bootc does not support download-only it should omit the flag",
			helpOutput:   "Usage: bootc switch [OPTIONS]...\n      --retain",
			helpExitCode: 0,
			expectedSwitchArgs: []any{
				"switch", "--transport", "oci", "--retain", layoutDir,
			},
		},
		{
			name:         "When Bootc help fails it should omit the flag",
			helpError:    "unsupported help option",
			helpExitCode: 1,
			expectedSwitchArgs: []any{
				"switch", "--transport", "oci", "--retain", layoutDir,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockExecuter := executer.NewMockExecuter(ctrl)
			helpCall := mockExecuter.EXPECT().ExecuteWithContext(
				gomock.Any(), BootcCmd, "switch", "--help",
			).Return(tc.helpOutput, tc.helpError, tc.helpExitCode)
			switchCall := mockExecuter.EXPECT().ExecuteWithContext(
				gomock.Any(), BootcCmd, tc.expectedSwitchArgs...,
			).Return("", "", 0)
			gomock.InOrder(helpCall, switchCall)

			bootc := NewBootc(log.NewPrefixLogger("test"), mockExecuter)
			require.NoError(t, bootc.SwitchOCI(context.Background(), layoutDir))
		})
	}
}

func TestBootcSwitchRegistryUsesCanonicalRegistryReference(t *testing.T) {
	const image = "quay.io/flightctl/os:v1"

	ctrl := gomock.NewController(t)
	mockExecuter := executer.NewMockExecuter(ctrl)
	mockExecuter.EXPECT().ExecuteWithContext(
		gomock.Any(),
		BootcCmd,
		"switch", "--transport", "registry", "--retain", image,
	).Return("", "", 0)

	bootc := NewBootc(log.NewPrefixLogger("test"), mockExecuter)
	require.NoError(t, bootc.SwitchRegistry(context.Background(), image))
}
