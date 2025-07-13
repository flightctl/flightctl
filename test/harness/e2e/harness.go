package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	service "github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	"github.com/flightctl/flightctl/test/util"
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
const TIMEOUT = "60s"
const LONGTIMEOUT = "10m"

type Harness struct {
	VMs       []vm.TestVMInterface
	Client    *apiclient.ClientWithResponses
	Context   context.Context
	Cluster   kubernetes.Interface
	ctxCancel context.CancelFunc
	startTime time.Time

	VM vm.TestVMInterface
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

func NewTestHarness(ctx context.Context) *Harness {

	startTime := time.Now()

	testVM, err := vm.NewVM(vm.TestVM{
		TestDir:       GinkgoT().TempDir(),
		VMName:        "flightctl-e2e-vm-" + uuid.New().String(),
		DiskImagePath: filepath.Join(findTopLevelDir(), "bin/output/qcow2/disk.qcow2"),
		VMUser:        "user",
		SSHPassword:   "user",
		SSHPort:       2233, // TODO: randomize and retry on error
	})
	Expect(err).ToNot(HaveOccurred())

	baseDir, err := client.DefaultFlightctlClientConfigPath()
	Expect(err).ToNot(HaveOccurred())

	c, err := client.NewFromConfigFile(baseDir)
	Expect(err).ToNot(HaveOccurred())

	ctx, cancel := context.WithCancel(ctx)

	k8sCluster, err := kubernetesClient()
	Expect(err).ToNot(HaveOccurred(), "failed to get kubernetes cluster")

	return &Harness{
		VMs:       []vm.TestVMInterface{testVM},
		Client:    c,
		Context:   ctx,
		Cluster:   k8sCluster,
		ctxCancel: cancel,
		startTime: startTime,
		VM:        testVM,
	}
}

func (h *Harness) AddVM(vmParams vm.TestVM) (vm.TestVMInterface, error) {
	testVM, err := vm.NewVM(vmParams)
	if err != nil {
		return nil, err
	}
	h.VMs = append(h.VMs, testVM)
	return testVM, nil
}

func (h *Harness) AddMultipleVMs(vmParamsList []vm.TestVM) ([]vm.TestVMInterface, error) {
	var createdVMs []vm.TestVMInterface
	for _, params := range vmParamsList {
		vm, err := h.AddVM(params)
		if err != nil {
			return nil, err
		}
		createdVMs = append(createdVMs, vm)
	}
	return createdVMs, nil
}

// ReadPrimaryVMAgentLogs reads flightctl-agent journalctl logs from the primary VM
func (h *Harness) ReadPrimaryVMAgentLogs(since string) (string, error) {
	if h.VM == nil {
		return "", fmt.Errorf("VM is not initialized")
	}
	logs, err := h.VM.JournalLogs(vm.JournalOpts{
		Unit:  "flightctl-agent",
		Since: since,
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
	cfg.AuthInfo.AuthProvider.Config[client.AuthAccessTokenExpiryKey] = time.Now().Add(-1 * time.Minute).Format(time.RFC3339Nano)
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

	for _, vm := range h.VMs {
		if running, _ := vm.IsRunning(); running && testFailed {
			fmt.Println("VM is running, attempting to get logs and details")
			stdout, _ := vm.RunSSH([]string{"sudo", "systemctl", "status", "flightctl-agent"}, nil)
			fmt.Print("\n\n\n")
			fmt.Println("============ systemctl status flightctl-agent ============")
			fmt.Println(stdout.String())
			fmt.Println("=============== logs for flightctl-agent =================")
			fmt.Println(h.ReadPrimaryVMAgentLogs(""))
			if printConsole {
				fmt.Println("======================= VM Console =======================")
				fmt.Println(vm.GetConsoleOutput())
			}
			fmt.Println("==========================================================")
			fmt.Print("\n\n\n")
		}
		err := vm.ForceDelete()
		Expect(err).ToNot(HaveOccurred())
	}

	diffTime := time.Since(h.startTime)
	fmt.Printf("Test took %s\n", diffTime)

	// Cancel the context to stop any blocking operations
	h.ctxCancel()
}

func (h *Harness) GetEnrollmentIDFromConsole(vms ...vm.TestVMInterface) string {
	// Use the first VM if no specific VM is passed
	var selectedVM vm.TestVMInterface
	if len(vms) > 0 && vms[0] != nil {
		selectedVM = vms[0]
	} else {
		selectedVM = h.VM
	}

	// Wait for the enrollment ID on the console
	enrollmentId := ""
	Eventually(func() string {
		consoleOutput := selectedVM.GetConsoleOutput()
		enrollmentId = util.GetEnrollmentIdFromText(consoleOutput)
		return enrollmentId
	}, TIMEOUT, POLLING).ShouldNot(BeEmpty(), "Enrollment ID not found in VM console output")

	return enrollmentId
}

func (h *Harness) WaitForEnrollmentRequest(id string) *v1alpha1.EnrollmentRequest {
	var enrollmentRequest *v1alpha1.EnrollmentRequest
	Eventually(func() *v1alpha1.EnrollmentRequest {
		resp, _ := h.Client.GetEnrollmentRequestWithResponse(h.Context, id)
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

func (h *Harness) StartMultipleVMAndEnroll(count int) ([]string, error) {
	if count <= 0 {
		return nil, fmt.Errorf("count must be positive, got %d", count)
	}

	// add count-1 vms to the harness using AddMultipleVMs method
	vmParamsList := make([]vm.TestVM, count-1)
	baseDir := GinkgoT().TempDir()
	topDir := findTopLevelDir()
	baseDiskPath := filepath.Join(topDir, "bin/output/qcow2/disk.qcow2")

	for i := 0; i < count-1; i++ {
		vmName := "flightctl-e2e-vm-" + uuid.New().String()
		overlayDiskPath := filepath.Join(baseDir, fmt.Sprintf("%s-disk.qcow2", vmName))

		// Create a qcow2 overlay that uses the base image as backing file
		cmd := exec.Command(
			"qemu-img", "create",
			"-f", "qcow2",
			"-b", baseDiskPath,
			"-F", "qcow2",
			overlayDiskPath)

		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("failed to create overlay disk for VM %s: %w", vmName, err)
		}

		vmParamsList[i] = vm.TestVM{
			TestDir:       GinkgoT().TempDir(),
			VMName:        "flightctl-e2e-vm-" + uuid.New().String(),
			DiskImagePath: overlayDiskPath,
			VMUser:        "user",
			SSHPassword:   "user",
			SSHPort:       2233 + i + 1,
		}
	}

	_, err := h.AddMultipleVMs(vmParamsList)
	if err != nil {
		return nil, fmt.Errorf("failed to add multiple VMs: %w", err)
	}

	var enrollmentIDs []string

	for _, vm := range h.VMs {
		err := vm.RunAndWaitForSSH()
		if err != nil {
			return nil, fmt.Errorf("failed to run VM and wait for SSH: %w", err)
		}

		enrollmentID := h.GetEnrollmentIDFromConsole(vm)
		logrus.Infof("Enrollment ID found in VM console output: %s", enrollmentID)

		_ = h.WaitForEnrollmentRequest(enrollmentID)
		h.ApproveEnrollment(enrollmentID, util.TestEnrollmentApproval())
		logrus.Infof("Waiting for device %s to report status", enrollmentID)

		// Wait for the device to pick up enrollment and report measurements on device status
		Eventually(h.GetDeviceWithStatusSystem, TIMEOUT, POLLING).WithArguments(
			enrollmentID).ShouldNot(BeNil())

		enrollmentIDs = append(enrollmentIDs, enrollmentID)
	}

	return enrollmentIDs, nil
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
	yamlString := string(yamlData)
	Expect(err).ToNot(HaveOccurred())
	if yamlString != lastResource {
		fmt.Println("")
		fmt.Println("======================= Resource change ========================== ")
		fmt.Println(yamlString)
		fmt.Println("================================================================== ")
	}
	return yamlString
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

	Eventually(func() error {
		logrus.Infof("Waiting for condition: %q to be met", description)
		resource, err := fetch(id)
		Expect(err).NotTo(HaveOccurred())

		lastResourcePrint = checkForResourceChange(resource, lastResourcePrint)

		if condition(resource) {
			return nil
		}
		return fmt.Errorf("resource: %s not updated", id)
	}, timeout, "2s").Should(BeNil())
}

func (h *Harness) EnrollAndWaitForOnlineStatus() (string, *v1alpha1.Device) {
	deviceId := h.GetEnrollmentIDFromConsole()
	logrus.Infof("Enrollment ID found in VM console output: %s", deviceId)
	Expect(deviceId).NotTo(BeNil())

	// Wait for the approve enrollment request response to not be nil
	h.WaitForEnrollmentRequest(deviceId)

	// Approve the enrollment and wait for the device details to be populated by the agent.
	h.ApproveEnrollment(deviceId, util.TestEnrollmentApproval())

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
	Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusOnline))
	Expect(*device.Status.Summary.Info).To(Equal(service.DeviceStatusInfoHealthy))
	return deviceId, device
}

func (h *Harness) parseImageReference(image string) (string, string) {
	// Split the image string by the colon to separate the repository and the tag.
	parts := strings.Split(image, ":")

	// The tag is the last part after the last colon.
	tag := parts[len(parts)-1]

	// The repository is composed of all parts before the last colon, joined back together with colons.
	repo := strings.Join(parts[:len(parts)-1], ":")

	return repo, tag
}

func (h *Harness) CleanUpResources(resourceType string) (string, error) {
	logrus.Infof("Deleting the instances of the %s resource type", resourceType)

	resources, err := h.CLI("get", resourceType, "-o", "name")
	if err != nil {
		return "", fmt.Errorf("failed to get %s resources: %w", resourceType, err)
	}
	resources = strings.TrimSpace(resources)
	if resources == "" {
		logrus.Infof("No %s resources found to delete", resourceType)
		return "No resources to delete", nil
	}

	deleteArgs := []string{"delete", resourceType}
	resourceNames := strings.Fields(resources)
	deleteArgs = append(deleteArgs, resourceNames...)

	return h.CLI(deleteArgs...)
}

func (h *Harness) CleanUpAllResources() error {
	for _, resourceType := range util.ResourceTypes {
		_, err := h.CleanUpResources(resourceType)
		if err != nil {
			// Return the error immediately if any operation fails
			logrus.Infof("Error: %v\n", err)
			return err
		}
		logrus.Infof("The instances of the %s resource type are deleted successfully", resourceType)

	}
	logrus.Infof("All the resource instances are deleted successfully")
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
func (h *Harness) GetDeviceByYaml(deviceYaml string) v1alpha1.Device {
	return getYamlResourceByFile[v1alpha1.Device](deviceYaml)
}

// Wrapper function for Fleet
func (h *Harness) GetFleetByYaml(fleetYaml string) v1alpha1.Fleet {
	return getYamlResourceByFile[v1alpha1.Fleet](fleetYaml)
}

// Wrapper function for Repository
func (h *Harness) GetRepositoryByYaml(repoYaml string) v1alpha1.Repository {
	return getYamlResourceByFile[v1alpha1.Repository](repoYaml)
}

// Wrapper function for ResourceSync
func (h *Harness) GetResourceSyncByYaml(rSyncYaml string) v1alpha1.ResourceSync {
	return getYamlResourceByFile[v1alpha1.ResourceSync](rSyncYaml)
}

// Wrapper function for EnrollmentRequest
func (h *Harness) GetEnrollmentRequestByYaml(erYaml string) *v1alpha1.EnrollmentRequest {
	return getYamlResourceByFile[*v1alpha1.EnrollmentRequest](erYaml)
}

// Wrapper function for CertificateSigningRequest
func (h *Harness) GetCertificateSigningRequestByYaml(csrYaml string) v1alpha1.CertificateSigningRequest {
	return getYamlResourceByFile[v1alpha1.CertificateSigningRequest](csrYaml)
}

// Create a repository resource
func (h *Harness) CreateRepository(repositorySpec v1alpha1.RepositorySpec, metadata v1alpha1.ObjectMeta) error {
	var repository = v1alpha1.Repository{
		ApiVersion: v1alpha1.RepositoryAPIVersion,
		Kind:       v1alpha1.RepositoryKind,

		Metadata: metadata,
		Spec:     repositorySpec,
	}
	_, err := h.Client.CreateRepositoryWithResponse(h.Context, repository)
	return err
}

// ReplaceRepository ensures the specified repository exists and is updated to the appropriate values
func (h *Harness) ReplaceRepository(repositorySpec v1alpha1.RepositorySpec, metadata v1alpha1.ObjectMeta) error {
	var repository = v1alpha1.Repository{
		ApiVersion: v1alpha1.RepositoryAPIVersion,
		Kind:       v1alpha1.RepositoryKind,

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
	context, err := getContext()
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
		stdout, err := h.VMs[0].RunSSH(cmd, nil)
		if err != nil {
			return fmt.Errorf("failed to simulate network failure %v: %v, stdout: %s", cmd, err, stdout)
		}
	}

	stdout, err := h.VMs[0].RunSSH([]string{"sudo", "iptables", "-L", "OUTPUT"}, nil)
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
	context, err := getContext()
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
		stdout, err := h.VMs[0].RunSSH(cmd, nil)
		if err != nil {
			return fmt.Errorf("failed to resume the network %v: %v, stdout: %s", cmd, err, stdout)
		}
	}

	// Clear any remaining DNS cache
	_, _ = h.VMs[0].RunSSH([]string{"sudo", "systemd-resolve", "--flush-caches"}, nil)

	stdout, err := h.VMs[0].RunSSH([]string{"sudo", "iptables", "-L", "OUTPUT"}, nil)
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

func (h Harness) CheckApplicationStatus(deviceId string, applicationName string) (v1alpha1.ApplicationStatusType, error) {
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

// ManageResource performs an operation ("apply" or "delete") on a specified resource.
func (h *Harness) ManageResource(operation, resource string, args ...string) (string, error) {
	switch operation {
	case "apply":
		return h.CLI("apply", "-f", util.GetTestExamplesYamlPath(resource))
	case "delete":
		if len(args) > 0 {
			deleteArgs := append([]string{"delete", resource}, args...)
			return h.CLI(deleteArgs...)
		}
		if len(args) == 0 {
			return h.CLI("delete", resource)
		}
		return h.CleanUpResources(resource)
	default:
		return "", fmt.Errorf("unsupported operation: %s", operation)
	}
}

func conditionExists(conditions []v1alpha1.Condition, predicate func(condition *v1alpha1.Condition) bool) bool {
	for _, condition := range conditions {
		if predicate(&condition) {
			return true
		}
	}
	return false
}

// ConditionExists checks if a specific condition exists for the device with the given type, status, and reason.
func ConditionExists(d *v1alpha1.Device, condType v1alpha1.ConditionType, condStatus v1alpha1.ConditionStatus, condReason string) bool {
	return conditionExists(d.Status.Conditions, func(condition *v1alpha1.Condition) bool {
		return condition.Type == condType && condition.Status == condStatus && condition.Reason == condReason
	})
}

// ConditionStatusExists returns true if the specified type and status exists on the condition slice
func ConditionStatusExists(conditions []v1alpha1.Condition, condType v1alpha1.ConditionType, status v1alpha1.ConditionStatus) bool {
	return conditionExists(conditions, func(condition *v1alpha1.Condition) bool {
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
		if (device != nil && device.Metadata != v1alpha1.ObjectMeta{} && device.Metadata.Name != nil) {
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

func getContext() (string, error) {
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
	context, err := getContext()
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
