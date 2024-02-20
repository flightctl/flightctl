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
	testutils "github.com/flightctl/flightctl/test/utils"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestDeviceAgent(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name string
	}{
		{
			name: "TestDeviceAgent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			testDirPath := t.TempDir()
			err := makeTestDirs(testDirPath, []string{"/etc/issue.d/"})
			require.NoError(err)

			serverCfg := *config.NewDefault()
			serverLog := log.InitLogs()

			// create store
			store, dbName := testutils.NewTestStore(t, serverCfg, serverLog)
			serverCfg.Database.Name = dbName

			// create certs
			serverCfg.Service.CertStore = testDirPath
			ca, serverCerts, clientCerts := testutils.NewTestCerts(t, &serverCfg)

			// create server
			server, listener := testutils.NewTestServer(t, serverLog, &serverCfg, store, ca, serverCerts)
			serverCfg.Service.Address = listener.Addr().String()
			defer listener.Close()

			// start server
			go func() {
				err := server.Run()
				require.NoError(err)
			}()

			fetchSpecInterval := 1 * time.Second
			statusUpdateInterval := 1 * time.Second
			configSyncInterval := 1 * time.Second

			cfg := NewDefault()
			cfg.CertDir = testDirPath
			cfg.EnrollmentEndpoint = "https://" + serverCfg.Service.Address
			cfg.ManagementEndpoint = "https://" + serverCfg.Service.Address
			cfg.FetchSpecInterval = util.Duration(fetchSpecInterval)
			cfg.StatusUpdateInterval = util.Duration(statusUpdateInterval)
			cfg.ConfigSyncInterval = util.Duration(configSyncInterval)
			cfg.SetTestRootDir(testDirPath)

			agentLog := log.InitLogs()

			agentInstance := New(agentLog, cfg)

			// start agent
			go func() {
				err := agentInstance.Run(ctx)
				require.NoError(err)
			}()

			// create client
			client, err := testutils.NewClient("https://"+listener.Addr().String(), ca.Config, clientCerts)
			require.NoError(err)

			var deviceName string
			// wait for the enrollment request to be created
			err = wait.PollImmediate(100*time.Millisecond, 10000*time.Second, func() (bool, error) {
				listResp, err := client.ListEnrollmentRequestsWithResponse(ctx, &v1alpha1.ListEnrollmentRequestsParams{})
				if err != nil {
					return false, err
				}
				if len(listResp.JSON200.Items) == 0 {
					return false, nil
				}
				deviceName = *listResp.JSON200.Items[0].Metadata.Name
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

			// TODO: update device with inline config
			// wait for the device config to be written
			// err = wait.PollImmediate(100*time.Millisecond, 10000*time.Second, func() (bool, error) {
			// 	_, err := os.Stat(filepath.Join(testDirPath, "/etc/motd"))
			// 	if err != nil && os.IsNotExist(err) {
			// 		return false, nil
			// 	}
			// 	return true, nil
			// })
			// require.NoError(err)
		})
	}
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
