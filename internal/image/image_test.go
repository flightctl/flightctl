package image

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBootcHost(t *testing.T) {
	require := require.New(t)
	statusBytes, err := os.ReadFile("testdata/bootc_status.json")
	require.NoError(err)

	var status BootcHost
	err = json.Unmarshal(statusBytes, &status)
	require.NoError(err)

	// spec image
	require.Equal("quay.io/flightctl/flightctl-agent-basic-nginx", status.Spec.Image.Image)
	// transport
	require.Equal("registry", status.Spec.Image.Transport)
	// booted
	require.Equal("quay.io/flightctl/flightctl-agent-fedora", status.Status.Booted.Details.Spec.Image)
	// rollback image
	require.Equal("quay.io/flightctl/flightctl-agent-basic-nginx", status.Status.Rollback.Details.Spec.Image)
	// staged image
	require.Equal("quay.io/flightctl/flightctl-agent-basic-nginx", status.Status.Staged.Details.Spec.Image)
	// version
	require.Equal("stream9.20240224.0", status.Status.Staged.Details.Version)
	// timestamp
	require.Equal("", status.Status.Staged.Details.Timestamp)
	// ostree checksum
	require.Equal("f627c830e921afe918402486d5fe8a7ffaf3bd8c0d21311cba28facc9b17b9e2", status.Status.Staged.Ostree.Checksum)
	// pinned
	require.Equal(false, status.Status.Staged.Pinned)
	// deploy serial
	require.Equal(4, status.Status.Staged.Ostree.DeploySerial)

}
