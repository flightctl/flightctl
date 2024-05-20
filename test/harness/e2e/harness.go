package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	client "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

const POLLING = "250ms"
const TIMEOUT = "60s"

type Harness struct {
	VM        vm.TestVMInterface
	Client    *apiclient.ClientWithResponses
	Context   context.Context
	ctxCancel context.CancelFunc
}

func findTopLevelDir() string {
	currentWorkDirectory, err := os.Getwd()
	Expect(err).ToNot(HaveOccurred())

	parts := strings.Split(currentWorkDirectory, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "test" {
			path := strings.Join(parts[:i], "/")
			logrus.Debugf("Top-level directory: %s", path)
			return path
		}
	}
	Fail("Could not find top-level directory")
	// this return is not reachable but we need to satisfy the compiler
	return ""
}

func NewTestHarness() *Harness {

	testVM, err := vm.NewVM(vm.TestVM{
		TestDir:       GinkgoT().TempDir(),
		VMName:        "flightctl-e2e-vm-" + uuid.New().String(),
		DiskImagePath: filepath.Join(findTopLevelDir(), "bin/output/qcow2/disk.qcow2"),
		VMUser:        "redhat",
		SSHPassword:   "redhat",
		SSHPort:       2233, // TODO: randomize and retry on error
	})
	Expect(err).ToNot(HaveOccurred())

	c, err := client.NewFromConfigFile(client.DefaultFlightctlClientConfigPath())
	Expect(err).ToNot(HaveOccurred())

	ctx, cancel := context.WithCancel(context.Background())

	return &Harness{
		VM:        testVM,
		Client:    c,
		Context:   ctx,
		ctxCancel: cancel,
	}
}

func (h *Harness) Cleanup() {
	err := h.VM.ForceDelete()
	Expect(err).ToNot(HaveOccurred())
	// This will stop any blocking function that is waiting for the context to be canceled
	h.ctxCancel()
}

func (h *Harness) UpdateOsImageTo(id, image string) {
	device := h.GetDevice(id)

	if device.Spec.Os == nil {
		device.Spec.Os = &v1alpha1.DeviceOSSpec{
			Image: image,
		}
	} else {
		device.Spec.Os.Image = image
	}
	logrus.Infof("Updated device to %#v", device)

	resp, err := h.Client.ReplaceDeviceWithResponse(h.Context, id, *device)
	Expect(err).ToNot(HaveOccurred())
	Expect(resp.JSON200).NotTo(BeNil())
}

func (h *Harness) GetDevice(id string) *v1alpha1.Device {
	resp, err := h.Client.ReadDeviceWithResponse(h.Context, id)
	Expect(err).ToNot(HaveOccurred())
	Expect(resp.JSON200).ToNot(BeNil())

	return resp.JSON200
}

func (h *Harness) UpdateDevice(id string) *v1alpha1.Device {
	resp, err := h.Client.ReadDeviceWithResponse(h.Context, id)
	Expect(err).ToNot(HaveOccurred())
	Expect(resp.JSON200).ToNot(BeNil())

	return resp.JSON200
}

func (h *Harness) GetEnrollmentIDFromConsole() string {
	// wait for the enrollment ID on the console
	Eventually(h.VM.GetConsoleOutput, TIMEOUT, POLLING).Should(ContainSubstring("/enroll/"))
	output := h.VM.GetConsoleOutput()

	enrollmentID := output[strings.Index(output, "/enroll/")+8:]
	enrollmentID = enrollmentID[:strings.Index(enrollmentID, "\r")]
	enrollmentID = strings.TrimRight(enrollmentID, "\n")
	return enrollmentID
}

func (h *Harness) WaitForEnrollmentRequest(id string) *v1alpha1.EnrollmentRequest {
	var enrollmentRequest *v1alpha1.EnrollmentRequest
	Eventually(func() *v1alpha1.EnrollmentRequest {
		resp, _ := h.Client.ReadEnrollmentRequestWithResponse(h.Context, id)
		if resp != nil && resp.JSON200 != nil {
			enrollmentRequest = resp.JSON200
		}
		return enrollmentRequest
	}, TIMEOUT, POLLING).ShouldNot(BeNil())
	return enrollmentRequest
}

func (h *Harness) ApproveEnrollment(id string, approval *v1alpha1.EnrollmentRequestApproval) {
	Expect(approval).NotTo(BeNil())

	logrus.Infof("Approving device enrollment: %s", id)
	apr, err := h.Client.CreateEnrollmentRequestApprovalWithResponse(h.Context, id, *approval)
	Expect(err).ToNot(HaveOccurred())
	Expect(apr.JSON200).NotTo(BeNil())
	logrus.Infof("Approved device enrollment: %s", id)
}
