//go:build linux

package packagemode_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	packageModeQCOWEnvVar         = "E2E_PACKAGE_MODE_QCOW"
	imageModeQCOWEnvVar           = "E2E_IMAGE_MODE_QCOW"
	defaultImageModeQCOWPath      = "bin/output/qcow2/disk.qcow2"
	packageModeVMUser             = "user"
	packageModeVMPassword         = "user"
	packageModeContainerRunAsUser = "flightctl"
	packageModeDeviceSSHPortBase  = 23100
	packageModeMixedSSHPortOffset = 50
	imageModeDeviceSSHPortBase    = 23200
	packageModeBootTimeout        = 6 * time.Minute
	packageModeCommandTimeout     = 60 * time.Second
	packageModeEnrollTimeout      = 5 * time.Minute
	packageModePollInterval       = 5 * time.Second
	packageModeConfigPath         = "/etc/flightctl/package-mode-e2e.conf"
	packageModeConfigContent      = "package-mode-e2e=enabled"
	packageModeContainerAppName   = "package-mode-sleep"
	packageModeLogObservationTime = 30 * time.Second
	packageModeFleetLabelKey      = "package-mode-suite"
	packageModeNormalFleetPrefix  = "package-mode-normal"
	packageModeMixedFleetPrefix   = "package-mode-mixed"
	packageModeVMNamePrefix       = "package-mode"
	imageModeVMNamePrefix         = "image-mode"
	packageModeExpectedRejectText = "package-mode device cannot satisfy spec with os.image"
)

var _ = Describe("Package-mode device scenarios", Ordered, func() {
	var harness *e2e.Harness

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()
	})

	Context("normal package-mode operation", Ordered, func() {
		var (
			deviceID      string
			fleetName     string
			selector      v1beta1.LabelSelector
			packageModeVM *vmUnderTest
		)

		BeforeAll(func() {
			var err error
			harness = e2e.GetWorkerHarness()
			restoreContext := setHarnessSetupContext(harness, packageModeNormalFleetPrefix)
			defer harness.SetTestContext(restoreContext)

			packageQCOW := requireQCOWPath(packageModeQCOWEnvVar, "")
			fleetName = uniqueFleetName(packageModeNormalFleetPrefix)
			selector = v1beta1.LabelSelector{
				MatchLabels: &map[string]string{
					packageModeFleetLabelKey: fleetName,
				},
			}

			packageModeVM, err = startCustomVMFromQCOW(packageQCOW, packageModeVMNamePrefix, packageModeDeviceSSHPortBase+GinkgoParallelProcess())
			Expect(err).ToNot(HaveOccurred())

			inlineConfig, err := e2e.NewInlineConfigSpec("package-mode-inline", []v1beta1.FileSpec{{
				Path:    packageModeConfigPath,
				Content: packageModeConfigContent,
			}})
			Expect(err).ToNot(HaveOccurred())

			appImage := harness.GetSleepAppImageRefForFleet(packageModeAuxSvcs.Registry.Host, packageModeAuxSvcs.Registry.Port, testutil.SleepAppTags.V2)
			containerApp, err := e2e.NewContainerApplicationSpecWithRunAs(
				packageModeContainerAppName,
				appImage,
				nil,
				nil,
				nil,
				nil,
				packageModeContainerRunAsUser,
			)
			Expect(err).ToNot(HaveOccurred())

			deviceSpec := v1beta1.DeviceSpec{
				Config:       &[]v1beta1.ConfigProviderSpec{inlineConfig},
				Applications: &[]v1beta1.ApplicationProviderSpec{containerApp},
			}

			err = harness.CreateOrUpdateTestFleet(fleetName, selector, deviceSpec)
			Expect(err).ToNot(HaveOccurred())

			deviceID, err = enrollCustomVM(harness, packageModeVM, map[string]string{
				packageModeFleetLabelKey: fleetName,
			})
			Expect(err).ToNot(HaveOccurred())
		})

		AfterAll(func() {
			if strings.TrimSpace(deviceID) != "" {
				if err := harness.DeleteDeviceIgnoreNotFound(deviceID); err != nil {
					GinkgoWriter.Printf("DeleteDeviceIgnoreNotFound(%s): %v\n", deviceID, err)
				}
			}
			if strings.TrimSpace(fleetName) != "" {
				if err := harness.DeleteFleetIgnoreNotFound(fleetName); err != nil {
					GinkgoWriter.Printf("DeleteFleetIgnoreNotFound(%s): %v\n", fleetName, err)
				}
			}
			cleanupSuiteLibvirtVM(packageModeVM)
		})

		It("enrolls a package-mode device and reports capabilities.osMode=package", Label("90146", "sanity"), func() {
			Eventually(func(g Gomega) {
				device, err := harness.GetDevice(deviceID)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(device).ToNot(BeNil())
				g.Expect(device.Status).ToNot(BeNil())
				g.Expect(device.Status.Capabilities).ToNot(BeNil())
				g.Expect(device.Status.Capabilities.OsMode).ToNot(BeNil())
				g.Expect(*device.Status.Capabilities.OsMode).To(Equal(v1beta1.OsModePackage))
			}, packageModeEnrollTimeout, packageModePollInterval).Should(Succeed())
		})

		It("deploys config and a podman application on a package-mode fleet without spec.os.image", Label("90147", "sanity"), func() {
			Eventually(func(g Gomega) {
				device, err := harness.GetDevice(deviceID)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(device).ToNot(BeNil())
				g.Expect(device.Spec).ToNot(BeNil())
				g.Expect(device.Spec.Os).To(BeNil())
			}, packageModeEnrollTimeout, packageModePollInterval).Should(Succeed())

			Expect(harness.WaitForApplicationStatus(deviceID, packageModeContainerAppName, v1beta1.ApplicationStatusRunning, testutil.TIMEOUT, testutil.POLLING)).ToNot(HaveOccurred())
			Expect(harness.WaitForApplicationSummary(deviceID, testutil.TIMEOUT, testutil.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)).ToNot(HaveOccurred())

			Eventually(func(g Gomega) {
				output, err := runSuiteLibvirtVMCommand(harness.GetTestContext(), packageModeVM.Instance, []string{"cat", packageModeConfigPath})
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(output).To(ContainSubstring(packageModeConfigContent))
			}, packageModeEnrollTimeout, packageModePollInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				device, err := harness.GetDevice(deviceID)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(device).ToNot(BeNil())
				g.Expect(device.Status).ToNot(BeNil())
				g.Expect(device.Status.Applications).ToNot(BeNil())
				application := findDeviceApplicationStatusByName(device.Status.Applications, packageModeContainerAppName)
				g.Expect(application).ToNot(BeNil())
				g.Expect(application.RunAs).To(Equal(v1beta1.Username(packageModeContainerRunAsUser)))
			}, packageModeEnrollTimeout, packageModePollInterval).Should(Succeed())
		})

		It("does not emit the mixed-fleet os.image reject message during normal package-mode operation", Label("90150", "sanity"), func() {
			observationStart, err := currentSuiteLibvirtVMJournalSince(harness.GetTestContext(), packageModeVM.Instance)
			Expect(err).ToNot(HaveOccurred())

			Consistently(func(g Gomega) {
				logs, err := readSuiteLibvirtVMServiceLogsSince(harness.GetTestContext(), packageModeVM.Instance, testutil.FLIGHTCTL_AGENT_SERVICE, observationStart)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(logs).ToNot(ContainSubstring(packageModeExpectedRejectText))
			}, packageModeLogObservationTime, packageModePollInterval).Should(Succeed())
		})
	})

	Context("mixed fleet os image behavior", Ordered, func() {
		var (
			packageDeviceID         string
			imageDeviceID           string
			fleetName               string
			selector                v1beta1.LabelSelector
			packageModeVM           *vmUnderTest
			imageModeVM             *vmUnderTest
			packageInitialVersion   int
			imageExpectedVersion    int
			requestedImageReference string
		)

		BeforeAll(func() {
			var err error
			harness = e2e.GetWorkerHarness()
			restoreContext := setHarnessSetupContext(harness, packageModeMixedFleetPrefix)
			defer harness.SetTestContext(restoreContext)

			packageQCOW := requireQCOWPath(packageModeQCOWEnvVar, "")
			imageQCOW := requireQCOWPath(imageModeQCOWEnvVar, defaultImageModeQCOWPath)
			if samePath(packageQCOW, imageQCOW) {
				failOrSkipForMissingQCOW(fmt.Sprintf("%s must point to an image-mode qcow different from %s", imageModeQCOWEnvVar, packageModeQCOWEnvVar))
			}

			fleetName = uniqueFleetName(packageModeMixedFleetPrefix)
			selector = v1beta1.LabelSelector{
				MatchLabels: &map[string]string{
					packageModeFleetLabelKey: fleetName,
				},
			}

			packageModeVM, err = startCustomVMFromQCOW(packageQCOW, packageModeVMNamePrefix, packageModeDeviceSSHPortBase+packageModeMixedSSHPortOffset+GinkgoParallelProcess())
			Expect(err).ToNot(HaveOccurred())

			imageModeVM, err = startCustomVMFromQCOW(imageQCOW, imageModeVMNamePrefix, imageModeDeviceSSHPortBase+GinkgoParallelProcess())
			Expect(err).ToNot(HaveOccurred())

			err = harness.CreateOrUpdateTestFleet(fleetName, selector, v1beta1.DeviceSpec{})
			Expect(err).ToNot(HaveOccurred())

			labels := map[string]string{packageModeFleetLabelKey: fleetName}

			packageDeviceID, err = enrollCustomVM(harness, packageModeVM, labels)
			Expect(err).ToNot(HaveOccurred())

			imageDeviceID, err = enrollCustomVM(harness, imageModeVM, labels)
			Expect(err).ToNot(HaveOccurred())

			Expect(harness.WaitForDeviceNewRenderedVersion(packageDeviceID, 1)).ToNot(HaveOccurred())
			Expect(harness.WaitForDeviceNewRenderedVersion(imageDeviceID, 1)).ToNot(HaveOccurred())

			packageInitialVersion, err = harness.GetCurrentDeviceRenderedVersion(packageDeviceID)
			Expect(err).ToNot(HaveOccurred())

			imageExpectedVersion, err = harness.PrepareNextDeviceVersion(imageDeviceID)
			Expect(err).ToNot(HaveOccurred())

			registryHost := packageModeAuxSvcs.Registry.Host
			registryPort := packageModeAuxSvcs.Registry.Port
			deviceSpec, err := harness.CreateFleetDeviceSpec(registryHost, registryPort, testutil.DeviceTags.V2)
			Expect(err).ToNot(HaveOccurred())
			requestedImageReference = harness.GetDeviceImageRefForFleet(registryHost, registryPort, testutil.DeviceTags.V2)

			err = harness.CreateOrUpdateTestFleet(fleetName, selector, deviceSpec)
			Expect(err).ToNot(HaveOccurred())
		})

		AfterAll(func() {
			if strings.TrimSpace(packageDeviceID) != "" {
				if err := harness.DeleteDeviceIgnoreNotFound(packageDeviceID); err != nil {
					GinkgoWriter.Printf("DeleteDeviceIgnoreNotFound(%s): %v\n", packageDeviceID, err)
				}
			}
			if strings.TrimSpace(imageDeviceID) != "" {
				if err := harness.DeleteDeviceIgnoreNotFound(imageDeviceID); err != nil {
					GinkgoWriter.Printf("DeleteDeviceIgnoreNotFound(%s): %v\n", imageDeviceID, err)
				}
			}
			if strings.TrimSpace(fleetName) != "" {
				if err := harness.DeleteFleetIgnoreNotFound(fleetName); err != nil {
					GinkgoWriter.Printf("DeleteFleetIgnoreNotFound(%s): %v\n", fleetName, err)
				}
			}
			cleanupSuiteLibvirtVM(packageModeVM)
			cleanupSuiteLibvirtVM(imageModeVM)
		})

		It("keeps the package-mode device OutOfDate when the fleet adds spec.os.image", Label("90148", "sanity"), func() {
			rejectObservationStart, err := currentSuiteLibvirtVMJournalSince(harness.GetTestContext(), packageModeVM.Instance)
			Expect(err).ToNot(HaveOccurred())

			Eventually(func(g Gomega) {
				device, err := harness.GetDevice(packageDeviceID)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(device).ToNot(BeNil())
				g.Expect(device.Status).ToNot(BeNil())
				g.Expect(device.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusOutOfDate))
				g.Expect(device.Status.Config.RenderedVersion).To(Equal(strconv.Itoa(packageInitialVersion)))
				g.Expect(deviceRejectsMixedFleetUpdate(device, packageModeExpectedRejectText)).To(BeTrue())
			}, testutil.LONG_TIMEOUT, packageModePollInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				logs, err := readSuiteLibvirtVMServiceLogsSince(harness.GetTestContext(), packageModeVM.Instance, testutil.FLIGHTCTL_AGENT_SERVICE, rejectObservationStart)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(logs).To(ContainSubstring(packageModeExpectedRejectText))
			}, testutil.LONG_TIMEOUT, packageModePollInterval).Should(Succeed())
		})

		It("lets the image-mode device converge on the requested os image in the same mixed fleet", Label("90149", "sanity", "slow"), func() {
			Expect(harness.WaitForDeviceNewRenderedVersion(imageDeviceID, imageExpectedVersion)).ToNot(HaveOccurred())

			Eventually(func(g Gomega) {
				device, err := harness.GetDevice(imageDeviceID)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(device).ToNot(BeNil())
				g.Expect(device.Status).ToNot(BeNil())
				g.Expect(device.Status.Capabilities).ToNot(BeNil())
				g.Expect(device.Status.Capabilities.OsMode).ToNot(BeNil())
				g.Expect(*device.Status.Capabilities.OsMode).To(Equal(v1beta1.OsModeImage))
				g.Expect(device.Status.Updated.Status).To(Equal(v1beta1.DeviceUpdatedStatusUpToDate))
				g.Expect(device.Status.Os.Image).To(Equal(requestedImageReference))
				g.Expect(device.Status.Config.RenderedVersion).To(Equal(strconv.Itoa(imageExpectedVersion)))
			}, testutil.LONG_TIMEOUT, packageModePollInterval).Should(Succeed())
		})
	})
})

var (
	suiteLibvirtVMMu sync.Mutex
	suiteLibvirtVMs  = map[string]*vmUnderTest{}
)

// vmUnderTest tracks a libvirt VM instance created for package-mode E2E coverage.
type vmUnderTest struct {
	Instance vm.TestVMInterface
	Name     string
	SSHPort  int
	TestDir  string
}

// requireQCOWPath resolves a qcow input or skips the suite context when the file is unavailable.
func requireQCOWPath(envVar, defaultRelativePath string) string {
	candidate := strings.TrimSpace(os.Getenv(envVar))
	if candidate == "" && defaultRelativePath != "" {
		candidate = filepath.Join(testutil.GetTopLevelDir(), defaultRelativePath)
	}
	if candidate == "" {
		failOrSkipForMissingQCOW(fmt.Sprintf("set %s to a qcow2 path before running this suite", envVar))
	}

	absolutePath, err := filepath.Abs(candidate)
	if err != nil {
		failOrSkipForMissingQCOW(fmt.Sprintf("resolve %s: %v", envVar, err))
	}
	if _, err := os.Stat(absolutePath); err != nil {
		failOrSkipForMissingQCOW(fmt.Sprintf("%s path %s is not readable: %v", envVar, absolutePath, err))
	}
	return absolutePath
}

// startCustomVMFromQCOW creates and boots a custom libvirt VM from a qcow image.
func startCustomVMFromQCOW(qcowPath, namePrefix string, sshPort int) (*vmUnderTest, error) {
	vmName := fmt.Sprintf("%s-%d", namePrefix, time.Now().UnixNano())
	testDir := GinkgoT().TempDir()
	vmDiskPath := filepath.Join(testDir, filepath.Base(qcowPath))
	if err := createQCOWOverlay(qcowPath, vmDiskPath); err != nil {
		return nil, fmt.Errorf("create qcow overlay for %s: %w", qcowPath, err)
	}
	testVM := vm.TestVM{
		TestDir:        testDir,
		VMName:         vmName,
		DiskImagePath:  vmDiskPath,
		VMUser:         packageModeVMUser,
		SSHPassword:    packageModeVMPassword,
		SSHPort:        sshPort,
		SSHWaitTimeout: packageModeBootTimeout,
	}

	libvirtVM, err := vm.NewVM(testVM)
	if err != nil {
		return nil, fmt.Errorf("create VM %s: %w", vmName, err)
	}
	if err := libvirtVM.CreateDomain(); err != nil {
		if cleanupErr := libvirtVM.ForceDelete(); cleanupErr != nil {
			GinkgoWriter.Printf("startCustomVMFromQCOW ForceDelete after CreateDomain failure: %v\n", cleanupErr)
		}
		return nil, fmt.Errorf("create domain %s: %w", vmName, err)
	}
	if err := libvirtVM.RunAndWaitForSSH(); err != nil {
		if cleanupErr := libvirtVM.ForceDelete(); cleanupErr != nil {
			GinkgoWriter.Printf("startCustomVMFromQCOW ForceDelete after RunAndWaitForSSH failure: %v\n", cleanupErr)
		}
		return nil, fmt.Errorf("boot VM %s: %w", vmName, err)
	}

	candidate := &vmUnderTest{
		Instance: libvirtVM,
		Name:     vmName,
		SSHPort:  sshPort,
		TestDir:  testDir,
	}
	if err := registerSuiteLibvirtVM(candidate); err != nil {
		if cleanupErr := libvirtVM.ForceDelete(); cleanupErr != nil {
			GinkgoWriter.Printf("startCustomVMFromQCOW ForceDelete after registration failure: %v\n", cleanupErr)
		}
		return nil, err
	}
	return candidate, nil
}

// enrollCustomVM approves the enrollment request for a custom VM and waits for Online status.
func enrollCustomVM(harness *e2e.Harness, candidate *vmUnderTest, labels map[string]string) (string, error) {
	GinkgoHelper()

	if harness == nil {
		return "", fmt.Errorf("harness is nil")
	}
	if candidate == nil || candidate.Instance == nil {
		return "", fmt.Errorf("VM is nil")
	}

	enrollmentID := waitForSuiteLibvirtVMEnrollmentID(harness.GetTestContext(), candidate.Instance, packageModeEnrollTimeout, packageModePollInterval)

	if _, err := harness.WaitForEnrollmentRequestResource(enrollmentID, testutil.TIMEOUT_5M, testutil.SHORT_POLLING); err != nil {
		return "", err
	}

	approvalLabels, err := mergeEnrollmentLabels(harness, labels)
	if err != nil {
		return "", err
	}
	if _, err := harness.ApproveEnrollmentRequestWithLabels(enrollmentID, approvalLabels); err != nil {
		return "", err
	}

	Eventually(func() error {
		device, err := harness.CheckDeviceStatus(enrollmentID, v1beta1.DeviceSummaryStatusOnline)
		if err != nil {
			return err
		}
		if device == nil {
			return fmt.Errorf("device %s is still nil", enrollmentID)
		}
		return nil
	}, packageModeEnrollTimeout, packageModePollInterval).Should(Succeed())

	return enrollmentID, nil
}

// waitForSuiteLibvirtVMEnrollmentID polls a suite-created libvirt VM's agent logs until an enrollment request ID appears.
func waitForSuiteLibvirtVMEnrollmentID(ctx context.Context, libvirtVM vm.TestVMInterface, timeout, polling time.Duration) string {
	GinkgoHelper()

	since, err := currentSuiteLibvirtVMJournalSince(ctx, libvirtVM)
	Expect(err).ToNot(HaveOccurred())

	var enrollmentID string
	Eventually(func() string {
		logs, err := readSuiteLibvirtVMServiceLogsSince(ctx, libvirtVM, testutil.FLIGHTCTL_AGENT_SERVICE, since)
		if err != nil {
			GinkgoWriter.Printf("waitForSuiteLibvirtVMEnrollmentID ReadServiceLogsSince: %v\n", err)
			return ""
		}
		enrollmentID = testutil.GetEnrollmentIdFromText(logs)
		return enrollmentID
	}, timeout, polling).ShouldNot(BeEmpty())

	return enrollmentID
}

// mergeEnrollmentLabels adds the harness test-id label to enrollment approval labels.
func mergeEnrollmentLabels(harness *e2e.Harness, labels map[string]string) (map[string]string, error) {
	testLabels, err := harness.TestResourceLabels()
	if err != nil {
		return nil, err
	}

	merged := map[string]string{}
	for key, value := range testLabels {
		merged[key] = value
	}
	for key, value := range labels {
		merged[key] = value
	}
	return merged, nil
}

// uniqueFleetName returns a predictable, unique fleet name for the current spec run.
func uniqueFleetName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// setHarnessSetupContext seeds a stable test-id context for Ordered BeforeAll setup work.
func setHarnessSetupContext(harness *e2e.Harness, prefix string) context.Context {
	restoreContext := harness.GetTestContext()
	setupContext := context.WithValue(restoreContext, testutil.TestIDKey, uniqueFleetName(prefix+"-setup"))
	harness.SetTestContext(setupContext)
	return restoreContext
}

// cleanupSuiteLibvirtVM force-deletes a suite-created libvirt VM.
func cleanupSuiteLibvirtVM(candidate *vmUnderTest) {
	if candidate == nil {
		return
	}
	defer unregisterSuiteLibvirtVM(candidate)
	if candidate.Instance != nil {
		if err := candidate.Instance.ForceDelete(); err != nil {
			GinkgoWriter.Printf("cleanupSuiteLibvirtVM(%s): %v\n", candidate.Name, err)
		}
	}
}

// runSuiteLibvirtVMCommand executes a command on a suite-created libvirt VM and returns trimmed stdout.
func runSuiteLibvirtVMCommand(ctx context.Context, libvirtVM vm.TestVMInterface, args []string) (string, error) {
	if libvirtVM == nil {
		return "", fmt.Errorf("VM is nil")
	}
	if len(args) == 0 {
		return "", fmt.Errorf("command args are empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	commandCtx, cancel := context.WithTimeout(ctx, packageModeCommandTimeout)
	defer cancel()

	stdout, err := libvirtVM.RunSSHContext(commandCtx, args, nil)
	if err != nil {
		return "", err
	}
	if stdout == nil {
		return "", fmt.Errorf("command output is nil")
	}
	return strings.TrimSpace(stdout.String()), nil
}

// registerSuiteLibvirtVM tracks a suite-created libvirt VM for failure log capture.
func registerSuiteLibvirtVM(candidate *vmUnderTest) error {
	if candidate == nil || candidate.Instance == nil || strings.TrimSpace(candidate.Name) == "" {
		return fmt.Errorf("suite libvirt VM is incomplete")
	}

	suiteLibvirtVMMu.Lock()
	defer suiteLibvirtVMMu.Unlock()
	suiteLibvirtVMs[candidate.Name] = candidate
	return nil
}

// unregisterSuiteLibvirtVM removes a suite-created libvirt VM from failure log capture tracking.
func unregisterSuiteLibvirtVM(candidate *vmUnderTest) {
	if candidate == nil || strings.TrimSpace(candidate.Name) == "" {
		return
	}

	suiteLibvirtVMMu.Lock()
	defer suiteLibvirtVMMu.Unlock()
	delete(suiteLibvirtVMs, candidate.Name)
}

// listSuiteLibvirtVMs returns the currently tracked suite-created libvirt VMs.
func listSuiteLibvirtVMs() []*vmUnderTest {
	suiteLibvirtVMMu.Lock()
	defer suiteLibvirtVMMu.Unlock()

	vms := make([]*vmUnderTest, 0, len(suiteLibvirtVMs))
	for _, candidate := range suiteLibvirtVMs {
		vms = append(vms, candidate)
	}
	return vms
}

// printPackageModeCustomVMLogsIfFailed prints flightctl-agent logs for suite-created libvirt VMs after a failed spec.
func printPackageModeCustomVMLogsIfFailed(ctx context.Context) {
	if !CurrentSpecReport().Failed() {
		return
	}

	for _, candidate := range listSuiteLibvirtVMs() {
		if candidate == nil || candidate.Instance == nil {
			continue
		}

		running, err := candidate.Instance.IsRunning()
		if err != nil || !running {
			continue
		}

		logs, err := readSuiteLibvirtVMServiceLogsSince(ctx, candidate.Instance, testutil.FLIGHTCTL_AGENT_SERVICE, "")
		if err != nil {
			GinkgoWriter.Printf("printPackageModeCustomVMLogsIfFailed(%s): %v\n", candidate.Name, err)
			continue
		}

		GinkgoWriter.Printf("flightctl-agent logs for %s:\n%s\n", candidate.Name, logs)
	}
}

// deviceRejectsMixedFleetUpdate returns true when package-mode rejection is visible in device status.
func deviceRejectsMixedFleetUpdate(device *v1beta1.Device, rejectText string) bool {
	if device == nil || device.Status == nil {
		return false
	}

	if e2e.ConditionExists(device, v1beta1.ConditionTypeDeviceUpdating, v1beta1.ConditionStatusFalse, string(v1beta1.UpdateStateError)) {
		cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
		if cond != nil && strings.Contains(cond.Message, rejectText) {
			return true
		}
	}

	return device.Status.Updated.Info != nil && strings.Contains(*device.Status.Updated.Info, rejectText)
}

// samePath compares two paths after cleaning them.
func samePath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// failOrSkipForMissingQCOW fails in CI and skips in local runs when a required qcow is unavailable.
func failOrSkipForMissingQCOW(message string) {
	GinkgoHelper()

	if strings.TrimSpace(os.Getenv("CI")) != "" {
		Fail(message)
	}
	Skip(message)
}

// createQCOWOverlay creates a qcow2 overlay so each suite VM gets isolated writes without copying the base disk.
func createQCOWOverlay(baseQCOWPath, overlayQCOWPath string) error {
	backingFormat, err := detectQCOWFormat(baseQCOWPath)
	if err != nil {
		return fmt.Errorf("detect backing qcow format for %s: %w", baseQCOWPath, err)
	}

	cmd := exec.Command("qemu-img", "create", "-f", "qcow2", "-b", baseQCOWPath, "-F", backingFormat, overlayQCOWPath) //nolint:gosec
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("qemu-img create failed: %w, output: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// currentSuiteLibvirtVMJournalSince returns a guest-aligned RFC3339 timestamp for journalctl --since filtering.
func currentSuiteLibvirtVMJournalSince(ctx context.Context, libvirtVM vm.TestVMInterface) (string, error) {
	if libvirtVM == nil {
		return "", fmt.Errorf("VM is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	commandCtx, cancel := context.WithTimeout(ctx, packageModeCommandTimeout)
	defer cancel()

	out, err := libvirtVM.RunSSHContext(commandCtx, []string{"date", "-u", `+%Y-%m-%dT%H:%M:%SZ`}, nil)
	if err != nil {
		return "", fmt.Errorf("read VM clock for journal since: %w", err)
	}
	timestamp := strings.TrimSpace(out.String())
	parsed, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return "", fmt.Errorf("parse VM date %q: %w", timestamp, err)
	}
	return parsed.Add(-2 * time.Second).UTC().Format(time.RFC3339), nil
}

// readSuiteLibvirtVMServiceLogsSince returns service journal output produced at or after the provided guest-aligned timestamp.
func readSuiteLibvirtVMServiceLogsSince(ctx context.Context, libvirtVM vm.TestVMInterface, serviceName, since string) (string, error) {
	if libvirtVM == nil {
		return "", fmt.Errorf("VM is nil")
	}
	return runSuiteLibvirtVMCommand(ctx, libvirtVM, suiteLibvirtVMJournalSinceArgs(serviceName, since, 500))
}

// detectQCOWFormat returns the qemu-img reported format for the backing image.
func detectQCOWFormat(qcowPath string) (string, error) {
	cmd := exec.Command("qemu-img", "info", "--output=json", qcowPath) //nolint:gosec
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("qemu-img info failed: %w, output: %s", err, strings.TrimSpace(string(output)))
	}

	var metadata struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(output, &metadata); err != nil {
		return "", fmt.Errorf("parse qemu-img info output: %w", err)
	}
	if strings.TrimSpace(metadata.Format) == "" {
		return "", fmt.Errorf("qemu-img info output missing format")
	}
	return metadata.Format, nil
}

// findDeviceApplicationStatusByName returns the named application status entry or nil when absent.
func findDeviceApplicationStatusByName(applications []v1beta1.DeviceApplicationStatus, applicationName string) *v1beta1.DeviceApplicationStatus {
	for i := range applications {
		if applications[i].Name == applicationName {
			return &applications[i]
		}
	}
	return nil
}

// suiteLibvirtVMJournalSinceArgs builds a bounded journalctl command for a specific service and since timestamp.
func suiteLibvirtVMJournalSinceArgs(serviceName, since string, lines int) []string {
	args := []string{
		"sudo",
		"TZ=UTC",
		"journalctl",
		"--no-pager",
		"--no-hostname",
		"-u", serviceName,
		"--boot=all",
	}
	if strings.TrimSpace(since) != "" {
		args = append(args, "--since", since)
	}
	if lines > 0 {
		args = append(args, "-n", strconv.Itoa(lines))
	}
	return args
}
