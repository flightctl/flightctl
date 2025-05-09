package agent

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ccoveille/go-safecast"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/flightctl/flightctl/test/harness"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"go.uber.org/mock/gomock"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const (
	TIMEOUT = "60s"
	POLLING = "250ms"
)

func TestAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Suite")
}

var _ = Describe("Device Agent behavior", func() {
	var (
		h *harness.TestHarness
	)

	BeforeEach(func() {
		var err error
		h, err = harness.NewTestHarness(GinkgoT().TempDir(), func(err error) {
			// this inline function handles any errors that are returned from go routines
			fmt.Fprintf(os.Stderr, "Error in test harness go routine: %v\n", err)
			GinkgoWriter.Printf("Error in go routine: %v\n", err)
			GinkgoRecover()
		})
		// check for test harness creation errors
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		h.Cleanup()
	})

	Context("enrollment", func() {
		It("should submit a request for enrollment", func() {
			deviceName := ""
			Eventually(getEnrollmentDeviceName, TIMEOUT, POLLING).WithArguments(h, &deviceName).Should(BeTrue())
		})

		When("an enrollment request is approved", func() {
			It("should mark enrollment request as approved", func() {
				deviceName := ""
				Eventually(getEnrollmentDeviceName, TIMEOUT, POLLING).WithArguments(h, &deviceName).Should(BeTrue())
				approveEnrollment(h, deviceName, testutil.TestEnrollmentApproval())

				// verify that the enrollment request is marked as approved
				er, err := h.Client.GetEnrollmentRequestWithResponse(h.Context, deviceName)
				Expect(err).ToNot(HaveOccurred())
				Expect(er.JSON200.Status.Conditions).ToNot(BeEmpty())

				Expect(v1alpha1.IsStatusConditionTrue(er.JSON200.Status.Conditions, "Approved")).To(BeTrue())

			})

			It("should create a device", func() {
				dev := enrollAndWaitForDevice(h, testutil.TestEnrollmentApproval())
				Expect(dev.Metadata.Name).NotTo(BeNil())
			})

			It("should create a device, with the approval labels", func() {
				// craft some specific labels we will test for in the device
				approval := testutil.TestEnrollmentApproval()
				const (
					TEST_LABEL_1 = "label-1"
					TEST_VALUE_1 = "value-1"
					TEST_LABEL_2 = "label-2"
					TEST_VALUE_2 = "value-2"
				)
				approval.Labels = &map[string]string{TEST_LABEL_1: TEST_VALUE_1, TEST_LABEL_2: TEST_VALUE_2}

				dev := enrollAndWaitForDevice(h, approval)

				Expect(*dev.Metadata.Labels).To(HaveKeyWithValue(TEST_LABEL_1, TEST_VALUE_1))
				Expect(*dev.Metadata.Labels).To(HaveKeyWithValue(TEST_LABEL_2, TEST_VALUE_2))
			})

			It("should write the agent.crt to the device", func() {
				dev := enrollAndWaitForDevice(h, testutil.TestEnrollmentApproval())

				GinkgoWriter.Printf(
					"Waiting for agent.crt file to be created on the device %s, with testDirPath: %s\n",
					*dev.Metadata.Name, h.TestDirPath)

				var fileInfo fs.FileInfo
				Eventually(func() bool {
					var err error
					fileInfo, err = os.Stat(filepath.Join(h.TestDirPath, "/var/lib/flightctl/certs/agent.crt"))
					if err != nil && os.IsNotExist(err) {
						return false
					}
					return true
				}, TIMEOUT, POLLING).Should(BeTrue())

				Expect(fileInfo.Mode()).To(Equal(os.FileMode(0600)))
			})

		})

		When("updating the agent device spec", func() {
			It("should write any files to the device", func(ctx context.Context) {
				const (
					firstSecretKey    = "first-secret"
					firstSecretValue  = "This is the first secret"
					secondSecretKey   = "second-secret"
					secondSecretValue = "Second secret"
				)
				secrets := map[string]string{
					firstSecretKey:  firstSecretValue,
					secondSecretKey: secondSecretValue,
				}
				mockSecret(h.GetMockK8sClient(), secrets)
				resp, err := h.Client.CreateFleetWithResponse(h.Context, getTestFleet("fleet.yaml"))
				Expect(err).ToNot(HaveOccurred())
				Expect(resp.HTTPResponse.StatusCode).To(Equal(http.StatusCreated))
				approval := testutil.TestEnrollmentApproval()
				approval.Labels = &map[string]string{"fleet": "default"}

				dev := enrollAndWaitForDevice(h, approval)

				waitForFile("/var/lib/flightctl/certs/agent.crt", *dev.Metadata.Name, h.TestDirPath, nil, nil)
				waitForFile("/etc/motd", *dev.Metadata.Name, h.TestDirPath, lo.ToPtr("This system is managed by flightctl."), lo.ToPtr(0o0600))
				waitForFile("/etc/testdir/encoded", *dev.Metadata.Name, h.TestDirPath, lo.ToPtr("This text is encoded."), lo.ToPtr(0o1775))

				for key, value := range secrets {
					fname := filepath.Join("/etc/secret/secretMountPath", key)
					waitForFile(fname, *dev.Metadata.Name, h.TestDirPath, &value, lo.ToPtr(0644))
				}
			})
		})

		When("agent begins enrollment, and the enrollment is approved while the device is shutdown", func() {
			It("the agent should start and complete enrollment successfully", func() {
				deviceName := ""
				Eventually(getEnrollmentDeviceName, TIMEOUT, POLLING).WithArguments(h, &deviceName).Should(BeTrue())

				// shut down the agent before approving the enrollment request
				GinkgoWriter.Printf("Agent has requested enrollment: %s, shutting it down\n", deviceName)
				h.StopAgent()

				// while the agent is down, we approve the enrollment for the device
				approveEnrollment(h, deviceName, testutil.TestEnrollmentApproval())

				// start the agent again
				h.StartAgent()

				// wait for the agent to retrieve the agent certificate from the EnrollmentRequest
				Eventually(h.AgentDownloadedCertificate, TIMEOUT, POLLING).Should(BeTrue())
			})
		})

	})
})

func assertRestResponse(expectedCode int, resp *http.Response, body []byte) {
	if resp.StatusCode != expectedCode {
		fmt.Printf("REST call returned %d: %s\n", resp.StatusCode, body)
	}
	Expect(resp.StatusCode).To(Equal(expectedCode))
}

func enrollAndWaitForDevice(h *harness.TestHarness, approval *v1alpha1.EnrollmentRequestApproval) *v1alpha1.Device {
	deviceName := ""
	Eventually(getEnrollmentDeviceName, TIMEOUT, POLLING).WithArguments(h, &deviceName).Should(BeTrue())
	approveEnrollment(h, deviceName, approval)

	// verify that the device is created
	dev, err := h.Client.GetDeviceWithResponse(h.Context, deviceName)
	Expect(err).ToNot(HaveOccurred())
	assertRestResponse(200, dev.HTTPResponse, dev.Body)
	return dev.JSON200
}

func approveEnrollment(h *harness.TestHarness, deviceName string, approval *v1alpha1.EnrollmentRequestApproval) {
	Expect(approval).NotTo(BeNil())
	GinkgoWriter.Printf("Approving device enrollment: %s\n", deviceName)
	resp, err := h.Client.ApproveEnrollmentRequestWithResponse(h.Context, deviceName, *approval)
	Expect(err).ToNot(HaveOccurred())
	assertRestResponse(200, resp.HTTPResponse, resp.Body)
}

func getEnrollmentDeviceName(h *harness.TestHarness, deviceName *string) bool {
	listResp, err := h.Client.ListEnrollmentRequestsWithResponse(h.Context, &v1alpha1.ListEnrollmentRequestsParams{})
	Expect(err).ToNot(HaveOccurred())
	assertRestResponse(200, listResp.HTTPResponse, listResp.Body)

	if len(listResp.JSON200.Items) == 0 {
		return false
	}

	Expect(*listResp.JSON200.Items[0].Metadata.Name).ToNot(BeEmpty())
	*deviceName = *listResp.JSON200.Items[0].Metadata.Name
	return true
}

func getTestFleet(fleetYaml string) v1alpha1.Fleet {
	fleetBytes, err := os.ReadFile(filepath.Join("testdata", fleetYaml))
	Expect(err).ToNot(HaveOccurred())

	var fleet v1alpha1.Fleet
	err = yaml.Unmarshal(fleetBytes, &fleet)
	Expect(err).ToNot(HaveOccurred())

	return fleet
}

func mockSecret(mockK8sClient *k8sclient.MockK8SClient, secrets map[string]string) {
	mockK8sClient.EXPECT().GetSecret(gomock.Any(), "secret-namespace", "secret").
		Return(&v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "secret",
				Namespace: "secret-namespace",
			},
			Data: lo.MapValues(secrets, func(v, _ string) []byte {
				return []byte(v)
			}),
		}, nil).AnyTimes()
}

func waitForFile(path, devName, testDirPath string, contents *string, mode *int) {
	var fileInfo fs.FileInfo
	var err error

	GinkgoWriter.Printf(
		"Waiting for file %s to be created on the device %s, with testDirPath: %s\n", path, devName, testDirPath)
	Eventually(func() bool {
		fileInfo, err = os.Stat(filepath.Join(testDirPath, path))
		if err != nil && os.IsNotExist(err) {
			return false
		}
		return true
	}, TIMEOUT, POLLING).Should(BeTrue())

	Expect(fileInfo.IsDir()).To(Equal(false))

	if mode != nil {
		filemode, err := safecast.ToUint32(*mode)
		Expect(err).To(BeNil())
		Expect(fileInfo.Mode().Perm()).To(Equal(os.FileMode(filemode).Perm()))
		if *mode&0o1000 != 0 {
			Expect(fileInfo.Mode() & os.ModeSticky).ToNot(Equal(0))
		}
		if *mode&0o2000 != 0 {
			Expect(fileInfo.Mode() & os.ModeSetgid).ToNot(Equal(0))
		}
		if *mode&0o4000 != 0 {
			Expect(fileInfo.Mode() & os.ModeSetuid).ToNot(Equal(0))
		}
	}
	if contents != nil {
		Expect(os.ReadFile(filepath.Join(testDirPath, path))).To(Equal([]byte(*contents)))
	}
}
