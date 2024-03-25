package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const TIMEOUT = "30s"
const POLLING = "250ms"

func TestAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent E2E Suite")
}

var _ = Describe("VM Agent behavior", func() {
	var (
		testVM vm.BootcVM
	)

	BeforeEach(func() {

		currentWorkDirectory, err := os.Getwd()

		Expect(err).ToNot(HaveOccurred())
		params := vm.NewVMParameters{
			TestDir:       GinkgoT().TempDir(),
			VMName:        "e2e-test-vm",
			DiskImagePath: filepath.Join(currentWorkDirectory, "../../bin/output/qcow2/disk.qcow2"),
		}

		testVM, err = vm.NewVM(params)
		Expect(err).ToNot(HaveOccurred())

		err = testVM.Run(vm.RunVMParameters{
			VMUser:      "redhat",
			SSHPassword: "redhat",
			SSHPort:     2233, // TODO: randomize and retry on error
		})

		Expect(err).ToNot(HaveOccurred())

		testVM.WaitForSSHToBeReady()
	})

	AfterEach(func() {
		err := testVM.ForceDelete()
		Expect(err).ToNot(HaveOccurred())
	})

	Context("vm", func() {
		It("should print QR output to console", func() {
			// Wait for the top-most part of the QR output to appear
			Eventually(testVM.GetConsoleOutput, TIMEOUT, POLLING).Should(ContainSubstring("████████████████████████████████"))
		})
		It("should have flightctl-agent running", func() {
			stdout, err := testVM.RunSSH([]string{"sudo", "systemctl", "status", "flightctl-agent"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("Active: active (running)"))
		})
	})

})
