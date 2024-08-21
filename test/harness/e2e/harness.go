package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	client "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	"github.com/flightctl/flightctl/test/util"
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
		VMUser:        "user",
		SSHPassword:   "user",
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

// Harness cleanup, this will delete the VM and cancel the context
// if something failed we try to gather logs, console logs are optional
// and can be enabled by setting printConsole to true
func (h *Harness) Cleanup(printConsole bool) {
	testFailed := CurrentSpecReport().Failed()

	if testFailed {
		fmt.Println("==========================================================")
		fmt.Printf("oops... %s failed\n", CurrentSpecReport().FullText())
	}

	if running, _ := h.VM.IsRunning(); running && testFailed {
		fmt.Println("VM is running, attempting to get logs and details")
		stdout, _ := h.VM.RunSSH([]string{"sudo", "systemctl", "status", "flightctl-agent"}, nil)
		fmt.Print("\n\n\n")
		fmt.Println("============ systemctl status flightctl-agent ============")
		fmt.Println(stdout.String())
		fmt.Println("=============== logs for flightctl-agent =================")
		stdout, _ = h.VM.RunSSH([]string{"sudo", "journalctl", "--no-hostname", "-u", "flightctl-agent"}, nil)
		fmt.Println(stdout.String())
		if printConsole {
			fmt.Println("======================= VM Console =======================")
			fmt.Println(h.VM.GetConsoleOutput())
		}
		fmt.Println("==========================================================")
		fmt.Print("\n\n\n")
	}
	err := h.VM.ForceDelete()
	Expect(err).ToNot(HaveOccurred())
	// This will stop any blocking function that is waiting for the context to be canceled
	h.ctxCancel()
}

func (h *Harness) GetEnrollmentIDFromConsole() string {
	// wait for the enrollment ID on the console
	enrollmentId := ""
	Eventually(func() string {
		consoleOutput := h.VM.GetConsoleOutput()
		enrollmentId = util.GetEnrollmentIdFromText(consoleOutput)
		return enrollmentId
	}, TIMEOUT, POLLING).ShouldNot(BeEmpty(), "Enrollment ID not found in VM console output")

	return enrollmentId
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
	apr, err := h.Client.ApproveEnrollmentRequestWithResponse(h.Context, id, *approval)
	Expect(err).ToNot(HaveOccurred())
	Expect(apr.JSON200).NotTo(BeNil())
	logrus.Infof("Approved device enrollment: %s", id)
}

func (h *Harness) StartVMAndEnroll() string {
	err := h.VM.RunAndWaitForSSH()
	Expect(err).ToNot(HaveOccurred())

	enrollmentID := h.GetEnrollmentIDFromConsole()
	logrus.Infof("Enrollment ID found in VM console output: %s", enrollmentID)

	_ = h.WaitForEnrollmentRequest(enrollmentID)
	h.ApproveEnrollment(enrollmentID, util.TestEnrollmentApproval())
	logrus.Infof("Waiting for device %s to report status", enrollmentID)

	// wait for the device to pickup enrollment and report measurements on device status
	Eventually(h.GetDeviceWithStatusSystem, TIMEOUT, POLLING).WithArguments(
		enrollmentID).ShouldNot(BeNil())

	return enrollmentID
}

func (h *Harness) GetDeviceWithStatusSystem(enrollmentID string) *apiclient.ReadDeviceResponse {
	device, err := h.Client.ReadDeviceWithResponse(h.Context, enrollmentID)
	Expect(err).NotTo(HaveOccurred())
	// we keep waiting for a 200 response, with filled in Status.SystemInfo
	if device.JSON200 == nil || device.JSON200.Status == nil || device.JSON200.Status.SystemInfo.IsEmpty() {
		return nil
	}
	return device
}

func (h *Harness) ApiEndpoint() string {
	ep := os.Getenv("API_ENDPOINT")
	if ep == "" {
		ep = "https://" + util.GetExtIP() + ":3443"
		logrus.Infof("API_ENDPOINT not set, using default: %s", ep)
		err := os.Setenv("API_ENDPOINT", ep)
		Expect(err).ToNot(HaveOccurred())
	}
	return ep
}

func (h *Harness) setArgsInCmd(cmd *exec.Cmd, args ...string) {
	for _, arg := range args {
		replacedArg := strings.ReplaceAll(arg, "${API_ENDPOINT}", h.ApiEndpoint())
		cmd.Args = append(cmd.Args, replacedArg)
	}
}

func (h *Harness) RunInteractiveCLI(args ...string) (io.WriteCloser, io.ReadCloser, error) {
	// TODO pty: this is how oci does a PTY:
	// https://github.com/cri-o/cri-o/blob/main/internal/oci/oci_unix.go
	//
	// set PS1 environment variable to make bash print the default prompt

	cmd := exec.Command(flightctlPath()) //nolint:gosec
	h.setArgsInCmd(cmd, args...)

	logrus.Infof("running: %s", strings.Join(cmd.Args, " "))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("error getting stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("error getting stdout pipe: %w", err)
	}

	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("error starting interactive process: %w", err)
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			logrus.Errorf("error waiting for interactive process: %v", err)
		} else {
			logrus.Info("interactive process exited succesfully")
		}
	}()
	return stdin, stdout, nil
}

func (h *Harness) CLIWithStdin(stdin string, args ...string) (string, error) {
	cmd := exec.Command(flightctlPath()) //nolint:gosec

	cmd.Stdin = strings.NewReader(stdin)

	h.setArgsInCmd(cmd, args...)

	logrus.Infof("running: %s", strings.Join(cmd.Args, " "))
	output, err := cmd.CombinedOutput()

	if err != nil {
		logrus.Errorf("executing cli: %s", err)
		// keeping standard error output for debugging, otherwise log output
		// will make it very hard to read
		fmt.Fprintf(GinkgoWriter, "output: %s\n", string(output))
	}

	return string(output), err
}

func flightctlPath() string {
	return filepath.Join(util.GetTopLevelDir(), "/bin/flightctl")
}

func (h *Harness) CLI(args ...string) (string, error) {
	return h.CLIWithStdin("", args...)
}
