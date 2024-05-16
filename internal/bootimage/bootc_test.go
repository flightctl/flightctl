package bootimage

import (
	_ "embed"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

//go:embed testdata/bootc_status.json
var bootcRollbackStatus string

func TestBootcHost(t *testing.T) {
	require := require.New(t)
	var status BootcHost
	err := json.Unmarshal([]byte(bootcRollbackStatus), &status)
	require.NoError(err)

	// spec image
	require.Equal("quay.io/flightctl/flightctl-agent-centos:latest", status.Spec.Image.Image)
	// transport
	require.Equal("registry", status.Spec.Image.Transport)
	// booted
	require.Equal("quay.io/flightctl/flightctl-agent-centos:latest", status.Status.Booted.Image.Image.Image)
	// rollback image
	require.Equal("quay.io/flightctl/flightctl-agent-centos:latest", status.Status.Rollback.Image.Image.Image)
	// staged image
	require.Equal("", status.Status.Staged.Image.Image.Image)
	// version
	require.Equal("stream9.20240503.0", status.Status.Booted.Image.Version)
	// timestamp
	require.Equal("", status.Status.Staged.Image.Timestamp)
	// ostree checksum
	require.Equal("8570a1f1fd63277e4f99a191128b6960172c7e227bc500fbb30000c633f87060", status.Status.Booted.Ostree.Checksum)
	// pinned
	require.Equal(false, status.Status.Booted.Pinned)
	// deploy serial
	require.Equal(0, status.Status.Booted.Ostree.DeploySerial)

}
