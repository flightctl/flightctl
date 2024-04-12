package agent_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	"github.com/google/uuid"
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
		testVM vm.TestVMInterface
	)

	BeforeEach(func() {

		currentWorkDirectory, err := os.Getwd()

		Expect(err).ToNot(HaveOccurred())

		testVM, err = vm.StartAndWaitForSSH(vm.TestVM{
			TestDir:       GinkgoT().TempDir(),
			VMName:        "flightctl-e2e-vm-" + uuid.New().String(),
			DiskImagePath: filepath.Join(currentWorkDirectory, "../../bin/output/qcow2/disk.qcow2"),
			VMUser:        "redhat",
			SSHPassword:   "redhat",
			SSHPort:       2233, // TODO: randomize and retry on erro
		})
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		err := testVM.ForceDelete()
		Expect(err).ToNot(HaveOccurred())
	})

	Context("vm", func() {
		It("should print QR output to console", func() {
			// Wait for the top-most part of the QR output to appear
			Eventually(testVM.GetConsoleOutput, TIMEOUT, POLLING).Should(ContainSubstring("████████████████████████████████"))
			output := testVM.GetConsoleOutput()
			// this is only to show output when VERBOSE=true
			fmt.Println("============ Console output ============")
			lines := strings.Split(output, "\n")
			fmt.Println(strings.Join(lines[len(lines)-20:], "\n"))
			fmt.Println("========================================")
		})
		It("should have flightctl-agent running", func() {
			stdout, err := testVM.RunSSH([]string{"sudo", "systemctl", "status", "flightctl-agent"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("Active: active (running)"))
		})
	})

})
