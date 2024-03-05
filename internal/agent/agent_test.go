package agent_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/test/harness"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"
)

func TestDeviceAgent(t *testing.T) {

	require := require.New(t)

	testDirPath := t.TempDir()
	defer t.Cleanup(func() {
		_ = os.RemoveAll(testDirPath)
	})
	h, err := harness.NewTestHarness(testDirPath, func(err error) {
		// this inline function handles any errors that are returned from go routines
		require.NoError(err)
	})
	require.NoError(err)
	defer h.Cleanup()

	var deviceName string
	// wait for the enrollment request to be created
	err = wait.PollImmediate(100*time.Millisecond, 120*time.Second, func() (bool, error) {
		listResp, err := h.Client.ListEnrollmentRequestsWithResponse(h.Context, &v1alpha1.ListEnrollmentRequestsParams{})
		if err != nil {
			return false, err
		}
		if len(listResp.JSON200.Items) == 0 {
			return false, nil
		}
		deviceName = *listResp.JSON200.Items[0].Metadata.Name
		if deviceName == "" {
			return false, nil
		}
		return true, nil
	})
	require.NoError(err)

	// approve the enrollment request
	approval := v1alpha1.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &map[string]string{"label": "value"},
		Region:   util.StrToPtr("region"),
	}
	_, err = h.Client.CreateEnrollmentRequestApprovalWithResponse(h.Context, deviceName, approval)
	require.NoError(err)

	// wait for the enrollment request to be approved
	err = wait.PollImmediate(100*time.Millisecond, 120*time.Second, func() (bool, error) {
		listResp, err := h.Client.ListEnrollmentRequestsWithResponse(h.Context, &v1alpha1.ListEnrollmentRequestsParams{})
		if err != nil {
			return false, err
		}
		if len(listResp.JSON200.Items) == 0 {
			return false, nil
		}
		for _, cond := range *listResp.JSON200.Items[0].Status.Conditions {
			if cond.Type == "Approved" {
				return true, nil
			}
		}
		return false, nil
	})
	require.NoError(err)

	// get the device
	resp, err := h.Client.ReadDeviceStatusWithResponse(h.Context, deviceName)
	require.NoError(err)
	require.Equal(200, resp.StatusCode())
	device := *resp.JSON200

	// update the device spec to include an ignition config
	device.Spec, err = getTestSpec()
	require.NoError(err)

	_, err = h.Client.ReplaceDeviceWithResponse(h.Context, deviceName, device)
	require.NoError(err)

	// wait for the device config to be written
	err = wait.PollImmediate(100*time.Millisecond, 120*time.Second, func() (bool, error) {
		_, err := os.Stat(filepath.Join(h.TestDirPath, "/etc/motd"))
		if err != nil && os.IsNotExist(err) {
			return false, nil
		}
		return true, nil
	})
	require.NoError(err)
}

func getTestSpec() (v1alpha1.DeviceSpec, error) {
	deviceBytes, err := os.ReadFile(filepath.Join("testdata", "device.yaml"))
	if err != nil {
		return v1alpha1.DeviceSpec{}, err
	}

	var device v1alpha1.Device
	err = yaml.Unmarshal(deviceBytes, &device)
	if err != nil {
		return v1alpha1.DeviceSpec{}, err
	}
	return device.Spec, nil
}
