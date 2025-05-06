package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	client "github.com/flightctl/flightctl/internal/client"
	service "github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	"github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

const POLLING = "250ms"
const TIMEOUT = "60s"
const LONGTIMEOUT = "5m"

type Harness struct {
	VM        vm.TestVMInterface
	Client    *apiclient.ClientWithResponses
	Context   context.Context
	ctxCancel context.CancelFunc
	startTime time.Time
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

func NewTestHarness() *Harness {

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

	c, err := client.NewFromConfigFile(client.DefaultFlightctlClientConfigPath())
	Expect(err).ToNot(HaveOccurred())

	ctx, cancel := context.WithCancel(context.Background())

	return &Harness{
		VM:        testVM,
		Client:    c,
		Context:   ctx,
		ctxCancel: cancel,
		startTime: startTime,
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

	diffTime := time.Since(h.startTime)
	fmt.Printf("Test took %s\n", diffTime)
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

func (h *Harness) GetDeviceWithStatusSystem(enrollmentID string) *apiclient.GetDeviceResponse {
	device, err := h.Client.GetDeviceWithResponse(h.Context, enrollmentID)
	Expect(err).NotTo(HaveOccurred())
	// we keep waiting for a 200 response, with filled in Status.SystemInfo
	if device.JSON200 == nil || device.JSON200.Status == nil || device.JSON200.Status.SystemInfo.IsEmpty() {
		return nil
	}
	return device
}

func (h *Harness) GetDeviceWithStatusSummary(enrollmentID string) v1alpha1.DeviceSummaryStatusType {
	device, err := h.Client.GetDeviceWithResponse(h.Context, enrollmentID)
	Expect(err).NotTo(HaveOccurred())
	// we keep waiting for a 200 response, with filled in Status.SystemInfo
	if device == nil || device.JSON200 == nil || device.JSON200.Status == nil || device.JSON200.Status.Summary.Status == "" {
		return ""
	}
	return device.JSON200.Status.Summary.Status
}

func (h *Harness) GetDeviceWithUpdateStatus(enrollmentID string) v1alpha1.DeviceUpdatedStatusType {
	device, err := h.Client.GetDeviceWithResponse(h.Context, enrollmentID)
	Expect(err).NotTo(HaveOccurred())
	// we keep waiting for a 200 response, with filled in Status.SystemInfo
	if device == nil || device.JSON200 == nil || device.JSON200.Status == nil {
		return ""
	}
	return device.JSON200.Status.Updated.Status
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
			logrus.Info("interactive process exited successfully")
		}
	}()
	return stdin, stdout, nil
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

func (h *Harness) UpdateDeviceWithRetries(deviceId string, updateFunction func(*v1alpha1.Device)) {
	Eventually(func(updFunction func(*v1alpha1.Device)) error {
		return h.UpdateDevice(deviceId, updFunction)
	}, TIMEOUT, "1s").WithArguments(updateFunction).Should(BeNil())
}

func (h *Harness) UpdateDevice(deviceId string, updateFunction func(*v1alpha1.Device)) error {
	response, err := h.Client.GetDeviceWithResponse(h.Context, deviceId)
	Expect(err).NotTo(HaveOccurred())
	if response.JSON200 == nil {
		logrus.Errorf("An error happened retrieving device: %+v", response)
		return fmt.Errorf("device %s not found: %v", deviceId, response.Status())
	}
	device := response.JSON200

	updateFunction(device)

	resp, err := h.Client.ReplaceDeviceWithResponse(h.Context, deviceId, *device)

	// if a conflict happens (the device updated status or object since we read it) we retry
	if resp.JSON409 != nil {
		logrus.Warningf("Conflict updating device %s: %+v", deviceId, resp.JSON409)
	}

	// response code 200 = updated, we are expecting to update... something else is unexpected
	if resp.StatusCode() != 200 {
		logrus.Errorf("Unexpected http status code received: %d", resp.StatusCode())
		logrus.Errorf("Unexpected http response: %s", string(resp.Body))
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode(), string(resp.Body))
	}

	if err != nil {
		logrus.Errorf("Unexpected error updating device %s: %v", deviceId, err)
		return err
	}

	return nil
}

func (h *Harness) WaitForDeviceContents(deviceId string, description string, condition func(*v1alpha1.Device) bool, timeout string) {
	lastResourcePrint := ""

	Eventually(func() error {
		logrus.Infof("Waiting for condition: %q to be met", description)
		response, err := h.Client.GetDeviceWithResponse(h.Context, deviceId)
		Expect(err).NotTo(HaveOccurred())
		if response.JSON200 == nil {
			logrus.Errorf("An error happened retrieving device: %+v", response)
			return errors.New("device not found???")
		}
		device := response.JSON200

		yamlData, err := yaml.Marshal(device)
		yamlString := string(yamlData)
		Expect(err).ToNot(HaveOccurred())
		if yamlString != lastResourcePrint {
			fmt.Println("")
			fmt.Println("======================= Device resource change ===================== ")
			fmt.Println(yamlString)
			fmt.Println("================================================================== ")
			lastResourcePrint = yamlString
		}

		if condition(device) {
			return nil
		}
		return errors.New("not updated")
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
	response := h.GetDeviceWithStatusSystem(deviceId)
	device := response.JSON200
	Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusOnline))
	Expect(*device.Status.Summary.Info).To(Equal(service.DeviceStatusInfoHealthy))
	return deviceId, device
}

func (h *Harness) WaitForBootstrapAndUpdateToVersion(deviceId string, version string) (*v1alpha1.Device, string) {
	// Check the device status right after bootstrap
	response := h.GetDeviceWithStatusSystem(deviceId)
	device := response.JSON200
	Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusType("Online")))

	var newImageReference string

	h.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
		currentImage := device.Status.Os.Image
		logrus.Infof("current image for %s is %s", deviceId, currentImage)
		repo, _ := h.parseImageReference(currentImage)
		newImageReference = repo + version
		device.Spec.Os = &v1alpha1.DeviceOsSpec{Image: newImageReference}
		logrus.Infof("updating %s to image %s", deviceId, device.Spec.Os.Image)
	})

	return device, newImageReference
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

func (h *Harness) GetCurrentDeviceGeneration(deviceId string) (deviceRenderedVersionInt int64, err error) {
	var deviceGeneration int64 = -1
	logrus.Infof("Waiting for the device to be UpToDate")
	h.WaitForDeviceContents(deviceId, "The device is UpToDate",
		func(device *v1alpha1.Device) bool {
			for _, condition := range device.Status.Conditions {
				if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
					device.Status.Updated.Status == v1alpha1.DeviceUpdatedStatusUpToDate {
					deviceGeneration = *device.Metadata.Generation

					return true
				}
			}
			return false
		}, TIMEOUT)

	if deviceGeneration <= 0 {
		return deviceGeneration, fmt.Errorf("invalid generation: %d", deviceGeneration)

	}
	logrus.Infof("The device current generation is %d", deviceGeneration)

	return deviceGeneration, nil
}

func (h *Harness) PrepareNextDeviceGeneration(deviceId string) (int64, error) {
	currentGeneration, err := h.GetCurrentDeviceGeneration(deviceId)
	if err != nil {
		return -1, err
	}
	return currentGeneration + 1, nil
}

func (h *Harness) GetCurrentDeviceRenderedVersion(deviceId string) (deviceRenderedVersionInt int, err error) {
	deviceRenderedVersion := "-1"

	logrus.Infof("Waiting for the device to be UpToDate")
	h.WaitForDeviceContents(deviceId, "The device is UpToDate",
		func(device *v1alpha1.Device) bool {
			for _, condition := range device.Status.Conditions {
				if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
					device.Status.Updated.Status == v1alpha1.DeviceUpdatedStatusUpToDate {
					deviceRenderedVersion = device.Status.Config.RenderedVersion

					return true
				}
			}
			return false
		}, TIMEOUT)

	deviceRenderedVersionInt, err = strconv.Atoi(deviceRenderedVersion)
	if err != nil {
		return -1, fmt.Errorf("failed to get current rendered version: %w", err)
	}
	if deviceRenderedVersionInt <= 0 {
		return deviceRenderedVersionInt, fmt.Errorf("invalid version: %d", deviceRenderedVersionInt)

	}
	logrus.Infof("The device current renderedVersion is %d", deviceRenderedVersionInt)

	return deviceRenderedVersionInt, nil
}

func (h *Harness) PrepareNextDeviceVersion(deviceId string) (int, error) {
	currentVersion, err := h.GetCurrentDeviceRenderedVersion(deviceId)
	if err != nil {
		return -1, err
	}
	return currentVersion + 1, nil
}

func (h *Harness) WaitForDeviceNewRenderedVersion(deviceId string, newRenderedVersionInt int) (err error) {
	// Check that the device was already approved
	Eventually(h.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(
		deviceId).ShouldNot(BeEmpty())
	logrus.Infof("The device %s was approved", deviceId)

	// Wait for the device to pickup the new config and report measurements on device status.
	logrus.Infof("Waiting for the device to pick the config")
	UpdateRenderedVersionSuccessMessage := fmt.Sprintf("%s %d", util.UpdateRenderedVersionSuccess.String(), newRenderedVersionInt)
	h.WaitForDeviceContents(deviceId, UpdateRenderedVersionSuccessMessage,
		func(device *v1alpha1.Device) bool {
			for _, condition := range device.Status.Conditions {
				if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
					device.Status.Updated.Status == v1alpha1.DeviceUpdatedStatusUpToDate &&
					device.Status.Config.RenderedVersion == strconv.Itoa(newRenderedVersionInt) {
					return true
				}
			}
			return false
		}, LONGTIMEOUT)

	return nil
}

func (h *Harness) WaitForDeviceNewGeneration(deviceId string, newGeneration int64) (err error) {
	// Check that the device was already approved
	Eventually(h.GetDeviceWithStatusSummary, TIMEOUT, POLLING).WithArguments(
		deviceId).ShouldNot(BeEmpty())
	logrus.Infof("The device %s was approved", deviceId)

	// Wait for the device to pickup the new config and report measurements on device status.
	logrus.Infof("Waiting for the device to pick the config")
	h.WaitForDeviceContents(deviceId, "Waiting fot the device generation",
		func(device *v1alpha1.Device) bool {
			for _, condition := range device.Status.Conditions {
				if condition.Type == "Updating" && condition.Reason == "Updated" && condition.Status == "False" &&
					device.Status.Updated.Status == v1alpha1.DeviceUpdatedStatusUpToDate &&
					newGeneration == *device.Metadata.Generation {
					return true
				}
			}
			return false
		}, LONGTIMEOUT)

	return nil
}

func (h *Harness) CleanUpResources(resourceType string) (string, error) {
	logrus.Infof("Deleting the instances of the %s resource type", resourceType)
	return h.CLI("delete", resourceType)

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
func (h *Harness) GetEnrollmentRequestByYaml(erYaml string) v1alpha1.EnrollmentRequest {
	return getYamlResourceByFile[v1alpha1.EnrollmentRequest](erYaml)
}

// Wrapper function for CertificateSigningRequest
func (h *Harness) GetCertificateSigningRequestByYaml(csrYaml string) v1alpha1.CertificateSigningRequest {
	return getYamlResourceByFile[v1alpha1.CertificateSigningRequest](csrYaml)
}

// getDeviceConfig is a generic helper function to retrieve device configurations
func GetDeviceConfig[T any](device *v1alpha1.Device, configType v1alpha1.ConfigProviderType,
	asConfig func(v1alpha1.ConfigProviderSpec) (T, error)) (T, error) {

	var config T
	if device.Spec == nil || device.Spec.Config == nil {
		return config, fmt.Errorf("device spec or config is nil")
	}

	if len(*device.Spec.Config) > 0 {
		for _, configItem := range *device.Spec.Config {
			// Check config type
			itemType, err := configItem.Type()
			if err != nil {
				return config, fmt.Errorf("failed to get config type: %w", err)
			}
			if itemType == configType {
				// Convert to the expected config type
				config, err := asConfig(configItem)
				if err != nil {
					return config, fmt.Errorf("failed to convert config: %w", err)
				}

				return config, nil
			}
		}
	}

	// If we don't find the config, return an error
	return config, fmt.Errorf("%s config not found in the device", configType)
}

// Get InlineConfig
func (h *Harness) GetDeviceInlineConfig(device *v1alpha1.Device, configName string) (v1alpha1.InlineConfigProviderSpec, error) {
	return GetDeviceConfig(device, v1alpha1.InlineConfigProviderType,
		func(c v1alpha1.ConfigProviderSpec) (v1alpha1.InlineConfigProviderSpec, error) {
			inlineConfig, err := c.AsInlineConfigProviderSpec()
			if err != nil {
				return inlineConfig, fmt.Errorf("failed to cast config type: %w", err)
			}
			if inlineConfig.Name == configName {
				logrus.Infof("Inline configuration found %s", configName)
				return inlineConfig, nil
			}
			return v1alpha1.InlineConfigProviderSpec{}, fmt.Errorf("inline config not found")
		})
}

// Get GitConfig
func (h *Harness) GetDeviceGitConfig(device *v1alpha1.Device, configName string) (v1alpha1.GitConfigProviderSpec, error) {
	return GetDeviceConfig(device, v1alpha1.GitConfigProviderType,
		func(c v1alpha1.ConfigProviderSpec) (v1alpha1.GitConfigProviderSpec, error) {
			gitConfig, err := c.AsGitConfigProviderSpec()
			if err != nil {
				return gitConfig, fmt.Errorf("failed to cast config type: %w", err)
			}
			if gitConfig.Name == configName {
				logrus.Infof("Git configuration found %s", configName)
				return gitConfig, nil
			}
			return v1alpha1.GitConfigProviderSpec{}, fmt.Errorf("git config not found")
		})
}

// Get HttpConfig
func (h *Harness) GetDeviceHttpConfig(device *v1alpha1.Device, configName string) (v1alpha1.HttpConfigProviderSpec, error) {
	return GetDeviceConfig(device, v1alpha1.HttpConfigProviderType,
		func(c v1alpha1.ConfigProviderSpec) (v1alpha1.HttpConfigProviderSpec, error) {
			httpConfig, err := c.AsHttpConfigProviderSpec()
			if err != nil {
				return httpConfig, fmt.Errorf("failed to cast config type: %w", err)
			}
			if httpConfig.Name == configName {
				logrus.Infof("Http configuration found %s", configName)
				return httpConfig, nil
			}
			return v1alpha1.HttpConfigProviderSpec{}, fmt.Errorf("http config not found")
		})
}

// Get an http config of a device resource
func (h *Harness) GetDeviceOsImage(device *v1alpha1.Device) (image string, err error) {
	if device.Spec == nil {
		return "", fmt.Errorf("device spec is nil")
	}
	if device.Spec.Os == nil {
		return "", fmt.Errorf("device os spec is nil")
	}

	return device.Spec.Os.Image, nil
}

// Create a test fleet resource
func (h *Harness) CreateOrUpdateTestFleet(testFleetName string, testFleetSelector v1alpha1.LabelSelector, testFleetSpec v1alpha1.DeviceSpec) error {
	var testFleet = v1alpha1.Fleet{
		ApiVersion: v1alpha1.FleetAPIVersion,
		Kind:       v1alpha1.FleetKind,
		Metadata: v1alpha1.ObjectMeta{
			Name:   &testFleetName,
			Labels: &map[string]string{},
		},
		Spec: v1alpha1.FleetSpec{
			Selector: &testFleetSelector,
			Template: struct {
				Metadata *v1alpha1.ObjectMeta "json:\"metadata,omitempty\""
				Spec     v1alpha1.DeviceSpec  "json:\"spec\""
			}{
				Spec: testFleetSpec,
			},
		},
	}
	_, err := h.Client.ReplaceFleetWithResponse(h.Context, testFleetName, testFleet)
	return err
}

// Create a test fleet with a configuration
func (h *Harness) CreateTestFleetWithConfig(testFleetName string, testFleetSelector v1alpha1.LabelSelector, configProviderSpec v1alpha1.ConfigProviderSpec) error {
	var testFleetSpec = v1alpha1.DeviceSpec{
		Config: &[]v1alpha1.ConfigProviderSpec{
			configProviderSpec,
		},
	}
	err := h.CreateOrUpdateTestFleet(testFleetName, testFleetSelector, testFleetSpec)
	return err
}

// Create a repository resource
func (h Harness) CreateRepository(repositorySpec v1alpha1.RepositorySpec, metadata v1alpha1.ObjectMeta) error {
	var repository = v1alpha1.Repository{
		ApiVersion: v1alpha1.RepositoryAPIVersion,
		Kind:       v1alpha1.RepositoryKind,

		Metadata: metadata,
		Spec:     repositorySpec,
	}
	_, err := h.Client.CreateRepositoryWithResponse(h.Context, repository)
	return err
}

// Check that the device summary status is equal to the status input
func (h Harness) CheckDeviceStatus(deviceId string, status v1alpha1.DeviceSummaryStatusType) (*v1alpha1.Device, error) {
	response := h.GetDeviceWithStatusSystem(deviceId)
	if response == nil {
		return nil, fmt.Errorf("device response is nil")
	}
	if response.JSON200 == nil {
		return nil, fmt.Errorf("device.JSON200 response is nil")
	}
	device := response.JSON200
	deviceStaus := device.Status.Summary.Status
	if deviceStaus != status {
		return nil, fmt.Errorf("the device status is notOnline but %s", deviceStaus)
	}
	return device, nil
}

// Get device with response
func (h Harness) GetDevice(deviceId string) (*v1alpha1.Device, error) {
	response, err := h.Client.GetDeviceWithResponse(h.Context, deviceId)
	if err != nil {
		return nil, fmt.Errorf("failed to get device with response: %s", err)
	}
	if response == nil {
		return nil, fmt.Errorf("device response is nil")
	}
	device := response.JSON200
	return device, nil
}

// CheckRunningContainers verifies the expected number of running containers on the VM.
func (h Harness) CheckRunningContainers() (string, error) {
	out, err := h.VM.RunSSH([]string{"sudo", "podman", "ps", "|", "grep", "Up", "|", "wc", "-l"}, nil)
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// RunGetDevices executes "get devices" CLI command with optional arguments.
func (h Harness) RunGetDevices(args ...string) (string, error) {
	allArgs := append([]string{"get", "devices"}, args...)
	return h.CLI(allArgs...)
}

// RunGetEvents executes "get events" CLI command with optional arguments.
func (h Harness) RunGetEvents(args ...string) (string, error) {
	// Starting with the base command to get events
	allArgs := append([]string{"get", "events"}, args...)
	return h.CLI(allArgs...)
}

// ManageResource performs an operation ("apply" or "delete") on a specified resource.
func (h Harness) ManageResource(operation, resource string, args ...string) (string, error) {
	switch operation {
	case "apply":
		return h.CLI("apply", "-f", util.GetTestExamplesYamlPath(resource))
	case "delete":
		return h.CLI("delete", resource)
	default:
		return "", fmt.Errorf("unsupported operation: %s", operation)
	}
}

// ConditionExists checks if a specific condition exists for the device with the given type, status, and reason.
func ConditionExists(d *v1alpha1.Device, conditionType, conditionStatus, conditionReason string) bool {
	for _, condition := range d.Status.Conditions {
		if string(condition.Type) == conditionType &&
			condition.Reason == conditionReason &&
			string(condition.Status) == conditionStatus {
			return true
		}
	}
	return false
}
