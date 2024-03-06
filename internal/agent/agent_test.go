package agent_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/test/harness"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

const TIMEOUT = "20s"
const POLLING = "250ms"

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
			Expect(err).ToNot(HaveOccurred())
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
			Eventually(getEnrolledDeviceName, TIMEOUT, POLLING).WithArguments(h, &deviceName).Should(BeTrue())
		})

		When("an enrollment request is approved", func() {
			It("should mark enrollment resquest as approved", func() {
				deviceName := ""
				Eventually(getEnrolledDeviceName, TIMEOUT, POLLING).WithArguments(h, &deviceName).Should(BeTrue())
				approveEnrollment(h, deviceName, DefaultApproval())

				// verify that the enrollment request is marked as approved
				er, err := h.Client.ReadEnrollmentRequestWithResponse(h.Context, deviceName)
				Expect(err).ToNot(HaveOccurred())
				Expect(er.JSON200.Status.Conditions).ToNot(BeNil())

				Expect(v1alpha1.IsStatusConditionTrue(*er.JSON200.Status.Conditions, "Approved")).To(BeTrue())

			})

			It("should create a device", func() {
				dev := enrollAndWaitForDevice(h, DefaultApproval())
				Expect(dev.Metadata.Name).NotTo(BeNil())
			})

			It("should create a device, with the approval labels", func() {
				// craft some specific labels and region we will test for in the device
				approval := DefaultApproval()
				const (
					TEST_LABEL_1 = "label-1"
					TEST_VALUE_1 = "value-1"
					TEST_LABEL_2 = "label-2"
					TEST_VALUE_2 = "value-2"
					REGION       = "somewhere"
				)
				approval.Labels = &map[string]string{TEST_LABEL_1: TEST_VALUE_1, TEST_LABEL_2: TEST_VALUE_2}
				approval.Region = util.StrToPtr(REGION)

				dev := enrollAndWaitForDevice(h, approval)

				Expect(*dev.Metadata.Labels).To(HaveKeyWithValue(TEST_LABEL_1, TEST_VALUE_1))
				Expect(*dev.Metadata.Labels).To(HaveKeyWithValue(TEST_LABEL_2, TEST_VALUE_2))
				Expect(*dev.Metadata.Labels).To(HaveKeyWithValue("region", REGION))
			})

		})

		When("updating the agent device spec", func() {
			It("should write any files to the device", func() {
				dev := enrollAndWaitForDevice(h, DefaultApproval())
				dev.Spec = getTestSpec("device.yaml")
				_, err := h.Client.ReplaceDeviceWithResponse(h.Context, *dev.Metadata.Name, *dev)
				Expect(err).ToNot(HaveOccurred())

				GinkgoWriter.Printf(
					"Waiting for /etc/motd file to be created on the device %s, with testDirPath: %s\n",
					*dev.Metadata.Name, h.TestDirPath)

				var fileInfo fs.FileInfo
				Eventually(func() bool {
					fileInfo, err = os.Stat(filepath.Join(h.TestDirPath, "/etc/motd"))
					if err != nil && os.IsNotExist(err) {
						return false
					}
					return true
				}, TIMEOUT, POLLING).Should(BeTrue())

				Expect(fileInfo.Mode()).To(Equal(os.FileMode(0600)))
			})
		})

	})
})

func enrollAndWaitForDevice(h *harness.TestHarness, approval *v1alpha1.EnrollmentRequestApproval) *v1alpha1.Device {
	deviceName := ""
	Eventually(getEnrolledDeviceName, TIMEOUT, POLLING).WithArguments(h, &deviceName).Should(BeTrue())
	approveEnrollment(h, deviceName, approval)

	// verify that the device is created
	dev, err := h.Client.ReadDeviceWithResponse(h.Context, deviceName)
	Expect(err).ToNot(HaveOccurred())
	return dev.JSON200
}

func DefaultApproval() *v1alpha1.EnrollmentRequestApproval {
	return &v1alpha1.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &map[string]string{"label": "value"},
		Region:   util.StrToPtr("region"),
	}
}

func approveEnrollment(h *harness.TestHarness, deviceName string, approval *v1alpha1.EnrollmentRequestApproval) {
	Expect(approval).NotTo(BeNil())
	GinkgoWriter.Printf("Approving device enrollment: %s\n", deviceName)
	_, err := h.Client.CreateEnrollmentRequestApprovalWithResponse(h.Context, deviceName, *approval)
	Expect(err).ToNot(HaveOccurred())
}

func getEnrolledDeviceName(h *harness.TestHarness, deviceName *string) bool {
	listResp, err := h.Client.ListEnrollmentRequestsWithResponse(h.Context, &v1alpha1.ListEnrollmentRequestsParams{})
	Expect(err).ToNot(HaveOccurred())

	if len(listResp.JSON200.Items) == 0 {
		return false
	}

	Expect(*listResp.JSON200.Items[0].Metadata.Name).ToNot(BeEmpty())
	*deviceName = *listResp.JSON200.Items[0].Metadata.Name
	return true
}

func getTestSpec(deviceYaml string) v1alpha1.DeviceSpec {
	deviceBytes, err := os.ReadFile(filepath.Join("testdata", deviceYaml))
	Expect(err).ToNot(HaveOccurred())

	var device v1alpha1.Device
	err = yaml.Unmarshal(deviceBytes, &device)
	Expect(err).ToNot(HaveOccurred())

	return device.Spec
}
