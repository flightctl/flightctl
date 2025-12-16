package e2e

/*
VM Pool Pattern - The Only Supported Way to Manage VMs

The harness enforces a strict VM pool pattern to ensure proper test isolation and performance:

1. BEFORE SUITE (Once per worker):
   - Call e2e.RegisterVMPoolCleanup() to register cleanup
   - Call e2e.SetupVMForWorker(workerID, tempDir, sshPort) to create VM for this worker
   - VM is created once and reused across all tests

2. BEFORE EACH (Before each test):
   - Use e2e.NewTestHarnessWithVMPool(ctx, workerID) to get harness with VM from pool
   - Call harness.SetupVMFromPoolAndStartAgent(workerID) to revert to pristine snapshot and start agent

3. DURING TESTS:
   - Use the VM from the pool, never create new VMs
   - All VM state changes are isolated through snapshots
   - Each test starts with a pristine VM state

4. AFTER EACH:
   - Clean up test resources with harness.CleanUpAllResources()
   - Call harness.Cleanup(true) to clean up harness

5. AFTER SUITE:
   - VM cleanup is handled by make scripts after all tests complete

REMOVED METHODS (violated VM pool pattern):
- NewTestHarness() - Created VMs directly (removed)
- NewTestHarnessWithoutVM() - Manual VM setup (removed)
- AddVM() / AddMultipleVMs() - Created VMs outside pool (removed)
- StartMultipleVMAndEnroll() - Created multiple VMs directly (removed)

Example usage:
```go
var _ = BeforeSuite(func() {
    e2e.RegisterVMPoolCleanup()
    workerID = GinkgoParallelProcess()
    _, err = e2e.SetupVMForWorker(workerID, os.TempDir(), 2233)
    Expect(err).ToNot(HaveOccurred())
})

var _ = BeforeEach(func() {
    harness, err = e2e.NewTestHarnessWithVMPool(ctx, workerID)
    Expect(err).ToNot(HaveOccurred())
    err = harness.SetupVMFromPoolAndStartAgent(workerID)
    Expect(err).ToNot(HaveOccurred())
    // Handle device enrollment in test as needed
})
```
*/

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/flightctl/flightctl/api/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	service "github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	"github.com/flightctl/flightctl/test/util"
	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

const POLLING = "250ms"
const POLLINGLONG = "1s"
const TIMEOUT = "5m"
const LONGTIMEOUT = "10m"

// Operation constants for RBAC testing
const (
	OperationCreate = "create"
	OperationUpdate = "update"
	OperationGet    = "get"
	OperationList   = "list"
	OperationDelete = "delete"
	OperationAll    = "all"
)

type Harness struct {
	Client    *apiclient.ClientWithResponses
	Context   context.Context
	Cluster   kubernetes.Interface
	ctxCancel context.CancelFunc
	startTime time.Time

	VM vm.TestVMInterface

	// Git repository management
	gitRepos   map[string]string // map of repo name to repo path
	gitWorkDir string            // working directory for git operations
}

// GitServerConfig holds configuration for the git server
type GitServerConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	SSHKey   string // path to SSH private key if using key auth
}

// findTopLevelDir is unused but kept for potential future use
func findTopLevelDir() string { //nolint:unused
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
	// this return is not reachable, but we need to satisfy the compiler
	return ""
}

// try to resolve the kube config at a few well known locations
func resolveKubeConfigPath() (string, error) {
	if kc, ok := os.LookupEnv("KUBECONFIG"); ok && kc != "" {
		return kc, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	paths := []string{
		filepath.Join(home, ".kube", "config"),                                                           // default
		filepath.Join(string(filepath.Separator), "home", "kni", "clusterconfigs", "kubeconfig"),         // qa path
		filepath.Join(string(filepath.Separator), "home", "kni", "auth", "clusterconfigs", "kubeconfig"), // qa path
	}
	for _, path := range paths {
		if _, err = os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("failed to find kubeconfig file in the paths: %v", paths)
}

// build a k8s interface so that tests can interact with it directly from Go rather than
// shelling out to `oc` or `kubectl`
func kubernetesClient() (kubernetes.Interface, error) {
	kubeconfig, err := resolveKubeConfigPath()
	if err != nil {
		return nil, fmt.Errorf("unable to resolve kubeconfig location: %w", err)
	}

	logrus.Debugf("Using kubeconfig: %s", kubeconfig)
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("error building kubeconfig: %w", err)
	}

	iface, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("error creating kubernetes api: %w", err)
	}
	return iface, nil
}

// ReadPrimaryVMAgentLogs reads flightctl-agent journalctl logs from the primary VM
func (h *Harness) ReadPrimaryVMAgentLogs(since string, unit string) (string, error) {
	if h.VM == nil {
		return "", fmt.Errorf("VM is not initialized")
	}
	logs, err := h.VM.JournalLogs(vm.JournalOpts{
		Since: since,
		Unit:  unit,
	})

	return logs, err
}

// ReadClientConfig returns the client config for at the specified location. The default config path is used if no path is
// specified
func (h *Harness) ReadClientConfig(filePath string) (*client.Config, error) {
	if filePath == "" {
		defaultPath, err := client.DefaultFlightctlClientConfigPath()
		if err != nil {
			return nil, fmt.Errorf("failed to get default client config path: %w", err)
		}
		filePath = defaultPath
	}
	return client.ParseConfigFile(filePath)
}

// ExtractAuthURL extracts the authentication URL from an AuthProvider based on its type
func ExtractAuthURL(provider *v1beta1.AuthProvider) string {
	if provider == nil {
		return ""
	}
	providerType, _ := provider.Spec.Discriminator()
	switch providerType {
	case string(v1beta1.K8s):
		if k8sSpec, err := provider.Spec.AsK8sProviderSpec(); err == nil {
			return k8sSpec.ApiUrl
		}
	case string(v1beta1.Oidc):
		if oidcSpec, err := provider.Spec.AsOIDCProviderSpec(); err == nil {
			return oidcSpec.Issuer
		}
	case string(v1beta1.Aap):
		if aapSpec, err := provider.Spec.AsAapProviderSpec(); err == nil {
			return aapSpec.ApiUrl
		}
	case string(v1beta1.Oauth2):
		if oauth2Spec, err := provider.Spec.AsOAuth2ProviderSpec(); err == nil {
			return oauth2Spec.AuthorizationUrl
		}
	}
	return ""
}

// MarkClientAccessTokenExpired updates the client configuration at the specified path by marking the token as expired
// If no path is supplied, the default config path will be used
func (h *Harness) MarkClientAccessTokenExpired(filePath string) error {
	if filePath == "" {
		defaultPath, err := client.DefaultFlightctlClientConfigPath()
		if err != nil {
			return fmt.Errorf("failed to get default client config path: %w", err)
		}
		filePath = defaultPath
	}
	cfg, err := client.ParseConfigFile(filePath)
	if err != nil {
		return err
	}
	// expire the token by making setting the time to one minute ago
	cfg.AuthInfo.AccessTokenExpiry = time.Now().Add(-1 * time.Minute).Format(time.RFC3339Nano)
	return cfg.Persist(filePath)
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

	// Clean up the single VM (if it exists)
	if h.VM != nil {
		if running, _ := h.VM.IsRunning(); running && testFailed {
			fmt.Println("VM is running, attempting to get logs and details")
			stdout, _ := h.VM.RunSSH([]string{"sudo", "systemctl", "status", "flightctl-agent"}, nil)
			fmt.Print("\n\n\n")
			fmt.Println("============ systemctl status flightctl-agent ============")
			fmt.Println(stdout.String())
			fmt.Println("=============== logs for flightctl-agent =================")
			fmt.Println(h.ReadPrimaryVMAgentLogs("", util.FLIGHTCTL_AGENT_SERVICE))
			if printConsole {
				fmt.Println("======================= VM Console =======================")
				fmt.Println(h.VM.GetConsoleOutput())
			}
			fmt.Println("==========================================================")
			fmt.Print("\n\n\n")
		}
		err := h.VM.ForceDelete()
		Expect(err).ToNot(HaveOccurred())
	}

	// Clean up git repositories
	if err := h.CleanupGitRepositories(); err != nil {
		logrus.Errorf("Failed to clean up git repositories: %v", err)
	}

	diffTime := time.Since(h.startTime)
	fmt.Printf("Test took %s\n", diffTime)

	// Cancel the context to stop any blocking operations
	h.ctxCancel()
}

// GetServiceLogs returns the logs from the specified service using journalctl.
// This is useful for debugging service output and capturing logs from the latest service invocation.
func (h *Harness) GetServiceLogs(serviceName string) (string, error) {
	return h.VM.GetServiceLogs(serviceName)
}

// GetServiceLogs returns the logs from the specified service using journalctl.
// This is useful for debugging service output and capturing logs from the latest service invocation.
func (h *Harness) GetFlightctlAgentLogs() (string, error) {
	return h.VM.GetServiceLogs("flightctl-agent")
}

// GetEnrollmentIDFromServiceLogs returns the enrollment ID from the service logs using journalctl.
// This is more reliable than console output as it captures service output regardless of how the service is started.
func (h *Harness) GetEnrollmentIDFromServiceLogs(serviceName string) string {
	// Wait for the enrollment ID in the service logs
	enrollmentId := ""
	Eventually(func() string {
		// Get logs from the latest service invocation using systemd invocation ID
		output, err := h.GetServiceLogs(serviceName)

		if err != nil {
			logrus.Debugf("Failed to get service logs: %v", err)
			return ""
		}
		enrollmentId = util.GetEnrollmentIdFromText(output)
		return enrollmentId
	}, TIMEOUT, POLLING).ShouldNot(BeEmpty(), fmt.Sprintf("Enrollment ID not found in %s service logs", serviceName))

	return enrollmentId
}

func (h *Harness) WaitForEnrollmentRequest(id string) *v1beta1.EnrollmentRequest {
	var enrollmentRequest *v1beta1.EnrollmentRequest
	Eventually(func() *v1beta1.EnrollmentRequest {
		resp, _ := h.Client.GetEnrollmentRequestWithResponse(h.Context, id)
		if resp != nil && resp.JSON200 != nil {
			enrollmentRequest = resp.JSON200
		}
		return enrollmentRequest
	}, TIMEOUT, POLLING).ShouldNot(BeNil())
	return enrollmentRequest
}

func (h *Harness) ApproveEnrollment(id string, approval *v1beta1.EnrollmentRequestApproval) {
	Expect(approval).NotTo(BeNil())

	logrus.Infof("Approving device enrollment: %s", id)
	h.addTestLabelToEnrollmentApprovalRequest(approval)
	apr, err := h.Client.ApproveEnrollmentRequestWithResponse(h.Context, id, *approval)
	Expect(err).ToNot(HaveOccurred())

	// Debug the response
	logrus.Infof("Response status code: %d", apr.StatusCode())
	logrus.Infof("Response body: %s", string(apr.Body))
	logrus.Infof("JSON200 is nil: %v", apr.JSON200 == nil)

	// Handle different response status codes
	switch apr.StatusCode() {
	case 200:
		Expect(apr.JSON200).NotTo(BeNil())
		logrus.Infof("Approved device enrollment: %s", id)
	case 400:
		// Check if it's already approved
		if apr.JSON400 != nil && strings.Contains(apr.JSON400.Message, "already approved") {
			logrus.Infof("Enrollment request %s is already approved", id)
			return
		}
		// If it's a different 400 error, fail the test
		if apr.JSON400 != nil {
			Fail(fmt.Sprintf("Failed to approve enrollment request: %s", apr.JSON400.Message))
		} else {
			Fail("Failed to approve enrollment request: 400 Bad Request")
		}
	default:
		Fail(fmt.Sprintf("Unexpected status code: %d", apr.StatusCode()))
	}
}

func (h *Harness) StartVMAndEnroll() string {
	err := h.VM.RunAndWaitForSSH()
	Expect(err).ToNot(HaveOccurred())

	enrollmentID := h.GetEnrollmentIDFromServiceLogs("flightctl-agent")
	logrus.Infof("Enrollment ID found in flightctl-agent service logs: %s", enrollmentID)

	_ = h.WaitForEnrollmentRequest(enrollmentID)
	h.ApproveEnrollment(enrollmentID, h.TestEnrollmentApproval())
	logrus.Infof("Waiting for device %s to report status", enrollmentID)

	// wait for the device to pickup enrollment and report measurements on device status
	Eventually(h.GetDeviceWithStatusSystem, TIMEOUT, POLLING).WithArguments(
		enrollmentID).ShouldNot(BeNil())

	return enrollmentID
}

func (h *Harness) ApiEndpoint() string {
	ep := os.Getenv("API_ENDPOINT")
	Expect(ep).NotTo(BeEmpty(), "API_ENDPOINT environment variable must be set")
	return ep
}

func (h *Harness) RegistryEndpoint() string {
	ep := os.Getenv("REGISTRY_ENDPOINT")
	Expect(ep).NotTo(BeEmpty(), "REGISTRY_ENDPOINT environment variable must be set")
	return ep
}

func (h *Harness) setArgsInCmd(cmd *exec.Cmd, args ...string) {
	for _, arg := range args {
		replacedArg := strings.ReplaceAll(arg, "${API_ENDPOINT}", h.ApiEndpoint())
		cmd.Args = append(cmd.Args, replacedArg)
	}
}

func (h *Harness) ReplaceVariableInString(s string, old string, new string) string {
	if s == "" || old == "" {
		replacedString := strings.ReplaceAll(s, old, new)
		return replacedString
	}
	return ""
}

func (h *Harness) RunInteractiveCLI(args ...string) (io.WriteCloser, io.ReadCloser, error) {
	cmd := exec.Command(flightctlPath()) //nolint:gosec // flightctlPath constructs path from project directory structure for test purposes
	h.setArgsInCmd(cmd, args...)

	// create a pty/tty pair
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, nil, err
	}

	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("error starting interactive process: %w", err)
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			logrus.Errorf("error waiting for interactive process: %v", err)
		} else {
			logrus.Info("interactive process exited successfully")
		}
		if err := tty.Close(); err != nil {
			logrus.Errorf("error closing tty: %v", err)
		}
	}()

	return ptmx, ptmx, nil
}

func (h *Harness) CLIWithStdin(stdin string, args ...string) (string, error) {
	return h.SHWithStdin(stdin, flightctlPath(), args...)
}

func (h *Harness) SHWithStdin(stdin, command string, args ...string) (string, error) {
	cmd := exec.Command(command)

	cmd.Stdin = strings.NewReader(stdin)

	h.setArgsInCmd(cmd, args...)

	logrus.Infof("running: %s with stdin: %s", strings.Join(cmd.Args, " "), stdin)
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

// GetFlightctlPath returns the path to the flightctl binary
func (h *Harness) GetFlightctlPath() string {
	return flightctlPath()
}

func (h *Harness) CLI(args ...string) (string, error) {
	return h.CLIWithStdin("", args...)
}

// CLIWithEnvAndShell runs a shell command with custom environment variables (for complex commands with pipes)
func (h *Harness) CLIWithEnvAndShell(env map[string]string, shellCommand string) (string, error) {
	cmd := exec.Command("bash", "-c", shellCommand)

	// Set custom environment variables
	if env != nil {
		cmd.Env = os.Environ()
		for key, value := range env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, value))
		}
	}

	logrus.Infof("running shell command with env: %v", env)
	output, err := cmd.CombinedOutput()

	if err != nil {
		logrus.Errorf("executing shell command: %s", err)
		// keeping standard error output for debugging, otherwise log output
		// will make it very hard to read
		fmt.Fprintf(GinkgoWriter, "output: %s\n", string(output))
	}

	return string(output), err
}

func (h *Harness) SH(command string, args ...string) (string, error) {
	return h.SHWithStdin("", command, args...)
}

func updateResourceWithRetries(updateFunc func() error) {
	Eventually(func() error {
		return updateFunc()
	}, TIMEOUT, "1s").Should(BeNil())
}

func checkForResourceChange[T any](resource T, lastResource string) string {
	yamlData, err := yaml.Marshal(resource)
	Expect(err).ToNot(HaveOccurred())

	current := string(yamlData)

	if lastResource != "" && current != lastResource {
		var prev any
		var curr any

		err = yaml.Unmarshal([]byte(lastResource), &prev)
		Expect(err).ToNot(HaveOccurred())
		err = yaml.Unmarshal(yamlData, &curr)
		Expect(err).ToNot(HaveOccurred())

		if d := cmp.Diff(prev, curr); d != "" {
			GinkgoWriter.Println("")
			GinkgoWriter.Println("==================== Resource change  ==================")
			GinkgoWriter.Println(d)
			GinkgoWriter.Println("==================================================================")
		}
	}

	return current
}

func ensureResourceContents[T any](id string, description string, fetch func(string) (T, error), condition func(T) bool, timeout string) {
	lastResourcePrint := ""
	Consistently(func() error {
		logrus.Infof("Ensuring condition: %q stays consistent", description)
		resource, err := fetch(id)
		Expect(err).NotTo(HaveOccurred())
		lastResourcePrint = checkForResourceChange(resource, lastResourcePrint)
		if condition(resource) {
			return nil
		}
		return fmt.Errorf("resource: %s not updated", id)
	}, timeout, "2s").Should(BeNil())
}

func waitForResourceContents[T any](id string, description string, fetch func(string) (T, error), condition func(T) bool, timeout string) {
	lastResourcePrint := ""
	pollingRate := "500ms"

	logrus.Infof("Waiting for condition: %q to be met - polling every %s, timeout=%s", description, pollingRate, timeout)
	Eventually(func() error {
		logrus.Debugf("Waiting for condition: %q to be met", description)
		resource, err := fetch(id)
		Expect(err).NotTo(HaveOccurred())

		lastResourcePrint = checkForResourceChange(resource, lastResourcePrint)

		if condition(resource) {
			return nil
		}
		return fmt.Errorf("resource: %s not updated", id)
	}, timeout, pollingRate).Should(BeNil())
}

func (h *Harness) EnrollAndWaitForOnlineStatus(labels ...map[string]string) (string, *v1beta1.Device) {
	deviceId := h.GetEnrollmentIDFromServiceLogs("flightctl-agent")
	logrus.Infof("Enrollment ID found in flightctl-agent service logs: %s", deviceId)
	Expect(deviceId).NotTo(BeNil())

	// Wait for the approve enrollment request response to not be nil
	h.WaitForEnrollmentRequest(deviceId)

	// Approve the enrollment and wait for the device details to be populated by the agent.
	h.ApproveEnrollment(deviceId, h.TestEnrollmentApproval(labels...))

	Eventually(h.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(
		deviceId).ShouldNot(BeEmpty())
	logrus.Infof("The device %s was approved", deviceId)

	// Wait for the device to pickup enrollment and report measurements on device status.
	Eventually(h.GetDeviceWithStatusSystem, TIMEOUT, POLLING).WithArguments(
		deviceId).ShouldNot(BeNil())
	logrus.Infof("The device %s is reporting its status", deviceId)

	// Check the device status.
	response, err := h.GetDeviceWithStatusSystem(deviceId)
	Expect(err).NotTo(HaveOccurred())
	device := response.JSON200
	Expect(device.Status.Summary.Status).To(Equal(v1beta1.DeviceSummaryStatusOnline))
	Expect(*device.Status.Summary.Info).To(Equal(service.DeviceStatusInfoHealthy))
	return deviceId, device
}
func (h *Harness) TestEnrollmentApproval(labels ...map[string]string) *v1beta1.EnrollmentRequestApproval {
	mergedLabels := map[string]string{"test-id": h.GetTestIDFromContext()}
	for _, label := range labels {
		for k, v := range label {
			mergedLabels[k] = v
		}
	}
	return &v1beta1.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &mergedLabels,
	}
}

func (h *Harness) CleanUpAllTestResources() error {
	return h.CleanUpTestResources(util.ResourceTypes[:]...)
}

func (h *Harness) CleanUpResource(resourceType string, resourceName string) (string, error) {
	logrus.Infof("Deleting resource %s of resource type %s", resourceName, resourceType)
	resource := resourceType + "/" + resourceName
	return h.CLI("delete", resource)
}

// CleanUpTestResources deletes only resources that have the test label for the current test
func (h *Harness) CleanUpTestResources(resourceTypes ...string) error {
	testID := h.GetTestIDFromContext()
	logrus.Infof("Cleaning up resources with test-id: %s", testID)

	// First, handle enrollment requests specially since they don't support labels
	if err := h.cleanUpEnrollmentRequests(testID); err != nil {
		return fmt.Errorf("failed to clean up enrollment requests: %w", err)
	}

	// Then clean up other resource types that support labels
	for _, resourceType := range resourceTypes {
		// Skip enrollment requests as they're handled separately
		if resourceType == util.EnrollmentRequest {
			continue
		}

		// Get resources with the test label
		resources, err := h.CLI("get", resourceType, "-l", fmt.Sprintf("test-id=%s", testID), "-o", "name")
		if err != nil {
			// If no resources found, that's fine
			logrus.Debugf("No %s resources found with test-id %s", resourceType, testID)
			continue
		}

		resources = strings.TrimSpace(resources)
		if resources == "" {
			logrus.Debugf("No %s resources found with test-id %s", resourceType, testID)
			continue
		}

		// Parse resource names from the output
		// Output format: "resourcetype/name1\nresourcetype/name2\n..."
		resourceNames := []string{}
		lines := strings.Split(resources, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			resourceNames = append(resourceNames, line)
		}

		if len(resourceNames) == 0 {
			logrus.Debugf("No %s resource names found with test-id %s", resourceType, testID)
			continue
		}

		// Delete the resources by name
		deleteArgs := append([]string{"delete", resourceType}, resourceNames...)
		_, err = h.CLI(deleteArgs...)
		if err != nil {
			logrus.Infof("Error deleting %s resources with test-id %s: %v", resourceType, testID, err)
			return err
		}

		logrus.Infof("Successfully deleted %d %s resources with test-id %s: %v", len(resourceNames), resourceType, testID, resourceNames)
	}

	logrus.Infof("Successfully cleaned up all test resources with test-id %s", testID)
	return nil
}

// cleanUpEnrollmentRequests handles the special case for enrollment requests
// Since enrollment requests don't support labels, we need to:
// 1. Get devices with the test label
// 2. Delete enrollment requests with the same names as those devices
func (h *Harness) cleanUpEnrollmentRequests(testID string) error {
	logrus.Debugf("Cleaning up enrollment requests for test-id: %s", testID)

	// Get devices with the test label
	devices, err := h.CLI("get", util.Device, "-l", fmt.Sprintf("test-id=%s", testID), "-o", "name")
	if err != nil {
		// If no devices found, that's fine - no enrollment requests to clean up
		logrus.Debugf("No devices found with test-id %s, skipping enrollment request cleanup", testID)
		return nil
	}

	devices = strings.TrimSpace(devices)
	if devices == "" {
		logrus.Debugf("No devices found with test-id %s, skipping enrollment request cleanup", testID)
		return nil
	}

	// Parse device names from the output
	deviceNames := []string{}
	lines := strings.Split(devices, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Extract just the name part from "device/name"
		if strings.Contains(line, "/") {
			parts := strings.Split(line, "/")
			if len(parts) == 2 {
				deviceNames = append(deviceNames, parts[1])
			}
		}
	}

	if len(deviceNames) == 0 {
		logrus.Debugf("No device names found with test-id %s, skipping enrollment request cleanup", testID)
		return nil
	}

	// Delete enrollment requests with the same names as the devices
	for _, deviceName := range deviceNames {
		_, err := h.CLI("delete", util.EnrollmentRequest, deviceName)
		if err != nil {
			// Log the error but don't fail the cleanup - enrollment requests might already be deleted
			logrus.Debugf("Error deleting enrollment request %s: %v (this might be expected if already deleted)", deviceName, err)
		} else {
			logrus.Debugf("Successfully deleted enrollment request: %s", deviceName)
		}
	}

	logrus.Infof("Completed enrollment request cleanup for test-id %s", testID)
	return nil
}

// Generic function to read and unmarshal YAML into the given target type
func getYamlResourceByFile[T any](yamlFile string) T {
	if yamlFile == "" {
		gomega.Expect(fmt.Errorf("yaml file path cannot be empty")).ToNot(gomega.HaveOccurred())
	}

	fileBytes, err := os.ReadFile(yamlFile)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "failed to read yaml file %s: %v", yamlFile, err)

	var resource T
	err = yaml.Unmarshal(fileBytes, &resource)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "failed to unmarshal yaml file %s: %v", yamlFile, err)

	return resource
}

// Wrapper function for Shell command to get resources by name
func (h *Harness) GetResourcesByName(resourceType string, resourceName ...string) (string, error) {
	var args []string

	if len(resourceName) > 0 && resourceName[0] != "" {
		args = []string{"get", fmt.Sprintf("%s/%s", resourceType, resourceName[0]), "-o", "name"}
	} else {
		args = []string{"get", resourceType, "-o", "name"}
	}

	return h.CLI(args...)
}

// Wrapper function for Device
func (h *Harness) GetDeviceByYaml(deviceYaml string) v1beta1.Device {
	return getYamlResourceByFile[v1beta1.Device](deviceYaml)
}

// Wrapper function for Fleet
func (h *Harness) GetFleetByYaml(fleetYaml string) v1beta1.Fleet {
	return getYamlResourceByFile[v1beta1.Fleet](fleetYaml)
}

// Wrapper function for Repository
func (h *Harness) GetRepositoryByYaml(repoYaml string) v1beta1.Repository {
	return getYamlResourceByFile[v1beta1.Repository](repoYaml)
}

// Wrapper function for ResourceSync
func (h *Harness) GetResourceSyncByYaml(rSyncYaml string) v1beta1.ResourceSync {
	return getYamlResourceByFile[v1beta1.ResourceSync](rSyncYaml)
}

// Wrapper function for EnrollmentRequest
func (h *Harness) GetEnrollmentRequestByYaml(erYaml string) *v1beta1.EnrollmentRequest {
	return getYamlResourceByFile[*v1beta1.EnrollmentRequest](erYaml)
}

// Wrapper function for CertificateSigningRequest
func (h *Harness) GetCertificateSigningRequestByYaml(csrYaml string) v1beta1.CertificateSigningRequest {
	return getYamlResourceByFile[v1beta1.CertificateSigningRequest](csrYaml)
}

// Create a repository resource
func (h *Harness) CreateRepository(repositorySpec v1beta1.RepositorySpec, metadata v1beta1.ObjectMeta) error {
	// Add test label to metadata
	h.addTestLabelToResource(&metadata)

	var repository = v1beta1.Repository{
		ApiVersion: v1beta1.RepositoryAPIVersion,
		Kind:       v1beta1.RepositoryKind,

		Metadata: metadata,
		Spec:     repositorySpec,
	}
	_, err := h.Client.CreateRepositoryWithResponse(h.Context, repository)
	return err
}

// ReplaceRepository ensures the specified repository exists and is updated to the appropriate values
func (h *Harness) ReplaceRepository(repositorySpec v1beta1.RepositorySpec, metadata v1beta1.ObjectMeta) error {
	var repository = v1beta1.Repository{
		ApiVersion: v1beta1.RepositoryAPIVersion,
		Kind:       v1beta1.RepositoryKind,

		Metadata: metadata,
		Spec:     repositorySpec,
	}
	_, err := h.Client.ReplaceRepositoryWithResponse(h.Context, *metadata.Name, repository)
	return err
}

// DeleteRepository deletes the specified repository
func (h *Harness) DeleteRepository(name string) error {
	_, err := h.Client.DeleteRepository(h.Context, name)
	return err
}

func tcpNetworkTableRule(ip, port string, remove bool) []string {
	flag := "-A" // Add rule
	if remove {
		flag = "-D" // Delete rule
	}

	rule := []string{"iptables", flag, "OUTPUT"}

	if ip != "" {
		rule = append(rule, "-d", ip)
	}

	if port != "" {
		rule = append(rule, "-p", "tcp", "--dport", port)
	}

	rule = append(rule, "-j", "DROP")

	return rule
}

func buildIPTablesCmd(ip, port string, remove bool) []string {
	return append([]string{"sudo"}, tcpNetworkTableRule(ip, port, remove)...)
}

func (h *Harness) SimulateNetworkFailure() error {
	context, err := GetContext()
	if err != nil {
		return fmt.Errorf("failed to get the context: %w", err)
	}

	var blockCommands [][]string

	switch context {
	case util.KIND:
		registryIP, registryPort, err := h.getRegistryEndpointInfo()

		if err != nil {
			return fmt.Errorf("failed to get the registry endpoint info: %w", err)
		}

		blockCommands = [][]string{
			buildIPTablesCmd(registryIP, registryPort, false),
		}

	case util.OCP:
		args := fmt.Sprintf(`
		 echo '1.2.3.4 %s' | sudo tee -a /etc/hosts
	`, h.RegistryEndpoint())
		blockCommands = [][]string{{"sudo", "bash", "-c", args}}

	default:
		return fmt.Errorf("unknown context: %s", context)
	}

	for _, cmd := range blockCommands {
		stdout, err := h.VM.RunSSH(cmd, nil)
		if err != nil {
			return fmt.Errorf("failed to simulate network failure %v: %v, stdout: %s", cmd, err, stdout)
		}
	}

	stdout, err := h.VM.RunSSH([]string{"sudo", "iptables", "-L", "OUTPUT"}, nil)
	if err != nil {
		logrus.Warnf("Failed to list iptables rules: %v", err)
	} else {
		logrus.Debugf("Current iptables rules:\n%s", stdout.String())
	}

	return nil
}

// SimulateNetworkFailureForCLI adds an entry to iptables to drop tcp traffic to the specified port:ip
// It returns a function that will only execute once to undo the iptables modification
func (h *Harness) SimulateNetworkFailureForCLI(ip, port string) (func() error, error) {
	args := tcpNetworkTableRule(ip, port, false)
	_, err := h.SH("sudo", args...)
	noop := func() error { return nil }
	if err != nil {
		return noop, fmt.Errorf("failed to add iptables rule %v: %w", args, err)
	}

	var once sync.Once
	var respErr error = nil
	return func() error {
		once.Do(func() { respErr = h.FixNetworkFailureForCLI(ip, port) })
		return respErr
	}, nil
}

func (h *Harness) FixNetworkFailure() error {
	context, err := GetContext()
	if err != nil {
		return fmt.Errorf("failed to get the context: %w", err)
	}

	var unblockCommands [][]string

	switch context {
	case util.KIND:
		registryIP, registryPort, err := h.getRegistryEndpointInfo()
		if err != nil {
			return fmt.Errorf("failed to get the registry port: %w", err)
		}
		unblockCommands = [][]string{
			buildIPTablesCmd(registryIP, registryPort, true),
		}

	case util.OCP:
		unblockCommands = [][]string{
			{"bash", "-c", "head -n -1 /etc/hosts > /tmp/hosts_tmp && sudo mv /tmp/hosts_tmp /etc/hosts"},
		}

	default:
		return fmt.Errorf("unknown context: %s", context)
	}

	for _, cmd := range unblockCommands {
		stdout, err := h.VM.RunSSH(cmd, nil)
		if err != nil {
			return fmt.Errorf("failed to resume the network %v: %v, stdout: %s", cmd, err, stdout)
		}
	}

	// Clear any remaining DNS cache
	_, _ = h.VM.RunSSH([]string{"sudo", "systemd-resolve", "--flush-caches"}, nil)

	stdout, err := h.VM.RunSSH([]string{"sudo", "iptables", "-L", "OUTPUT"}, nil)
	if err != nil {
		logrus.Warnf("Failed to list iptables rules: %v", err)
	} else {
		logrus.Debugf("Current iptables rules after recovery:\n%s", stdout.String())
	}

	return nil
}

// FixNetworkFailureForCLI removes an entry from iptables if it exists. returns an error
// if no entry for the ip:port combo exists
func (h *Harness) FixNetworkFailureForCLI(ip, port string) error {
	args := tcpNetworkTableRule(ip, port, true)
	_, err := h.SH("sudo", args...)
	if err != nil {
		return fmt.Errorf("failed to add iptables rule %v: %w", args, err)
	}
	_, _ = h.SH("sudo", "systemd-resolve", "--flush-caches")

	return nil
}

// CheckRunningContainers verifies the expected number of running containers on the VM.
func (h *Harness) CheckRunningContainers() (string, error) {
	out, err := h.VM.RunSSH([]string{"sudo", "podman", "ps", "|", "grep", "Up", "|", "wc", "-l"}, nil)
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

func (h *Harness) CheckApplicationDirectoryExist(applicationName string) error {
	_, err := h.VM.RunSSH([]string{"test", "-d", "/etc/compose/manifests/" + applicationName}, nil)
	return err
}

func (h *Harness) CheckApplicationComposeFileExist(applicationName string, ComposeFile string) error {
	_, err := h.VM.RunSSH([]string{"test", "-f", "/etc/compose/manifests/" + applicationName + "/" + ComposeFile}, nil)
	return err
}

func (h Harness) CheckApplicationStatus(deviceId string, applicationName string) (v1beta1.ApplicationStatusType, error) {
	device, err := h.GetDevice(deviceId)
	if err != nil {
		return "", fmt.Errorf("failed to get device %s: %w", deviceId, err)
	}
	for _, appStatus := range device.Status.Applications {
		if appStatus.Name == applicationName {
			return appStatus.Status, nil
		}
	}
	return "", nil // Application status not found, return empty status and no error
}

func (h *Harness) CheckEnvInjectedToApplication(envVarName string, image string) (string, error) {
	containersOutput, err := h.VM.RunSSH([]string{"sudo", "podman", "ps"}, nil)
	if err != nil {
		return "", fmt.Errorf("failed to list containers: %w", err)
	}
	lines := strings.Split(containersOutput.String(), "\n")
	var containerIDs []string
	for _, line := range lines {
		if strings.Contains(line, image) {
			parts := strings.SplitN(line, " ", 2)
			if len(parts) == 2 {
				containerID := strings.TrimSpace(parts[0])
				containerIDs = append(containerIDs, containerID)
				envOutput, err := h.VM.RunSSH([]string{"sudo", "podman", "exec", containerID, "printenv", envVarName}, nil)
				if err == nil {
					return strings.TrimSpace(envOutput.String()), nil
				}
			}
		}
	}
	if len(containerIDs) == 0 {
		logrus.Warnf("No containers with image %s found", image)
		return "", nil // No error, but also no value found
	} else {
		logrus.Warnf("Environment variable '%s' not found in any of %d containers", envVarName, len(containerIDs))
		return "", nil // No error, but also no value found
	}
}

// RunGetDevices executes "get devices" CLI command with optional arguments.
func (h *Harness) RunGetDevices(args ...string) (string, error) {
	allArgs := append([]string{"get", "devices"}, args...)
	return h.CLI(allArgs...)
}

// RunGetEvents executes "get events" CLI command with optional arguments.
func (h *Harness) RunGetEvents(args ...string) (string, error) {
	// Starting with the base command to get events
	allArgs := append([]string{"get", "events"}, args...)
	return h.CLI(allArgs...)
}

// ManageResource performs an operation ("apply", "delete", "approve" or "deny") on a specified resource.
func (h *Harness) ManageResource(operation, resource string, args ...string) (string, error) {
	switch operation {
	case "apply":
		return h.applyResourceWithTestLabels(resource)

	case "delete":
		deleteArgs := []string{"delete"}
		if resource != "" {
			deleteArgs = append(deleteArgs, resource)
		}
		deleteArgs = append(deleteArgs, args...)
		return h.CLI(deleteArgs...)

	case "approve":
		approveArgs := []string{"approve"}
		if resource != "" {
			approveArgs = append(approveArgs, resource)
		}
		approveArgs = append(approveArgs, args...)
		return h.CLI(approveArgs...)

	case "deny":
		// If no resource and no extra args → call bare `flightctl deny`
		if resource == "" && len(args) == 0 {
			return h.CLI("deny")
		}
		denyArgs := []string{"deny"}
		if resource != "" {
			denyArgs = append(denyArgs, resource)
		}
		denyArgs = append(denyArgs, args...)
		return h.CLI(denyArgs...)

	default:
		return "", fmt.Errorf("unsupported operation: %s", operation)
	}
}

// applyResourceWithTestLabels reads a YAML file, adds test labels to the resource, and applies it
func (h *Harness) applyResourceWithTestLabels(yamlPath string) (string, error) {
	// Read the YAML file
	fileBytes, err := os.ReadFile(yamlPath)
	if err != nil {
		return "", fmt.Errorf("failed to read yaml file %s: %w", yamlPath, err)
	}

	// Parse the YAML to add test labels
	modifiedYAML, err := h.addTestLabelsToYAML(string(fileBytes))
	if err != nil {
		return "", fmt.Errorf("failed to add test labels to yaml: %w", err)
	}

	// Apply the modified YAML
	return h.CLIWithStdin(modifiedYAML, "apply", "-f", "-")
}

func (h *Harness) ApplyResource(yamlPath string) (string, error) {
	// Read the YAML file
	fileBytes, err := os.ReadFile(yamlPath)
	if err != nil {
		return "", fmt.Errorf("failed to read yaml file %s: %w", yamlPath, err)
	}

	// Apply the modified YAML
	return h.CLIWithStdin(string(fileBytes), "apply", "-f", "-")
}

// addTestLabelsToYAML adds test labels to all resources in the YAML content
func (h *Harness) addTestLabelsToYAML(yamlContent string) (string, error) {
	testID := h.GetTestIDFromContext()

	// Parse the YAML document
	var resource map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlContent), &resource); err != nil {
		return "", fmt.Errorf("failed to parse yaml document: %w", err)
	}

	// Add test label to metadata
	if metadata, ok := resource["metadata"].(map[string]interface{}); ok {
		if labels, ok := metadata["labels"].(map[string]interface{}); ok {
			labels["test-id"] = testID
		} else {
			metadata["labels"] = map[string]interface{}{"test-id": testID}
		}
	} else {
		resource["metadata"] = map[string]interface{}{"labels": map[string]interface{}{"test-id": testID}}
	}

	// Marshal back to YAML
	modifiedDoc, err := yaml.Marshal(resource)
	if err != nil {
		return "", fmt.Errorf("failed to marshal modified yaml: %w", err)
	}

	return string(modifiedDoc), nil
}

func conditionExists(conditions []v1beta1.Condition, predicate func(condition *v1beta1.Condition) bool) bool {
	for _, condition := range conditions {
		if predicate(&condition) {
			return true
		}
	}
	return false
}

// ConditionExists checks if a specific condition exists for the device with the given type, status, and reason.
func ConditionExists(d *v1beta1.Device, condType v1beta1.ConditionType, condStatus v1beta1.ConditionStatus, condReason string) bool {
	return conditionExists(d.Status.Conditions, func(condition *v1beta1.Condition) bool {
		return condition.Type == condType && condition.Status == condStatus && condition.Reason == condReason
	})
}

// ConditionStatusExists returns true if the specified type and status exists on the condition slice
func ConditionStatusExists(conditions []v1beta1.Condition, condType v1beta1.ConditionType, status v1beta1.ConditionStatus) bool {
	return conditionExists(conditions, func(condition *v1beta1.Condition) bool {
		return condition.Type == condType && condition.Status == status
	})
}

func (h *Harness) WaitForClusterRegistered(deviceId string, timeout time.Duration) error {
	start := time.Now()

	// Retry loop
	for {
		// Fetch managed cluster information
		out, err := exec.Command("bash", "-c", "oc get managedcluster").CombinedOutput()
		if err != nil {
			// Ignore error and retry after sleep
			if time.Since(start) > timeout {
				return fmt.Errorf("timed out waiting for managedcluster to be registered")
			}
			time.Sleep(5 * time.Second)
			continue
		}

		// Check device status
		response, err := h.GetDeviceWithStatusSystem(deviceId)
		if err != nil {
			return err
		}
		if response == nil {
			// If response is nil, retry
			if time.Since(start) > timeout {
				return fmt.Errorf("timed out waiting for managedcluster to be registered")
			}
			time.Sleep(5 * time.Second)
			continue
		}

		device := response.JSON200
		// Check if device metadata is valid and matches the managed cluster name
		if (device != nil && device.Metadata != v1beta1.ObjectMeta{} && device.Metadata.Name != nil) {
			if strings.Contains(string(out), *device.Metadata.Name) {
				return nil // Success: managed cluster is registered
			}
		}

		// If we haven't found a match, retry after sleeping
		if time.Since(start) > timeout {
			return fmt.Errorf("timed out waiting for managedcluster to be registered")
		}
		time.Sleep(5 * time.Second)
	}
}

func (h *Harness) WaitForFileInDevice(filePath string, timeout string, polling string) (*bytes.Buffer, error) {
	readyMsg := "The file was found"
	script := fmt.Sprintf(`
				timeout=%s
				elapsed=0
				while ! sudo test -f %s; do
				if [ "$elapsed" -ge "$timeout" ]; then
					echo "Timeout waiting for %s"
					exit 1
				fi
				echo "Waiting for %s..."
				sleep 5
				elapsed=$((elapsed + %s))
				done
				echo %s
				`, timeout, filePath, filePath, filePath, polling, readyMsg)
	return h.VM.RunSSH([]string{"sudo", "bash", "-c", script}, nil)
}

// GetContext returns the Kubernetes context (KIND or OCP) or an error
func GetContext() (string, error) {
	kubeContext, err := exec.Command("kubectl", "config", "current-context").Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current kube context: %w", err)
	}
	contextStr := strings.TrimSpace(string(kubeContext))
	if strings.Contains(contextStr, "kind") {
		logrus.Debugf("The context is: %s", contextStr)
		return util.KIND, nil
	}
	contextStr = util.OCP
	logrus.Debugf("The context is: %s", contextStr)
	return contextStr, nil
}

func (h Harness) getRegistryEndpointInfo() (ip string, port string, err error) {
	context, err := GetContext()
	if err != nil {
		return "", "", fmt.Errorf("failed to get context: %w", err)
	}

	switch context {
	case util.KIND:
		registryIP, registryPort, err := net.SplitHostPort(h.RegistryEndpoint())
		if err != nil {
			return "", "", fmt.Errorf("invalid registry endpoint: %w", err)
		}
		return registryIP, registryPort, nil

	case util.OCP:
		// #nosec G204
		cmd := exec.Command("getent", "hosts", util.E2E_REGISTRY_NAME)
		var out bytes.Buffer
		cmd.Stdout = &out

		if err := cmd.Run(); err != nil {
			return "", "", fmt.Errorf("failed to run 'getent host': %w", err)
		}

		registryIP := ""

		fields := strings.Fields(out.String())
		if len(fields) > 0 {
			registryIP = strings.TrimSpace(fields[0])
		} else {
			return "", "", fmt.Errorf("no IP found")
		}

		// registryIP := strings.TrimSpace(out.String())
		var output bytes.Buffer
		// #nosec G204
		cmd = exec.Command("oc", "get", "route", util.E2E_REGISTRY_NAME, "-n", util.E2E_NAMESPACE, "-o", "jsonpath={.spec.port.targetPort}")
		cmd.Stdout = &output

		if err := cmd.Run(); err != nil {
			return "", "", fmt.Errorf("failed to run 'oc get route': %w", err)
		}

		port := strings.TrimSpace(output.String())
		if port == "" {
			return "", "", fmt.Errorf("registry port not found in route spec")
		}

		return registryIP, port, nil
	}

	return "", "", fmt.Errorf("unknown context")
}
func NewTestHarnessWithoutVM(ctx context.Context) (*Harness, error) {
	startTime := time.Now()

	baseDir, err := client.DefaultFlightctlClientConfigPath()
	if err != nil {
		return nil, fmt.Errorf("failed to get client config path: %w", err)
	}

	c, err := client.NewFromConfigFile(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)

	k8sCluster, err := kubernetesClient()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to get kubernetes cluster: %w", err)
	}

	// Create harness without VM first
	return &Harness{
		Client:    c,
		Context:   ctx,
		Cluster:   k8sCluster,
		ctxCancel: cancel,
		startTime: startTime,
		VM:        nil,
	}, nil

}

// NewTestHarnessWithVMPool creates a new test harness with VM pool management.
// This centralizes the VM pool logic that was previously duplicated in individual tests.
func NewTestHarnessWithVMPool(ctx context.Context, workerID int) (*Harness, error) {
	startTime := time.Now()

	baseDir, err := client.DefaultFlightctlClientConfigPath()
	if err != nil {
		return nil, fmt.Errorf("failed to get client config path: %w", err)
	}

	c, err := client.NewFromConfigFile(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)

	k8sCluster, err := kubernetesClient()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to get kubernetes cluster: %w", err)
	}

	// Initialize git repository management
	gitWorkDir := filepath.Join(GinkgoT().TempDir(), "git-repos")
	err = os.MkdirAll(gitWorkDir, 0755)
	Expect(err).ToNot(HaveOccurred())

	// Create harness without VM first
	harness := &Harness{
		Client:     c,
		Context:    ctx,
		Cluster:    k8sCluster,
		ctxCancel:  cancel,
		startTime:  startTime,
		VM:         nil,
		gitRepos:   make(map[string]string),
		gitWorkDir: gitWorkDir,
	}

	// Get VM from the pool (this should already exist from BeforeSuite)
	_, err = harness.GetVMFromPool(workerID)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to get VM from pool: %w", err)
	}

	return harness, nil
}

// GetVMFromPool retrieves a VM from the pool for the given worker ID.
// VMs are created on-demand if they don't already exist in the pool.
func (h *Harness) GetVMFromPool(workerID int) (vm.TestVMInterface, error) {
	// Get VM from the global pool (created on-demand if needed)
	testVM, err := SetupVMForWorker(workerID, os.TempDir(), 2233)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM from pool for worker %d: %w", workerID, err)
	}

	// Set the VM in the harness (one-to-one relationship)
	h.VM = testVM

	return testVM, nil
}

// SetupVMFromPool sets up a VM from the pool and reverts it to the pristine snapshot.
// It does not start the agent.
func (h *Harness) SetupVMFromPool(workerID int) error {
	// Get VM from pool (created on-demand if needed)
	testVM, err := h.GetVMFromPool(workerID)
	if err != nil {
		return fmt.Errorf("failed to get VM from pool: %w", err)
	}

	// Revert to pristine snapshot
	if err := testVM.RevertToSnapshot("pristine"); err != nil {
		return fmt.Errorf("failed to revert to pristine snapshot: %w", err)
	}

	// Wait for SSH to be ready
	if err := testVM.WaitForSSHToBeReady(); err != nil {
		return fmt.Errorf("failed to wait for SSH: %w", err)
	}

	// Clean any stale CSR from previous tests
	_, err = testVM.RunSSH([]string{"sudo", "rm", "-f", "/var/lib/flightctl/certs/agent.csr"}, nil)
	if err != nil {
		logrus.Warnf("Failed to clean stale CSR: %v", err)
	}

	// Print agent files right after snapshot revert - should be empty/version 0
	printAgentFilesForVM(testVM, "After Snapshot Revert")

	return nil
}

// SetupVMFromPoolAndStartAgent sets up a VM from the pool, reverts to pristine snapshot,
// and starts the agent. This is useful for tests that use the VM pool pattern.
func (h *Harness) SetupVMFromPoolAndStartAgent(workerID int) error {
	if err := h.SetupVMFromPool(workerID); err != nil {
		return err
	}
	testVM := h.VM

	// Start the agent after snapshot revert
	GinkgoWriter.Printf("🔄 Starting flightctl-agent after snapshot revert\n")
	if _, err := testVM.RunSSH([]string{"sudo", "systemctl", "start", "flightctl-agent"}, nil); err != nil {
		return fmt.Errorf("failed to start flightctl-agent: %w", err)
	}
	GinkgoWriter.Printf("✅ flightctl-agent started successfully after snapshot revert\n")

	return nil
}

// SetTestContext sets the context for the current test.
// This allows tests to use their own context for operations while keeping
// the suite context for cleanup operations.
func (h *Harness) SetTestContext(ctx context.Context) {
	if testID, ok := ctx.Value(util.TestIDKey).(string); ok && testID != "" {
		GinkgoWriter.Printf("SetTestContext called with test ID: %s\n", testID)
	} else {
		GinkgoWriter.Printf("SetTestContext called with context that has NO test ID\n")
	}
	h.Context = ctx
}

// GetTestContext returns the current test context.
// If no test context has been set, it returns the suite context.
func (h *Harness) GetTestContext() context.Context {
	return h.Context
}

// GetTestIDFromContext retrieves the test ID from the context
// If no test ID is found, it indicates a programming error and will cause the test to fail
func (h *Harness) GetTestIDFromContext() string {
	if testID, ok := h.Context.Value(util.TestIDKey).(string); ok && testID != "" {
		GinkgoWriter.Printf("Harness using test ID: %s\n", testID)
		return testID
	}

	// This should never happen if the test context is set up correctly
	Fail("Test ID not found in context - this indicates the test context was not properly initialized with StartSpecTracerForGinkgo")
	return "" // This line will never be reached, but needed for compilation
}

// StoreDeviceInTestContext stores device data in the test context for use within the same test
func (h *Harness) StoreDeviceInTestContext(deviceId string, device *v1beta1.Device) {
	ctx := context.WithValue(h.Context, util.DeviceIDKey, deviceId)
	ctx = context.WithValue(ctx, util.DeviceKey, device)
	h.Context = ctx
}

// GetDeviceFromTestContext retrieves device data from the test context
func (h *Harness) GetDeviceFromTestContext() (string, *v1beta1.Device, bool) {
	deviceId, hasDeviceId := h.Context.Value(util.DeviceIDKey).(string)
	device, hasDevice := h.Context.Value(util.DeviceKey).(*v1beta1.Device)
	return deviceId, device, hasDeviceId && hasDevice
}

// StoreTestDataInContext stores arbitrary test data in the context using a string key
func (h *Harness) StoreTestDataInContext(key string, value interface{}) {
	// Create a nested context structure to store multiple test data values
	testData, _ := h.Context.Value(util.TestContextKey).(map[string]interface{})
	if testData == nil {
		testData = make(map[string]interface{})
	}
	testData[key] = value
	h.Context = context.WithValue(h.Context, util.TestContextKey, testData)
}

// GetTestDataFromContext retrieves arbitrary test data from the context using a string key
func (h *Harness) GetTestDataFromContext(key string) (interface{}, bool) {
	testData, ok := h.Context.Value(util.TestContextKey).(map[string]interface{})
	if !ok {
		return nil, false
	}
	value, exists := testData[key]
	return value, exists
}

// addTestLabelToResource adds the test ID as a label to the resource metadata
func (h *Harness) addTestLabelToResource(metadata *v1beta1.ObjectMeta) {
	testID := h.GetTestIDFromContext()

	if metadata.Labels == nil {
		metadata.Labels = &map[string]string{}
	}

	(*metadata.Labels)["test-id"] = testID
}

// TODO: Modify addTestLabelsToYAML to include other labels and remove addLabelsToYAML
func (h *Harness) AddLabelsToYAML(yamlContent string, addLabels map[string]string) (string, error) {
	// Parse the YAML document
	var resource map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlContent), &resource); err != nil {
		return "", fmt.Errorf("failed to parse yaml document: %w", err)
	}

	// Add labels to metadata
	if metadata, ok := resource["metadata"].(map[string]interface{}); ok {
		if labels, ok := metadata["labels"].(map[string]interface{}); ok {
			for key, value := range addLabels {
				labels[key] = value
			}
		} else {
			metadata["labels"] = addLabels
		}
	} else {
		resource["metadata"] = map[string]interface{}{"labels": addLabels}
	}

	// Marshal back to YAML
	modifiedDoc, err := yaml.Marshal(resource)
	if err != nil {
		return "", fmt.Errorf("failed to marshal modified yaml: %w", err)
	}
	return string(modifiedDoc), nil
}

func (h *Harness) addTestLabelToEnrollmentApprovalRequest(approval *v1beta1.EnrollmentRequestApproval) {
	testID := h.GetTestIDFromContext()

	if approval.Labels == nil {
		approval.Labels = &map[string]string{}
	}

	(*approval.Labels)["test-id"] = testID
}

// SetLabelsForResource sets labels on any resource while preserving the test-id label
func (h *Harness) SetLabelsForResource(metadata *v1beta1.ObjectMeta, labels map[string]string) {
	testID := h.GetTestIDFromContext()

	metadata.Labels = &map[string]string{}

	// Always preserve the test-id label
	(*metadata.Labels)["test-id"] = testID

	// Add the provided labels
	for key, value := range labels {
		(*metadata.Labels)[key] = value
	}
	GinkgoWriter.Printf("Set labels for resource %s: %v", metadata.Name, metadata.Labels)
}

// SetLabelsForDeviceMetadata sets labels on device metadata while preserving the test-id label
func (h *Harness) SetLabelsForDeviceMetadata(metadata *v1beta1.ObjectMeta, labels map[string]string) {
	h.SetLabelsForResource(metadata, labels)
}

// SetLabelsForFleetMetadata sets labels on fleet metadata while preserving the test-id label
func (h *Harness) SetLabelsForFleetMetadata(metadata *v1beta1.ObjectMeta, labels map[string]string) {
	h.SetLabelsForResource(metadata, labels)
}

// SetLabelsForRepositoryMetadata sets labels on repository metadata while preserving the test-id label
func (h *Harness) SetLabelsForRepositoryMetadata(metadata *v1beta1.ObjectMeta, labels map[string]string) {
	h.SetLabelsForResource(metadata, labels)
}

// GetGitServerConfig returns the configuration for the e2e git server
func (h *Harness) GetGitServerConfig() GitServerConfig {
	// Default configuration for the e2e git server
	return GitServerConfig{
		Host:     getEnvOrDefault("E2E_GIT_SERVER_HOST", "localhost"),
		Port:     getEnvOrDefaultInt("E2E_GIT_SERVER_PORT", 3222),
		User:     getEnvOrDefault("E2E_GIT_SERVER_USER", "user"),
		Password: getEnvOrDefault("E2E_GIT_SERVER_PASSWORD", "user"),
	}
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvOrDefaultInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

// CreateGitRepositoryOnServer creates a new Git repository on the e2e git server
func (h *Harness) CreateGitRepositoryOnServer(repoName string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	config := h.GetGitServerConfig()

	// Use SSH to create the repository on the git server
	createCmd := fmt.Sprintf("create-repo %s", repoName)
	err := h.runGitServerSSHCommand(config, createCmd)
	if err != nil {
		return fmt.Errorf("failed to create git repository %s: %w", repoName, err)
	}

	// Store the repository name for cleanup
	h.gitRepos[repoName] = fmt.Sprintf("ssh://%s@%s:%d/home/user/repos/%s.git",
		config.User, config.Host, config.Port, repoName)

	logrus.Infof("Created git repository: %s on git server", repoName)
	return nil
}

// DeleteGitRepositoryOnServer deletes a Git repository from the e2e git server
func (h *Harness) DeleteGitRepositoryOnServer(repoName string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	config := h.GetGitServerConfig()

	// Use SSH to delete the repository on the git server
	deleteCmd := fmt.Sprintf("delete-repo %s", repoName)
	err := h.runGitServerSSHCommand(config, deleteCmd)
	if err != nil {
		return fmt.Errorf("failed to delete git repository %s: %w", repoName, err)
	}

	// Remove from our tracking
	delete(h.gitRepos, repoName)

	logrus.Infof("Deleted git repository: %s from git server", repoName)
	return nil
}

// runGitServerSSHCommand executes a command on the git server via SSH
func (h *Harness) runGitServerSSHCommand(config GitServerConfig, command string) error {
	// #nosec G204 -- This is test code with controlled inputs from GitServerConfig
	sshCmd := exec.Command("sshpass", "-e", "ssh",
		"-p", fmt.Sprintf("%d", config.Port),
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=no",
		"-o", "PubkeyAuthentication=no",
		"-o", "LogLevel=ERROR",
		fmt.Sprintf("%s@%s", config.User, config.Host),
		command)
	sshCmd.Env = append(os.Environ(), fmt.Sprintf("SSHPASS=%s", config.Password))

	output, err := sshCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("SSH command failed: %w, output: %s", err, string(output))
	}

	logrus.Debugf("SSH command executed successfully: %s", command)
	return nil
}

// CloneGitRepositoryFromServer clones a repository from the git server to a local working directory
func (h *Harness) CloneGitRepositoryFromServer(repoName, localPath string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if localPath == "" {
		return fmt.Errorf("local path cannot be empty")
	}

	config := h.GetGitServerConfig()
	repoURL := fmt.Sprintf("ssh://%s@%s:%d/home/user/repos/%s.git",
		config.User, config.Host, config.Port, repoName)

	// Create parent directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	// Use sshpass for authentication when cloning
	// #nosec G204 -- This is test code with controlled inputs from GitServerConfig
	cloneCmd := exec.Command("sshpass", "-e", "git", "clone", repoURL, localPath)
	cloneCmd.Env = append(os.Environ(),
		"SSHPASS="+config.Password,
		"GIT_SSH_COMMAND=sshpass -e ssh -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o PubkeyAuthentication=no")

	if output, err := cloneCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to clone repository %s to %s: %w, output: %s", repoURL, localPath, err, string(output))
	}

	logrus.Infof("Cloned git repository %s to %s", repoName, localPath)
	return nil
}

// PushContentToGitServerRepo pushes content to a git repository on the server
func (h *Harness) PushContentToGitServerRepo(repoName, filePath, content, commitMessage string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if filePath == "" {
		return fmt.Errorf("file path cannot be empty")
	}
	if commitMessage == "" {
		commitMessage = "Add content via test harness"
	}

	// Create a temporary working directory
	workDir := filepath.Join(h.gitWorkDir, "temp-"+uuid.New().String())
	defer os.RemoveAll(workDir)

	// Clone the repository
	if err := h.CloneGitRepositoryFromServer(repoName, workDir); err != nil {
		return fmt.Errorf("failed to clone repository for push: %w", err)
	}

	// Write content to file
	fullFilePath := filepath.Join(workDir, filePath)
	if err := os.MkdirAll(filepath.Dir(fullFilePath), 0755); err != nil {
		return fmt.Errorf("failed to create directory for file: %w", err)
	}

	if err := os.WriteFile(fullFilePath, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write content to file: %w", err)
	}

	// Git operations with authentication
	config := h.GetGitServerConfig()
	gitEnv := append(os.Environ(),
		"GIT_SSH_COMMAND=sshpass -e ssh -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o PubkeyAuthentication=no",
		"SSHPASS="+config.Password,
		"GIT_AUTHOR_NAME=Test Harness",
		"GIT_AUTHOR_EMAIL=test@flightctl.dev",
		"GIT_COMMITTER_NAME=Test Harness",
		"GIT_COMMITTER_EMAIL=test@flightctl.dev",
	)

	gitCmds := [][]string{
		{"git", "add", filePath},
		{"git", "commit", "-m", commitMessage},
		{"git", "push", "origin", "main"},
	}

	for _, gitCmd := range gitCmds {
		// #nosec G204 -- This is test code with controlled git commands (add, commit, push)
		cmd := exec.Command(gitCmd[0], gitCmd[1:]...)
		cmd.Dir = workDir
		cmd.Env = gitEnv

		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to execute git command %v: %w, output: %s", gitCmd, err, string(output))
		}
	}

	logrus.Infof("Pushed content to git repository %s, file: %s", repoName, filePath)
	return nil
}

// CreateRepository creates a Repository resource pointing to the git server repository
func (h *Harness) CreateGitRepository(repoName string, repositorySpec v1beta1.RepositorySpec) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	// First create the git repository on the server
	if err := h.CreateGitRepositoryOnServer(repoName); err != nil {
		return fmt.Errorf("failed to create git repository on server: %w", err)
	}

	// Create the Repository resource
	repository := v1beta1.Repository{
		ApiVersion: v1beta1.RepositoryAPIVersion,
		Kind:       v1beta1.RepositoryKind,
		Metadata: v1beta1.ObjectMeta{
			Name: &repoName,
		},
		Spec: repositorySpec,
	}

	_, err := h.Client.CreateRepositoryWithResponse(h.Context, repository)
	if err != nil {
		// Clean up the git repository if Repository resource creation fails
		if cleanupErr := h.DeleteGitRepositoryOnServer(repoName); cleanupErr != nil {
			logrus.Errorf("failed to delete git repository %s: %v", repoName, cleanupErr)
		}
		return fmt.Errorf("failed to create Repository resource: %w", err)
	}

	logrus.Infof("Created Repository resource %s", repoName)
	return nil
}

// UpdateGitServerRepository updates content in an existing git repository working directory
func (h *Harness) UpdateGitServerRepository(repoName, filePath, content, commitMessage string) error {
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if filePath == "" {
		return fmt.Errorf("file path cannot be empty")
	}
	if commitMessage == "" {
		commitMessage = "Update content via test harness"
	}

	return h.PushContentToGitServerRepo(repoName, filePath, content, commitMessage)
}

// CreateResourceSync creates a ResourceSync resource that points to a git repository
func (h *Harness) CreateResourceSync(name, repoName string, spec v1beta1.ResourceSyncSpec) error {
	if name == "" {
		return fmt.Errorf("ResourceSync name cannot be empty")
	}
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	// Set the repository name in the spec if not already set
	if spec.Repository == "" {
		spec.Repository = repoName
	}

	resourceSync := v1beta1.ResourceSync{
		ApiVersion: v1beta1.ResourceSyncAPIVersion,
		Kind:       v1beta1.ResourceSyncKind,
		Metadata: v1beta1.ObjectMeta{
			Name: &name,
		},
		Spec: spec,
	}

	_, err := h.Client.CreateResourceSyncWithResponse(h.Context, resourceSync)
	if err != nil {
		return fmt.Errorf("failed to create ResourceSync: %w", err)
	}

	logrus.Infof("Created ResourceSync %s pointing to repository %s", name, repoName)
	return nil
}

// ReplaceResourceSync replaces an existing ResourceSync resource
func (h *Harness) ReplaceResourceSync(name, repoName string, spec v1beta1.ResourceSyncSpec) error {
	if name == "" {
		return fmt.Errorf("ResourceSync name cannot be empty")
	}
	if repoName == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	// Set the repository name in the spec if not already set
	if spec.Repository == "" {
		spec.Repository = repoName
	}

	resourceSync := v1beta1.ResourceSync{
		ApiVersion: v1beta1.ResourceSyncAPIVersion,
		Kind:       v1beta1.ResourceSyncKind,
		Metadata: v1beta1.ObjectMeta{
			Name: &name,
		},
		Spec: spec,
	}

	_, err := h.Client.ReplaceResourceSyncWithResponse(h.Context, name, resourceSync)
	if err != nil {
		return fmt.Errorf("failed to replace ResourceSync: %w", err)
	}

	logrus.Infof("Replaced ResourceSync %s pointing to repository %s", name, repoName)
	return nil
}

// DeleteResourceSync deletes the specified ResourceSync
func (h *Harness) DeleteResourceSync(name string) error {
	if name == "" {
		return fmt.Errorf("ResourceSync name cannot be empty")
	}

	_, err := h.Client.DeleteResourceSync(h.Context, name)
	if err != nil {
		return fmt.Errorf("failed to delete ResourceSync: %w", err)
	}

	logrus.Infof("Deleted ResourceSync %s", name)
	return nil
}

// CreateFleetConfigInGitRepo creates a fleet configuration and pushes it to a git repository
func (h *Harness) CreateFleetConfigInGitRepo(repoName, fleetName string, fleetSpec v1beta1.FleetSpec) error {
	fleet := v1beta1.Fleet{
		ApiVersion: v1beta1.FleetAPIVersion,
		Kind:       v1beta1.FleetKind,
		Metadata: v1beta1.ObjectMeta{
			Name: &fleetName,
		},
		Spec: fleetSpec,
	}

	fleetYAML, err := yaml.Marshal(fleet)
	if err != nil {
		return fmt.Errorf("failed to marshal fleet to YAML: %w", err)
	}

	filePath := fmt.Sprintf("fleets/%s.yaml", fleetName)
	commitMessage := fmt.Sprintf("Add fleet configuration: %s", fleetName)

	return h.PushContentToGitServerRepo(repoName, filePath, string(fleetYAML), commitMessage)
}

// CreateDeviceConfigInGitRepo creates a device configuration and pushes it to a git repository
func (h *Harness) CreateDeviceConfigInGitRepo(repoName, deviceName string, deviceSpec v1beta1.DeviceSpec) error {
	device := v1beta1.Device{
		ApiVersion: v1beta1.DeviceAPIVersion,
		Kind:       v1beta1.DeviceKind,
		Metadata: v1beta1.ObjectMeta{
			Name: &deviceName,
		},
		Spec: &deviceSpec,
	}

	deviceYAML, err := yaml.Marshal(device)
	if err != nil {
		return fmt.Errorf("failed to marshal device to YAML: %w", err)
	}

	filePath := fmt.Sprintf("devices/%s.yaml", deviceName)
	commitMessage := fmt.Sprintf("Add device configuration: %s", deviceName)

	return h.PushContentToGitServerRepo(repoName, filePath, string(deviceYAML), commitMessage)
}

// WaitForResourceSyncStatus waits for a ResourceSync to reach a specific status
func (h *Harness) WaitForResourceSyncStatus(name string, expectedStatus v1beta1.ConditionStatus, timeout string) error {
	Eventually(func() error {
		response, err := h.Client.GetResourceSyncWithResponse(h.Context, name)
		if err != nil {
			return fmt.Errorf("failed to get ResourceSync: %w", err)
		}

		if response.JSON200 == nil {
			return fmt.Errorf("ResourceSync not found")
		}

		resourceSync := response.JSON200
		if resourceSync.Status == nil {
			return fmt.Errorf("ResourceSync status is nil")
		}

		if len(resourceSync.Status.Conditions) == 0 {
			return fmt.Errorf("ResourceSync has no conditions")
		}

		// Check if the expected status is present in conditions
		for _, condition := range resourceSync.Status.Conditions {
			if condition.Type == "ResourceSyncStatus" && condition.Status == expectedStatus {
				return nil
			}
		}

		return fmt.Errorf("ResourceSync %s has not reached expected status %s", name, expectedStatus)
	}, timeout, POLLING).Should(BeNil())

	logrus.Infof("ResourceSync %s reached expected status %s", name, expectedStatus)
	return nil
}

// GetGitRepoURL returns the SSH URL for a git repository on the server
func (h *Harness) GetGitRepoURL(repoName string) (string, error) {
	url, exists := h.gitRepos[repoName]
	if !exists {
		return "", fmt.Errorf("git repository %s not found in harness", repoName)
	}
	return url, nil
}

// CleanupGitRepositories removes all git repositories created by the harness
func (h *Harness) CleanupGitRepositories() error {
	for repoName := range h.gitRepos {
		if err := h.DeleteGitRepositoryOnServer(repoName); err != nil {
			logrus.Errorf("Failed to remove git repository %s: %v", repoName, err)
		} else {
			logrus.Infof("Cleaned up git repository %s", repoName)
		}
	}

	// Clean up the local git working directory
	if err := os.RemoveAll(h.gitWorkDir); err != nil {
		logrus.Errorf("Failed to remove git working directory %s: %v", h.gitWorkDir, err)
		return err
	}

	h.gitRepos = make(map[string]string)
	logrus.Info("Cleaned up all git repositories")
	return nil
}

// CreateGitRepositoryWithContent creates a git repository with initial content
func (h *Harness) CreateGitRepositoryWithContent(repoName, filePath, content string, repositorySpec v1beta1.RepositorySpec) error {
	// Create the git repository and Repository resource
	if err := h.CreateGitRepository(repoName, repositorySpec); err != nil {
		return fmt.Errorf("failed to create git repository: %w", err)
	}

	// Add initial content if provided
	if filePath != "" && content != "" {
		if err := h.PushContentToGitServerRepo(repoName, filePath, content, "Initial commit"); err != nil {
			// Clean up on failure
			if cleanupErr := h.DeleteGitRepositoryOnServer(repoName); cleanupErr != nil {
				logrus.Errorf("failed to delete git repository %s: %v", repoName, cleanupErr)
			}
			return fmt.Errorf("failed to push initial content: %w", err)
		}
	}

	return nil
}

// ChangeK8sContext changes the kubernetes context
func (h *Harness) ChangeK8sContext(ctx context.Context, k8sContext string) (string, error) {
	if !util.BinaryExistsOnPath("oc") {
		return "", fmt.Errorf("oc binary not found in PATH")
	}
	cmd := exec.CommandContext(ctx, "oc", "config", "use-context", k8sContext)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if the error is due to context timeout
		if ctx.Err() == context.DeadlineExceeded {
			GinkgoWriter.Printf("❌ Failed to change K8s context to %s: context timeout\n", k8sContext)
			return string(output), fmt.Errorf("failed to change K8s context to %s: context timeout", k8sContext)
		}
		GinkgoWriter.Printf("❌ Failed to change K8s context to %s: %v\n", k8sContext, err)
		return string(output), fmt.Errorf("failed to change K8s context to %s: %w", k8sContext, err)
	} else {
		GinkgoWriter.Printf("✅ Changed context to %s: %s\n", k8sContext, output)
		return string(output), nil
	}
}

func (h *Harness) CreateResource(resourceType string) (string, string, []byte, error) {
	uniqueResourceYAML, err := util.CreateUniqueYAMLFile(resourceType+".yaml", h.GetTestIDFromContext())
	if err != nil {
		return "", "", nil, err
	}
	defer util.CleanupTempYAMLFile(uniqueResourceYAML)

	// ManageResource applies the resource to the cluster with test labels
	applyOutput, err := h.ManageResource("apply", uniqueResourceYAML)
	if err != nil {
		return applyOutput, "", nil, err
	}
	if strings.Contains(applyOutput, "201 Created") || strings.Contains(applyOutput, "200 OK") {
		var resource interface{}
		var resourceName *string

		switch resourceType {
		case "device":
			device := h.GetDeviceByYaml(uniqueResourceYAML)
			updatedDevice, err := h.GetDevice(*device.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			resource = updatedDevice
			resourceName = updatedDevice.Metadata.Name
		case "fleet":
			fleet := h.GetFleetByYaml(uniqueResourceYAML)
			updatedFleet, err := h.GetFleet(*fleet.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			resource = updatedFleet
			resourceName = updatedFleet.Metadata.Name
		case "repository":
			repo := h.GetRepositoryByYaml(uniqueResourceYAML)
			updatedRepo, err := h.GetRepository(*repo.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			resource = updatedRepo
			resourceName = updatedRepo.Metadata.Name
		default:
			return applyOutput, "", nil, fmt.Errorf("unsupported resource type: %s", resourceType)
		}

		// Remove resourceVersion before marshaling to avoid conflicts
		switch resource := resource.(type) {
		case *v1beta1.Device:
			resource.Metadata.ResourceVersion = nil
		case *v1beta1.Fleet:
			resource.Metadata.ResourceVersion = nil
		case *v1beta1.Repository:
			resource.Metadata.ResourceVersion = nil
		}

		resourceData, err := yaml.Marshal(resource)
		if err != nil {
			return applyOutput, "", nil, err
		}
		return applyOutput, *resourceName, resourceData, nil
	} else {
		GinkgoWriter.Printf("Apply output: %s\n", applyOutput)
		return applyOutput, "", nil, fmt.Errorf("Failed to create a %s", resourceType)
	}
}

// GetDefaultK8sContext returns the a K8s context with default in its name
func (h *Harness) GetDefaultK8sContext() (string, error) {
	cmd := exec.Command("kubectl", "config", "get-contexts", "-o", "name")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get contexts: %v", err)
	}

	contexts := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, context := range contexts {
		if strings.Contains(context, "default") {
			GinkgoWriter.Printf("🔍 [DEBUG] Found default context: %s\n", context)
			return context, nil
		}
	}
	return "", fmt.Errorf("no context with 'default' in name found")
}

// GetK8sApiEndpoint returns the API endpoint for a given K8s context
func (h *Harness) GetK8sApiEndpoint(ctx context.Context, k8sContext string) (string, error) {
	if !util.BinaryExistsOnPath("oc") {
		return "", fmt.Errorf("oc binary not found on PATH")
	}
	if _, err := h.ChangeK8sContext(ctx, k8sContext); err != nil {
		return "", fmt.Errorf("failed to change K8s context to %s: %w", k8sContext, err)
	}
	cmd := exec.Command("bash", "-c", "oc whoami --show-server")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get Kubernetes API endpoint: %v", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// ExecuteResourceOperations tests all CRUD operations for the given resource types
// shouldSucceed determines whether the operations are expected to succeed or fail
func ExecuteResourceOperations(ctx context.Context, harness *Harness, resourceTypes []string, shouldSucceed bool, testLabels *map[string]string, namespace string, operations []string) error {
	for _, resourceType := range resourceTypes {
		By(fmt.Sprintf("Testing %s operations - should %s", resourceType, map[bool]string{true: "succeed", false: "fail"}[shouldSucceed]))

		// Check if OperationAll is in the operations list
		operationsToExecute := operations
		for _, op := range operations {
			if op == OperationAll {
				operationsToExecute = []string{OperationCreate, OperationUpdate, OperationGet, OperationList, OperationDelete}
				break
			}
		}

		// Execute operations in the order they appear in operationsToExecute
		var resourceName string
		var resourceData []byte
		var err error

		for _, operation := range operationsToExecute {
			if err = executeResourceOperation(harness, operation, resourceType, &resourceName, &resourceData, shouldSucceed, testLabels); err != nil {
				return err
			}
		}
	}
	return nil
}

// executeResourceOperation handles a single operation for a resource type
func executeResourceOperation(harness *Harness, operation, resourceType string, resourceName *string, resourceData *[]byte, shouldSucceed bool, testLabels *map[string]string) error {
	var output string
	var err error
	var operationName string

	switch operation {
	case OperationCreate:
		operationName = "creating"
		GinkgoWriter.Printf("Testing creating a %s", resourceType)
		output, *resourceName, *resourceData, err = harness.CreateResource(resourceType)
	case OperationUpdate:
		operationName = "updating"
		GinkgoWriter.Printf("Testing updating a %s", resourceType)
		updatedResourceData, updateErr := harness.AddLabelsToYAML(string(*resourceData), *testLabels)
		if updateErr != nil {
			return fmt.Errorf("failed to add labels to YAML: %w", updateErr)
		}
		output, err = harness.CLIWithStdin(updatedResourceData, "apply", "-f", "-")
	case OperationGet:
		operationName = "getting specific"
		GinkgoWriter.Printf("Testing getting a specific %s", resourceType)
		output, err = harness.GetResourcesByName(resourceType, *resourceName)
	case OperationList:
		operationName = "listing"
		GinkgoWriter.Printf("Testing listing %s", resourceType)
		output, err = harness.GetResourcesByName(resourceType)
	case OperationDelete:
		operationName = "deleting"
		GinkgoWriter.Printf("Testing deleting a %s", resourceType)
		output, err = harness.CLI("delete", resourceType, *resourceName)
	default:
		return fmt.Errorf("unknown operation: %s", operation)
	}

	return validateResourceOperationResult(operationName, resourceType, output, err, shouldSucceed)
}

// validateResourceOperationResult validates the result of an operation based on shouldSucceed flag
func validateResourceOperationResult(operationName, resourceType, output string, err error, shouldSucceed bool) error {
	if shouldSucceed {
		if err != nil {
			return fmt.Errorf("%s %s should succeed but failed: %w", operationName, resourceType, err)
		}
	} else {
		if err == nil {
			return fmt.Errorf("%s %s should fail but succeeded", operationName, resourceType)
		}
		if !strings.Contains(output, "403") {
			return fmt.Errorf("%s %s should fail with error code 403, got: %s", operationName, resourceType, output)
		}
	}
	return nil
}

// ExecuteReadOnlyResourceOperations tests read-only operations for the given resource types
// shouldSucceed determines whether the operations are expected to succeed or fail
func ExecuteReadOnlyResourceOperations(harness *Harness, resourceTypes []string, shouldSucceed bool) error {
	for _, resourceType := range resourceTypes {
		By(fmt.Sprintf("Testing %s operations - should %s", resourceType, map[bool]string{true: "succeed", false: "fail"}[shouldSucceed]))
		By(fmt.Sprintf("Testing listing %s", resourceType))
		_, err := harness.GetResourcesByName(resourceType)
		if shouldSucceed {
			if err != nil {
				return fmt.Errorf("admin should be able to list %s but failed: %w", resourceType, err)
			}
		} else {
			if err == nil {
				return fmt.Errorf("listing %s should fail but succeeded", resourceType)
			}

		}
	}
	return nil
}

// GetVersionsFromCLI returns client, server, and agent versions from the flightctl CLI version command
func (h *Harness) GetVersionsFromCLI() (clientVersion, serverVersion, agentVersion string, err error) {

	versionOutput, err := h.CLI("version")
	if err != nil {
		return "", "", "", fmt.Errorf("failed to get version from CLI: %w", err)
	}

	// Get client version
	clientVersionPrefix := "Client Version:"
	if !strings.Contains(versionOutput, clientVersionPrefix) {
		return "", "", "", fmt.Errorf("client version not found in CLI output: %s", versionOutput)
	}

	clientVersion = h.getVersionByPrefix(versionOutput, clientVersionPrefix)
	if clientVersion == "" {
		return "", "", "", fmt.Errorf("failed to parse client version from CLI output")
	}

	// Get server version
	serverVersionPrefix := "Server Version:"
	if !strings.Contains(versionOutput, serverVersionPrefix) {
		return "", "", "", fmt.Errorf("server version not found in CLI output: %s", versionOutput)
	}

	serverVersion = h.getVersionByPrefix(versionOutput, serverVersionPrefix)
	if serverVersion == "" {
		return "", "", "", fmt.Errorf("failed to parse server version from CLI output")
	}

	// Agent version requires a VM; skip if not initialized so client/server callers don't fail.
	if h.VM == nil {
		logrus.Debug("VM not initialized; skipping agent version lookup")
		return clientVersion, serverVersion, "", nil
	}

	// Get agent version from flightctl-agent version command
	agentVersion, err = h.GetAgentVersion()
	if err != nil {
		return "", "", "", fmt.Errorf("failed to get agent version: %w", err)
	}

	return clientVersion, serverVersion, agentVersion, nil
}

// GetAgentVersion returns the agent version from the flightctl-agent version command
func (h *Harness) GetAgentVersion() (string, error) {
	if h.VM == nil {
		return "", fmt.Errorf("VM is not initialized")
	}

	// Run flightctl-agent version command on the VM
	stdout, err := h.VM.RunSSH([]string{"flightctl-agent", "version"}, nil)
	if err != nil {
		return "", fmt.Errorf("failed to run flightctl-agent version: %w", err)
	}

	output := stdout.String()
	versionPrefix := "Agent Version:"
	if !strings.Contains(output, versionPrefix) {
		return "", fmt.Errorf("agent version not found in output: %s", output)
	}

	// Extract version using the existing helper function
	agentVersion := h.getVersionByPrefix(output, versionPrefix)
	if agentVersion == "" {
		return "", fmt.Errorf("failed to parse agent version from output")
	}

	return agentVersion, nil
}

// GetAgentVersionFromLogs returns the agent version from the flightctl-agent service logs
func (h *Harness) GetAgentVersionFromLogs() (string, error) {
	// Get agent logs
	agentLogs, err := h.GetFlightctlAgentLogs()
	if err != nil {
		return "", fmt.Errorf("failed to get agent logs: %w", err)
	}

	// Look for version information in the logs
	// Example log line: "System information: version=v0.9.3, go-version=go1.23.9..."
	versionStart := strings.Index(agentLogs, "version=")
	if versionStart == -1 {
		return "", fmt.Errorf("version information not found in agent logs")
	}

	// Extract version from "version=v0.9.3"
	versionStart += 8 // Skip "version="
	versionEnd := strings.Index(agentLogs[versionStart:], ",")
	if versionEnd == -1 {
		versionEnd = strings.Index(agentLogs[versionStart:], " ")
		if versionEnd == -1 {
			versionEnd = len(agentLogs) - versionStart
		}
	}

	agentVersion := strings.TrimSpace(agentLogs[versionStart : versionStart+versionEnd])
	if agentVersion == "" {
		return "", fmt.Errorf("failed to extract agent version from logs")
	}

	return agentVersion, nil
}

// getVersionByPrefix searches the output for a line starting with the given prefix
// and returns the trimmed value following the prefix. Returns an empty string if not found.
func (h *Harness) getVersionByPrefix(output, prefix string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// GetYAML returns the YAML output of a given resource name via Harness.CLI.
// No test framework calls inside; returns (string, error).
func (h *Harness) GetYAML(name string) (string, error) {
	out, err := h.CLI("get", name, "-o", "yaml")
	if err != nil {
		return out, fmt.Errorf("get -o yaml failed for %s: %v\n%s", name, err, out)
	}
	return out, nil
}

// ApplyTempIfSuggested looks for "Your changes have been saved to:" in CLI output.
// If found, it automatically applies the saved file to complete the edit flow.
func (h *Harness) ApplyTempIfSuggested(out string, cliErr error) error {
	if cliErr == nil {
		return nil
	}
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "saved to:") {
		return fmt.Errorf("edit failed without saved-temp-file hint: %v\n%s", cliErr, out)
	}
	re := regexp.MustCompile(`(?i)saved to:\s+(.+?)(?:\s*$|\n)`)
	match := re.FindStringSubmatch(out)
	if len(match) < 2 {
		return fmt.Errorf("could not extract temp file path from output:\n%s", out)
	}
	tmpPath := match[1]

	applyOut, applyErr := h.CLI("apply", "-f", tmpPath)
	if applyErr != nil {
		return fmt.Errorf("failed to apply saved temp file %s:\n%s", tmpPath, applyOut)
	}
	return nil
}

var (
	editorOnce     sync.Once
	editorPath     string
	editorBuildErr error
)

func buildHeadlessEditor() (string, error) {
	editorOnce.Do(func() {
		src := `
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"sigs.k8s.io/yaml"
)

func firstNonSpace(b []byte) byte {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return c
		}
	}
	return 0
}

func ensureMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key]; ok {
		if mm, ok := v.(map[string]any); ok {
			return mm
		}
	}
	n := map[string]any{}
	m[key] = n
	return n
}

func editJSON(buf []byte, marker string) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(buf, &root); err != nil {
		return nil, err
	}
	md := ensureMap(root, "metadata")
	labels := ensureMap(md, "labels")
	labels["autotest-edit"] = marker
	return json.MarshalIndent(root, "", "  ")
}

func editYAML(buf []byte, marker string) ([]byte, error) {
	j, err := yaml.YAMLToJSON(buf)
	if err != nil {
		return nil, err
	}
	edited, err := editJSON(j, marker)
	if err != nil {
		return nil, err
	}
	y, err := yaml.JSONToYAML(edited)
	if err != nil {
		return nil, err
	}
	// YAML must not use tabs for indentation
	y = bytes.ReplaceAll(y, []byte("\t"), []byte("  "))
	return y, nil
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: editor <file> <marker>")
		os.Exit(2)
	}
	path := os.Args[1]
	marker := os.Args[2]

	buf, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		os.Exit(1)
	}
	var out []byte
	switch firstNonSpace(buf) {
	case '{':
		out, err = editJSON(buf, marker)
	case 0:
		err = errors.New("empty file")
	default:
		out, err = editYAML(buf, marker)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "edit:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, out, 0600); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
}
`
		td, err := os.MkdirTemp("", "flightctl-editor-src-*")
		if err != nil {
			editorBuildErr = fmt.Errorf("mkdtemp: %w", err)
			return
		}
		srcPath := filepath.Join(td, "main.go")
		if err := os.WriteFile(srcPath, []byte(src), 0600); err != nil {
			editorBuildErr = fmt.Errorf("write src: %w", err)
			return
		}
		bin := filepath.Join(td, "flightctl-test-editor")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin, srcPath)
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			editorBuildErr = fmt.Errorf("build editor failed: %v\n%s", err, string(out))
			return
		}
		editorPath = bin
	})
	return editorPath, editorBuildErr
}

func (h *Harness) HeadlessEditorWrapper(marker string) (string, error) {
	bin, err := buildHeadlessEditor()
	if err != nil {
		return "", err
	}

	dir, err := os.MkdirTemp("", "flightctl-editor-*")
	if err != nil {
		return "", fmt.Errorf("mkdtemp: %w", err)
	}

	wrapper := fmt.Sprintf(`#!/usr/bin/env sh
set -eu
exec %s "$1" %s
`, shellQuote(bin), shellQuote(marker))

	path := filepath.Join(dir, "fake_editor.sh")

	// Write with 0600 (passes gosec G306), then chmod to 0700.
	if err := os.WriteFile(path, []byte(wrapper), 0o600); err != nil {
		return "", fmt.Errorf("write wrapper: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return "", fmt.Errorf("chmod wrapper: %w", err)
	}
	return path, nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// EditWithRetry runs: flightctl edit -o <format> --editor <editor> <resource>
// Retries briefly if the server responds with a Conflict (resourceVersion race).
func (h *Harness) EditWithRetry(format, editor, resource string) (string, error) {
	const tries = 5
	for i := 1; i <= tries; i++ {
		out, err := h.CLI("edit", "-o", format, "--editor", editor, resource)
		// Let ApplyTempIfSuggested handle the saved-temp-file flow
		if err2 := h.ApplyTempIfSuggested(out, err); err2 == nil {
			return out, nil
		} else {
			// Detect 409 conflict and retry
			if strings.Contains(out, `"reason":"Conflict"`) || (err != nil && strings.Contains(err.Error(), "Conflict")) {
				time.Sleep(300 * time.Millisecond)
				continue
			}
			return out, err2
		}
	}
	// last attempt
	out, err := h.CLI("edit", "-o", format, "--editor", editor, resource)
	return out, h.ApplyTempIfSuggested(out, err)
}

// printAgentFilesForVM prints all agent files for debugging
// This is a shared helper function used by harness and vm_pool.go
func printAgentFilesForVM(vm vm.TestVMInterface, context string) {
	fmt.Printf("🔍 [%s] Printing agent files:\n", context)

	// Define agent file paths
	agentFiles := map[string]string{
		"current.json": "/var/lib/flightctl/current.json",
		"desired.json": "/var/lib/flightctl/desired.json",
		"agent secret": "/etc/flightctl/certs/agent.crt",
	}

	for fileType, filePath := range agentFiles {
		fmt.Printf("📄 [%s] %s:\n", context, fileType)

		// Regular file handling
		stdout, err := vm.RunSSH([]string{"sudo", "cat", filePath}, nil)
		if err != nil {
			fmt.Printf("❌ [%s] Failed to read %s: %v\n", context, fileType, err)
		} else {
			content := stdout.String()
			if content == "" {
				fmt.Printf("📄 [%s] %s: (empty or does not exist)\n", context, fileType)
			} else {
				fmt.Printf("%s\n", content)
			}
		}
		fmt.Printf("---\n")
	}
}
