package bootimage

import (
	_ "embed"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

//go:embed testdata/rpm-ostree_status.json
var osTreeStatus string

//go:embed testdata/bootc_status.json
var bootcStatus string

func TestOsTreeImageStatus(t *testing.T) {
	require := require.New(t)
	var rpmOsTreeData RpmOsTreeStatus
	err := json.Unmarshal([]byte(osTreeStatus), &rpmOsTreeData)
	require.NoError(err)

	var bootcStatusData BootcHost
	err = json.Unmarshal([]byte(bootcStatus), &bootcStatusData)
	require.NoError(err)

	// ensure parity between bootc and rpm-ostree status
	for _, deployment := range rpmOsTreeData.Deployments {
		if deployment.Booted {
			booted, err := getOsTreeBootEntry(rpmOsTreeData.Deployments[0])
			require.NoError(err)
			require.NotNil(booted)
			// booted image
			require.Equal(bootcStatusData.Spec.Image.Image, booted.Image.Image.Image)
			require.Equal(bootcStatusData.Status.Booted.Image.Image.Image, booted.Image.Image.Image)
			// checksum
			require.Equal(bootcStatusData.Status.Booted.Ostree.Checksum, booted.Ostree.Checksum)
			// image version
			require.Equal(bootcStatusData.Status.Booted.Image.Version, booted.Image.Version)
			// pinned
			require.Equal(bootcStatusData.Status.Booted.Pinned, booted.Pinned)
			// deploy serial
			require.Equal(bootcStatusData.Status.Booted.Ostree.DeploySerial, booted.Ostree.DeploySerial)
		}
	}

}
