package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"
)

func TestDeviceAgent(t *testing.T) {
	require := require.New(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testDirPath := t.TempDir()
	defer t.Cleanup(func() {
		_ = os.RemoveAll(testDirPath)
	})
	err := makeTestDirs(testDirPath, []string{"/etc/issue.d/"})
	require.NoError(err)

	serverCfg := *config.NewDefault()
	serverLog := log.InitLogs()

	// create store
	store, dbName := testutil.NewTestStore(t, serverCfg, serverLog)
	serverCfg.Database.Name = dbName

	// create certs
	serverCfg.Service.CertStore = testDirPath
	ca, serverCerts, clientCerts := testutil.NewTestCerts(t, &serverCfg)

	// create server
	server, listener := testutil.NewTestServer(t, serverLog, &serverCfg, store, ca, serverCerts)
	serverCfg.Service.Address = listener.Addr().String()
	defer listener.Close()

	// start server
	go func() {
		err := server.Run()
		require.NoError(err)
	}()

	fetchSpecInterval := 1 * time.Second
	statusUpdateInterval := 1 * time.Second

	cfg := NewDefault()
	cfg.CertDir = testDirPath
	cfg.EnrollmentServerEndpoint = "https://" + serverCfg.Service.Address
	cfg.EnrollmentUIEndpoint = "https://" + serverCfg.Service.Address
	cfg.ManagementServerEndpoint = "https://" + serverCfg.Service.Address
	cfg.FetchSpecInterval = util.Duration(fetchSpecInterval)
	cfg.StatusUpdateInterval = util.Duration(statusUpdateInterval)
	cfg.SetTestRootDir(testDirPath)

	agentLog := log.InitLogs()

	agentInstance := New(agentLog, cfg)

	// start agent
	go func() {
		err := agentInstance.Run(ctx)
		require.NoError(err)
	}()

	// create client to talk to the server
	client, err := testutil.NewClient("https://"+listener.Addr().String(), ca.Config, clientCerts)
	require.NoError(err)

	var deviceName string
	// wait for the enrollment request to be created
	err = wait.PollInfinite(100*time.Millisecond, func() (bool, error) {
		listResp, err := client.ListEnrollmentRequestsWithResponse(ctx, &v1alpha1.ListEnrollmentRequestsParams{})
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
	_, err = client.CreateEnrollmentRequestApprovalWithResponse(ctx, deviceName, approval)
	require.NoError(err)

	// wait for the enrollment request to be approved
	err = wait.PollInfinite(100*time.Millisecond, func() (bool, error) {
		listResp, err := client.ListEnrollmentRequestsWithResponse(ctx, &v1alpha1.ListEnrollmentRequestsParams{})
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
	resp, err := client.ReadDeviceStatusWithResponse(ctx, deviceName)
	require.NoError(err)
	require.Equal(200, resp.StatusCode())
	device := *resp.JSON200

	// update the device spec to include an ignition config
	device.Spec, err = getTestSpec()
	require.NoError(err)

	_, err = client.ReplaceDeviceWithResponse(ctx, deviceName, device)
	require.NoError(err)

	// wait for the device config to be written
	err = wait.PollInfinite(100*time.Millisecond, func() (bool, error) {
		_, err := os.Stat(filepath.Join(testDirPath, "/etc/motd"))
		if err != nil && os.IsNotExist(err) {
			return false, nil
		}
		return true, nil
	})
	require.NoError(err)
}

func makeTestDirs(tmpDirPath string, paths []string) error {
	for _, path := range paths {
		err := os.MkdirAll(filepath.Join(tmpDirPath, path), 0755)
		if err != nil {
			return err
		}
	}
	return nil
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
