package device

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestBootstrapInitialize(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name                    string
		enrollmentConditionType string
		desiredSpec             *spec.Rendered
		desiredBootedOsImage    string
		isOSUpdateInProgress    bool
		isOsImageReconciled     bool
		wantErr                 error
	}{
		// {
		// 	name:                    "enrollment approved initial bootstrap",
		// 	enrollmentConditionType: "Approved",
		// },
		{
			name:                    "enrollment request denied",
			enrollmentConditionType: "Denied",
			wantErr:                 ErrEnrollmentRequestDenied,
		},
		// {
		// 	name:                    "enrollment request failed",
		// 	enrollmentConditionType: "Failed",
		// 	wantErr:                 ErrEnrollmentRequestFailed,
		// },
		// {
		// 	name:                    "OS update in progress boot image reconciled",
		// 	enrollmentConditionType: "Approved",
		// 	isOSUpdateInProgress:    true,
		// 	isOsImageReconciled:     false,
		// 	desiredSpec: &spec.Rendered{
		// 		RenderedDeviceSpec: &v1alpha1.RenderedDeviceSpec{
		// 			RenderedVersion: "1",
		// 			Os: &v1alpha1.DeviceOSSpec{
		// 				Image: "newimage",
		// 			},
		// 		},
		// 	},
		// },
		// {
		// 	name:                    "OS update in progress boot image not reconciled",
		// 	enrollmentConditionType: "Approved",
		// 	isOSUpdateInProgress:    true,
		// 	isOsImageReconciled:     true,
		// 	desiredSpec: &spec.Rendered{
		// 		RenderedDeviceSpec: &v1alpha1.RenderedDeviceSpec{
		// 			RenderedVersion: "1",
		// 			Os: &v1alpha1.DeviceOSSpec{
		// 				Image: "newimage",
		// 			},
		// 		},
		// 	},
		// 	desiredBootedOsImage: "oldimage",
		// },
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// initialize storage
			tmpDir := t.TempDir()
			t.Setenv("FLIGHTCTL_TEST_ROOT_DIR", tmpDir)
			err := os.MkdirAll(tmpDir+"/etc/flightctl", 0755)
			require.NoError(err)
			err = os.MkdirAll(tmpDir+"/var/lib/flightctl", 0755)
			require.NoError(err)

			readWriter := fileio.NewReadWriter(fileio.WithTestRootDir(tmpDir))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockEnrollmentClient := client.NewMockEnrollment(ctrl)
			mockEnrollmentClient.EXPECT().CreateEnrollmentRequest(gomock.Any(), gomock.Any()).Return(&v1alpha1.EnrollmentRequest{}, nil).Times(1)

			statusManager := status.NewMockManager(ctrl)
			statusManager.EXPECT().Collect(gomock.Any()).Return(nil).Times(1)
			statusManager.EXPECT().Get(gomock.Any()).Return(&v1alpha1.DeviceStatus{}).Times(1)

			specManager := spec.NewMockManager(ctrl)
			specManager.EXPECT().Exists(gomock.Any()).Return(true, nil).Times(1)

			execMock := executer.NewMockExecuter(ctrl)

			// prepare enrollment request response
			var conditions []v1alpha1.Condition
			var certificate *string
			switch tt.enrollmentConditionType {
			case "Approved":
				specManager.EXPECT().Read(spec.SpecType("desired")).Return(tt.desiredSpec, nil).Times(1)
				specManager.EXPECT().Read(spec.SpecType("desired")).Return(tt.desiredSpec, nil).Times(1)

				specManager.EXPECT().IsOSUpdateInProgress().Return(tt.isOSUpdateInProgress, nil).Times(1)
				bootcHost := newTestBootcHost(tt.desiredBootedOsImage)

				// check if OS update is in progress
				if tt.isOSUpdateInProgress {
					hostJson, err := json.Marshal(bootcHost)
					require.NoError(err)
					execMock.EXPECT().ExecuteWithContext(gomock.Any(), container.CmdBootc, "status", "--json").Return(string(hostJson), "", 0)
					specManager.EXPECT().IsOsImageReconciled(gomock.Any()).Return(tt.isOsImageReconciled, nil).Times(1)

					// check if OS image is reconciled
					if tt.isOsImageReconciled {
						// check if OS image is reconciled
						specManager.EXPECT().Write(spec.SpecType("current"), gomock.Any()).Return(nil).Times(1)
						expectedFn := []status.UpdateStatusFn{
							status.SetOSImage(v1alpha1.DeviceOSStatus{
								Image: tt.desiredSpec.Os.Image,
							}),
							status.SetConfig(v1alpha1.DeviceConfigStatus{
								RenderedVersion: tt.desiredSpec.RenderedVersion,
							}),
						}
						statusManager.EXPECT().Update(gomock.Any(), gomock.AssignableToTypeOf(expectedFn)).Return(nil, nil).Times(1)
					} else {
						// os is dirty
						expectedStatus := v1alpha1.DeviceSummaryStatus{
							Status: v1alpha1.DeviceSummaryStatusDegraded,
							Info:   util.StrToPtr(fmt.Sprintf("Booted image %s, expected %s", bootcHost.GetBootedImage(), tt.desiredSpec.Os.Image)),
						}
						expectedSummary := status.SetDeviceSummary(expectedStatus)
						statusManager.EXPECT().Update(gomock.Any(), gomock.AssignableToTypeOf(expectedSummary)).Return(nil, nil).Times(1)
					}

				}

				// ensure status is updated as expected
				expectedStatus := v1alpha1.DeviceSummaryStatus{
					Status: v1alpha1.DeviceSummaryStatusOnline,
					Info:   util.StrToPtr("Bootstrap complete"),
				}
				expectedSummary := status.SetDeviceSummary(expectedStatus)
				statusManager.EXPECT().Update(gomock.Any(), gomock.AssignableToTypeOf(expectedSummary)).Return(nil, nil).Times(1)

				// populate certificate and conditions
				certificate = util.StrToPtr(testCert)
				conditions = append(conditions, v1alpha1.Condition{
					Type: "Approved",
				})

			case "Failed":
				conditions = append(conditions, v1alpha1.Condition{
					Type: "Failed",
				})
			case "Denied":
				conditions = append(conditions, v1alpha1.Condition{
					Type: "Denied",
				})
			}

			resp := &v1alpha1.EnrollmentRequest{
				Status: &v1alpha1.EnrollmentRequestStatus{
					Conditions:  conditions,
					Certificate: certificate,
				},
			}

			mockEnrollmentClient.EXPECT().GetEnrollmentRequest(gomock.Any(), gomock.Any()).Return(resp, nil).Times(1)

			log := flightlog.NewPrefixLogger("")

			backoff := wait.Backoff{
				Cap:      100 * time.Millisecond,
				Duration: 100 * time.Millisecond,
				Factor:   0,
				Steps:    1,
			}

			b := NewBootstrap(
				"test-device",
				execMock,
				readWriter,
				[]byte("test-csr"), // TODO: use real csr
				specManager,
				statusManager,
				mockEnrollmentClient,
				"mock.server.com:8080",
				client.NewDefault(),
				backoff,
				log,
				make(map[string]string),
			)
			b.managementClient = client.NewManagement(nil)

			err = b.Initialize(ctx)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
		})
	}
}

func newTestBootcHost(name string) container.BootcHost {
	return container.BootcHost{
		Status: container.Status{
			Booted: container.ImageStatus{
				Image: container.ImageDetails{
					Image: container.ImageSpec{
						Image: name,
					},
				},
			},
		},
	}
}

var testCert = `-----BEGIN CERTIFICATE-----
MIIDZTCCAk2gAwIBAgIUUvtAe9GojLepIB7/C7fX+zAqeEkwDQYJKoZIhvcNAQEL
BQAwQjELMAkGA1UEBhMCVVMxFTATBgNVBAcMDERlZmF1bHQgQ2l0eTEcMBoGA1UE
CgwTRGVmYXVsdCBDb21wYW55IEx0ZDAeFw0yNDAzMTIyMDUwMDlaFw0yNDAzMTMy
MDUwMDlaMEIxCzAJBgNVBAYTAlVTMRUwEwYDVQQHDAxEZWZhdWx0IENpdHkxHDAa
BgNVBAoME0RlZmF1bHQgQ29tcGFueSBMdGQwggEiMA0GCSqGSIb3DQEBAQUAA4IB
DwAwggEKAoIBAQC6h1wkMwNGea0N7YyCCvOgUXlCYFvUdQ0t/sVSrGRQyvNRifcq
xSeEVkiGOdUIKfEWNLhxgl/EQ9dwM2MszrYd2gC3IeUC1u8psd8jfTlsj9dR9tK/
Hrx7EC/Oa4SCsApK9C4BSSyMTbaNmmnX/z0k6trW8MXkC+pl/xzUwSxyNGYsMXZR
4GGCEi+PrtpGwO7c0S6ZYvy1j3OvxfnHy6r99X4duSG4yp+XS7nOYJFVysAABtfU
GxfI9CKEuXzOxg0xxkJit54FvQz+WcghXxmaEuDYWxZPcN9fKoK3swpYsHyQXr/P
eZpbc+lmjUTcp0UKsIujdA7jSPY/iUGzd1UHAgMBAAGjUzBRMB0GA1UdDgQWBBSE
42CFhnaO3wzzgMSTJFvFN4cZ2TAfBgNVHSMEGDAWgBSE42CFhnaO3wzzgMSTJFvF
N4cZ2TAPBgNVHRMBAf8EBTADAQH/MA0GCSqGSIb3DQEBCwUAA4IBAQAlFAi8OBsZ
a3KOSS8uomt7cL9Wxm8GWTIC4lQjvjfiVr9qxwaUMoxtNkHeHc2mPgMuPnYLy8zb
T07PwhiCJBq9t/fGwY7BGWPeshjhqeHN9RCNrwMbanrmATJKw0qKMpRz7RjPgwq6
qfOqV765fTEByQTh4L7ej0h9IbSNtG9EJZXq4W9+b1bMzUI0P5PWtKRzZF+Xrxh9
3TUXfKM90r7ezUFkCtapIqcBAfnZEnX0rAv3JOe33SNJIt/+8EDtw21C/hSqT54b
kwCcobAMr3v1/n03zADMmi+DOXlC9LWi9XCC/c16ionDvJY1Kg04FNRlH2s8IqxN
gBsqbosDC1bR
-----END CERTIFICATE-----`
