package device

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/bootimage"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestEnsureEnrollment(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name               string
		enrollmentApproved bool
		wantErr            bool
	}{
		{
			name:               "happy path",
			enrollmentApproved: true,
		},
		{
			name:               "enrollment not approved",
			enrollmentApproved: false,
			wantErr:            true,
		},
		{
			name:               "enrollment request not found",
			enrollmentApproved: false,
			wantErr:            true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// initialize storage
			tmpDir := t.TempDir()
			err := os.MkdirAll(tmpDir+"/etc/flightctl", 0755)
			require.NoError(err)
			err = os.MkdirAll(tmpDir+"/var/lib/flightctl", 0755)
			require.NoError(err)

			writer := fileio.NewWriter()
			writer.SetRootdir(tmpDir)
			reader := fileio.NewReader()
			reader.SetRootdir(tmpDir)

			// create mock enrollment  server
			mockEnrollmentServer := createMockEnrollmentServer(t, tt.enrollmentApproved)
			defer mockEnrollmentServer.Close()
			enrollmentEndpoint := mockEnrollmentServer.URL
			httpClient, err := testutil.NewClient(enrollmentEndpoint, nil, nil)
			require.NoError(err)
			enrollmentClient := client.NewEnrollment(httpClient)

			// create mock management server
			mockManagementServer := createMockManagementServer(t, false)
			defer mockManagementServer.Close()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			statusManager := status.NewMockManager(ctrl)
			statusManager.EXPECT().Get(gomock.Any()).Return(&v1alpha1.DeviceStatus{}, nil).Times(1)

			imageManager := bootimage.NewMockManager(ctrl)
			log := flightlog.NewPrefixLogger("")

			backoff := wait.Backoff{
				Cap:      100 * time.Millisecond,
				Duration: 100 * time.Millisecond,
				Factor:   0,
				Steps:    1,
			}

			currentSpecFilePath := filepath.Join("/var/lib/flightctl", spec.CurrentFile)
			desiredSpecFilePath := filepath.Join("/var/lib/flightctl", spec.DesiredFile)

			execMock := executer.NewMockExecuter(ctrl)

			b := NewBootstrap(
				"test-device",
				execMock,
				writer,
				reader,
				[]byte("test-csr"), // TODO: use real csr
				statusManager,
				imageManager,
				enrollmentClient,
				mockManagementServer.URL,
				"",
				"",
				"",
				"/etc/flightctl/agent.crt",
				backoff,
				currentSpecFilePath,
				desiredSpecFilePath,
				log,
			)
			err = b.ensureEnrollment(context.Background())
			if tt.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

var _ = Describe("Calling osimages Sync", func() {
	var (
		ctx                 context.Context
		ctrl                *gomock.Controller
		execMock            *executer.MockExecuter
		statusManager       *status.MockManager
		imageManager        *bootimage.MockManager
		log                 *flightlog.PrefixLogger
		tmpDir              string
		currentSpecFilePath string
		desiredSpecFilePath string
		writer              *fileio.Writer
		bootstrap           *Bootstrap
		defaultRenderedData []byte
	)

	BeforeEach(func() {
		ctx = context.Background()
		log = flightlog.NewPrefixLogger("")
		ctrl = gomock.NewController(GinkgoT())
		execMock = executer.NewMockExecuter(ctrl)
		statusManager = status.NewMockManager(ctrl)
		imageManager = bootimage.NewMockManager(ctrl)

		// initialize storage
		tmpDir = GinkgoT().TempDir()
		err := os.MkdirAll(filepath.Join(tmpDir, "/var/lib/flightctl"), 0755)
		Expect(err).ToNot(HaveOccurred())
		writer = fileio.NewWriter()
		writer.SetRootdir(tmpDir)
		reader := fileio.NewReader()
		reader.SetRootdir(tmpDir)
		currentSpecFilePath = filepath.Join("/var/lib/flightctl", spec.CurrentFile)
		desiredSpecFilePath = filepath.Join("/var/lib/flightctl", spec.DesiredFile)

		renderedConfig := v1alpha1.RenderedDeviceSpec{
			Os: &v1alpha1.DeviceOSSpec{
				Image: "image",
			},
			Config:          util.StrToPtr("config stuff"),
			Owner:           "myfleet",
			TemplateVersion: "tv",
		}
		defaultRenderedData, err = json.Marshal(renderedConfig)
		Expect(err).ToNot(HaveOccurred())

		// create certs
		serverCfg := config.NewDefault()
		serverCfg.Service.CertStore = tmpDir

		bootstrap = NewBootstrap(
			"device",
			execMock,
			writer,
			reader,
			[]byte(""),
			statusManager,
			imageManager,
			nil,
			"",
			"",
			"",
			"",
			"",
			wait.Backoff{},
			currentSpecFilePath,
			desiredSpecFilePath,
			log,
		)
	})

	AfterEach(func() {
		ctrl.Finish()
	})

	Context("When the current spec does not exist", func() {
		It("should return with no action", func() {
			err := writer.WriteFile(desiredSpecFilePath, defaultRenderedData, 0600)
			Expect(err).ToNot(HaveOccurred())
			err = bootstrap.ensureCurrentRenderedSpecUpToDate(ctx)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When the desired spec does not exist", func() {
		It("should return with no action", func() {
			err := writer.WriteFile(currentSpecFilePath, defaultRenderedData, 0600)
			Expect(err).ToNot(HaveOccurred())
			err = bootstrap.ensureCurrentRenderedSpecUpToDate(ctx)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When the OS hasn't changed", func() {
		It("should return with no action", func() {
			err := writer.WriteFile(currentSpecFilePath, defaultRenderedData, 0600)
			Expect(err).ToNot(HaveOccurred())
			err = writer.WriteFile(desiredSpecFilePath, defaultRenderedData, 0600)
			Expect(err).ToNot(HaveOccurred())
			imageManager.EXPECT().IsDisabled().Return(false)
			err = bootstrap.ensureCurrentRenderedSpecUpToDate(ctx)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When the current spec does not exist", func() {
		It("should return with no action", func() {
			err := writer.WriteFile(desiredSpecFilePath, defaultRenderedData, 0600)
			Expect(err).ToNot(HaveOccurred())
			err = bootstrap.ensureCurrentRenderedSpecUpToDate(ctx)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When the desired spec does not exist", func() {
		It("should return with no action", func() {
			err := writer.WriteFile(currentSpecFilePath, defaultRenderedData, 0600)
			Expect(err).ToNot(HaveOccurred())
			err = bootstrap.ensureCurrentRenderedSpecUpToDate(ctx)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When image manager is disabled", func() {
		It("should not call any image manager methods", func() {
			err := writer.WriteFile(currentSpecFilePath, defaultRenderedData, 0600)
			Expect(err).ToNot(HaveOccurred())
			err = writer.WriteFile(desiredSpecFilePath, defaultRenderedData, 0600)
			Expect(err).ToNot(HaveOccurred())

			imageManager.EXPECT().IsDisabled().Return(true)

			err = bootstrap.ensureCurrentRenderedSpecUpToDate(ctx)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("When the OS changed and was reconciled", func() {
		It("should write desired spec to file", func() {
			desiredConfig := v1alpha1.RenderedDeviceSpec{
				Os: &v1alpha1.DeviceOSSpec{
					Image: "newimage",
				},
			}
			desiredRenderedData, err := json.Marshal(desiredConfig)
			Expect(err).ToNot(HaveOccurred())

			err = writer.WriteFile(currentSpecFilePath, defaultRenderedData, 0600)
			Expect(err).ToNot(HaveOccurred())
			err = writer.WriteFile(desiredSpecFilePath, desiredRenderedData, 0600)
			Expect(err).ToNot(HaveOccurred())

			// New image was booted
			imageManager.EXPECT().IsDisabled().Return(false)
			imageManager.EXPECT().Status(gomock.Any()).Return(testutil.CreateTestImageManagerBootedStatus("newimage"), nil)

			err = bootstrap.ensureCurrentRenderedSpecUpToDate(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Current should equal desired
			fileData, err := os.ReadFile(filepath.Join(tmpDir, currentSpecFilePath))
			Expect(err).ToNot(HaveOccurred())
			Expect(fileData).To(Equal(desiredRenderedData))
		})
	})

	Context("When the OS changed and was not reconciled", func() {
		It("should set degraded status", func() {
			desiredConfig := v1alpha1.RenderedDeviceSpec{
				Os: &v1alpha1.DeviceOSSpec{
					Image: "newimage",
				},
			}
			desiredRenderedData, err := json.Marshal(desiredConfig)
			Expect(err).ToNot(HaveOccurred())

			err = writer.WriteFile(currentSpecFilePath, defaultRenderedData, 0600)
			Expect(err).ToNot(HaveOccurred())
			err = writer.WriteFile(desiredSpecFilePath, desiredRenderedData, 0600)
			Expect(err).ToNot(HaveOccurred())

			// Old image was booted
			imageManager.EXPECT().IsDisabled().Return(false)
			imageManager.EXPECT().Status(gomock.Any()).Return(testutil.CreateTestImageManagerBootedStatus("image"), nil)
			statusManager.EXPECT().UpdateConditionError(gomock.Any(), BootedWithUnexpectedImage, fmt.Errorf("booted image image, expected newimage"))

			err = bootstrap.ensureCurrentRenderedSpecUpToDate(ctx)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})

func createMockEnrollmentServer(t *testing.T, approved bool) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/enrollmentrequests/test-device" {
			var condition v1alpha1.Condition
			var certificate *string
			if approved {
				condition = v1alpha1.Condition{
					Type: "Approved",
				}
				certificate = util.StrToPtr(testCert)
			} else {
				condition = v1alpha1.Condition{
					Type: "Failed",
				}
			}

			conditions := []v1alpha1.Condition{
				condition,
			}

			status := v1alpha1.EnrollmentRequestStatus{
				Conditions:  &conditions,
				Certificate: certificate,
			}

			resp := v1alpha1.EnrollmentRequest{
				Status: &status,
			}
			// handle get enrollment request
			bytes, err := json.Marshal(resp)
			if err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, err = w.Write(bytes)
			if err != nil {
				t.Fatal(err)
			}
			return
		}

		approvalBytes, err := json.Marshal(testutil.TestEnrollmentApproval())
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, err = w.Write(approvalBytes)
		if err != nil {
			t.Fatal(err)
		}
	}))
}

func createMockManagementServer(t *testing.T, noChange bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mockTemplateVersion := "mockTemplateVersion"
		mockOwner := "mockOwner"
		resp := v1alpha1.RenderedDeviceSpec{
			Owner:           mockOwner,
			TemplateVersion: mockTemplateVersion,
		}

		w.Header().Set("Content-Type", "application/json")
		if noChange {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
		respBytes, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		_, err = w.Write(respBytes)
		if err != nil {
			t.Fatal(err)
		}
	}))
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
