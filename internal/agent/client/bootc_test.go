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

func TestBootcSwitchOCIUsesOCITransportWithoutDownloadOnly(t *testing.T) {
	const layoutDir = "/var/tmp/os-delta/image"
	ctrl := gomock.NewController(t)
	mockExecuter := executer.NewMockExecuter(ctrl)
	mockExecuter.EXPECT().ExecuteWithContext(
		gomock.Any(),
		BootcCmd,
		"switch", "--transport", "oci", "--retain", layoutDir,
	).Return("", "", 0)

	bootc := NewBootc(log.NewPrefixLogger("test"), mockExecuter)
	require.NoError(t, bootc.SwitchOCI(context.Background(), layoutDir))
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
