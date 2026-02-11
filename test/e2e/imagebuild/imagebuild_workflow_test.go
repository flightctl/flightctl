package imagebuild_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	imagebuilderapi "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/harness/e2e/vm"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

// Shared constants used across all imagebuild test files
const (
	// Test timeouts
	imageBuildTimeout    = 10 * time.Minute
	imageExportTimeout   = 15 * time.Minute
	processingTimeout    = 2 * time.Minute
	failureTimeout       = 3 * time.Minute
	processingPollPeriod = 5 * time.Second
	pollPeriodLong       = 10 * time.Second
	vmBootTimeout        = 2 * time.Minute
	cancelTimeout        = 1 * time.Minute
	vmBootPollPeriod     = 5 * time.Second
	agentServiceName     = "flightctl-agent"
	labelEnvironment     = "environment"
	imageBuildUsername   = "flightctl"
)

const (
	// Source image (centos-bootc from quay.io)
	sourceRegistry  = "quay.io"
	sourceImageName = "centos-bootc/centos-bootc"
	sourceImageTag  = "stream9"

	// Destination image
	destImageName = "centos-bootc-custom"

	// VM SSH port base for this test (use high port to avoid conflicts)
	vmSSHPortBase = 22800
)

var _ = Describe("ImageBuild", Label("imagebuild"), func() {
	Context("ImageBuild end-to-end workflow", func() {
		It("should verify basic image build process - build, export, download, use in an agent", Label("87335", "imagebuild", "slow"), func() {
			Expect(workerHarness).ToNot(BeNil())
			Expect(workerHarness.ImageBuilderClient).ToNot(BeNil(), "ImageBuilderClient must be available")

			testID := workerHarness.GetTestIDFromContext()
			registryAddress := workerHarness.RegistryEndpoint()

			sourceRepoName := fmt.Sprintf("source-repo-%s", testID)
			destRepoName := fmt.Sprintf("dest-repo-%s", testID)
			imageBuildName := fmt.Sprintf("test-build-%s", testID)
			imageExportName := fmt.Sprintf("test-export-qcow2-%s", testID)

			// ============================================================
			// Step 1: Create repositories
			// ============================================================

			By("Step 1: Creating repositories")

			_, err := resources.CreateOCIRepository(workerHarness, sourceRepoName, sourceRegistry,
				lo.ToPtr(api.Https), lo.ToPtr(api.Read), false, nil)
			Expect(err).ToNot(HaveOccurred())

			_, err = resources.CreateOCIRepository(workerHarness, destRepoName, registryAddress,
				lo.ToPtr(api.Https), lo.ToPtr(api.ReadWrite), true, nil)
			Expect(err).ToNot(HaveOccurred())

			// ============================================================
			// Step 2: Create ImageBuild with user configuration
			// ============================================================

			By("Step 2: Creating ImageBuild with SSH key user configuration")

			sshPubKeyPath, err := e2e.GetSSHPublicKeyPath()
			Expect(err).ToNot(HaveOccurred(), "Should get SSH public key path")
			sshPubKeyBytes, err := os.ReadFile(sshPubKeyPath)
			Expect(err).ToNot(HaveOccurred(), "Should be able to read SSH public key from %s", sshPubKeyPath)
			sshPublicKey := strings.TrimSpace(string(sshPubKeyBytes))

			spec := e2e.NewImageBuildSpecWithUserConfig(
				sourceRepoName,
				sourceImageName,
				sourceImageTag,
				destRepoName,
				destImageName,
				testID,
				imagebuilderapi.BindingTypeEarly,
				imageBuildUsername,
				sshPublicKey,
			)

			createdBuild, err := workerHarness.CreateImageBuildWithLabels(imageBuildName, spec, map[string]string{
				labelEnvironment: "production",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(createdBuild).ToNot(BeNil())
			Expect(createdBuild.Metadata).ToNot(BeNil())
			Expect(createdBuild.Metadata.Name).ToNot(BeNil())
			Expect(*createdBuild.Metadata.Name).To(Equal(imageBuildName), "Created ImageBuild should have expected name")

			// Wait for the build to start processing
			_, err = workerHarness.WaitForImageBuildProcessing(imageBuildName, processingTimeout, processingPollPeriod)
			Expect(err).ToNot(HaveOccurred(), "ImageBuild should start processing")

			// Stream logs until build completes or times out
			finalBuild, err := workerHarness.WaitForImageBuildWithLogs(imageBuildName, imageBuildTimeout)
			Expect(err).ToNot(HaveOccurred())
			Expect(finalBuild).ToNot(BeNil())

			// Verify ImageBuild status via client
			verifiedBuild, err := workerHarness.GetImageBuild(imageBuildName)
			Expect(err).ToNot(HaveOccurred())
			Expect(verifiedBuild).ToNot(BeNil())
			GinkgoWriter.Printf("ImageBuild %s status verified\n", imageBuildName)

			// Assert the build completed successfully
			reason, _ := workerHarness.GetImageBuildConditionReason(imageBuildName)
			Expect(reason).To(Equal(string(imagebuilderapi.ImageBuildConditionReasonCompleted)),
				"Expected ImageBuild to complete successfully")

			// ============================================================
			// Step 3: Build the image export
			// ============================================================

			By("Step 3: Creating QCOW2 ImageExport from the ImageBuild")

			exportSpec := e2e.NewImageExportSpec(imageBuildName, imagebuilderapi.ExportFormatTypeQCOW2)

			imageExport, err := workerHarness.CreateImageExport(imageExportName, exportSpec)
			Expect(err).ToNot(HaveOccurred())
			Expect(imageExport).ToNot(BeNil())
			Expect(imageExport.Metadata).ToNot(BeNil())
			Expect(imageExport.Metadata.Name).ToNot(BeNil())
			Expect(*imageExport.Metadata.Name).To(Equal(imageExportName), "Created ImageExport should have expected name")

			_, err = workerHarness.WaitForImageExportProcessing(imageExportName, processingTimeout, processingPollPeriod)
			Expect(err).ToNot(HaveOccurred(), "ImageExport should start processing")

			finalExport, err := workerHarness.WaitForImageExportWithLogs(imageExportName, imageExportTimeout)
			Expect(err).ToNot(HaveOccurred())
			Expect(finalExport).ToNot(BeNil())

			verifiedExport, err := workerHarness.GetImageExport(imageExportName)
			Expect(err).ToNot(HaveOccurred())
			Expect(verifiedExport).ToNot(BeNil())
			GinkgoWriter.Printf("ImageExport %s status verified\n", imageExportName)

			exportReason, _ := workerHarness.GetImageExportConditionReason(imageExportName)
			Expect(exportReason).To(Equal(string(imagebuilderapi.ImageExportConditionReasonCompleted)),
				"Expected ImageExport to complete successfully")

			// ============================================================
			// Step 4: Create a device using the disk image
			// ============================================================

			By("Step 4: Downloading the QCOW2 artifact and creating a device")

			tempDir, err := os.MkdirTemp("", "imagebuild-test-*")
			Expect(err).ToNot(HaveOccurred())
			defer os.RemoveAll(tempDir)

			qcow2Path := filepath.Join(tempDir, "disk.qcow2")

			body, contentLength, err := workerHarness.DownloadImageExport(imageExportName)
			Expect(err).ToNot(HaveOccurred(), "Download should succeed")
			Expect(body).ToNot(BeNil())
			defer body.Close()

			Expect(contentLength).To(BeNumerically(">", 0), "Content-Length should be positive")

			outFile, err := os.Create(qcow2Path)
			Expect(err).ToNot(HaveOccurred())

			bytesWritten, err := io.Copy(outFile, body)
			outFile.Close()
			Expect(err).ToNot(HaveOccurred(), "Should be able to write QCOW2 to disk")
			Expect(bytesWritten).To(BeNumerically(">", 0), "Should have written bytes to disk")

			GinkgoWriter.Printf("Downloaded QCOW2: %d bytes to %s\n", bytesWritten, qcow2Path)

			vmName := fmt.Sprintf("imagebuild-test-%s", testID)
			sshPort := vmSSHPortBase + GinkgoParallelProcess()
			sshPrivateKeyPath, err := e2e.GetSSHPrivateKeyPath()
			Expect(err).ToNot(HaveOccurred(), "Should get SSH private key path")

			testVM := vm.TestVM{
				TestDir:           tempDir,
				VMName:            vmName,
				DiskImagePath:     qcow2Path,
				VMUser:            imageBuildUsername,
				SSHPrivateKeyPath: sshPrivateKeyPath,
				SSHPort:           sshPort,
			}

			libvirtVM, err := vm.NewVM(testVM)
			Expect(err).ToNot(HaveOccurred(), "Should be able to create VM instance")

			defer func() {
				GinkgoWriter.Printf("Cleaning up VM %s\n", vmName)
				if cleanupErr := libvirtVM.ForceDelete(); cleanupErr != nil {
					GinkgoWriter.Printf("Warning: Failed to cleanup VM: %v\n", cleanupErr)
				}
			}()

			err = libvirtVM.CreateDomain()
			Expect(err).ToNot(HaveOccurred(), "Should be able to create VM domain")

			err = libvirtVM.RunAndWaitForSSH()
			Expect(err).ToNot(HaveOccurred(), "Should be able to start VM and SSH should become ready")

			isRunning, err := libvirtVM.IsRunning()
			Expect(err).ToNot(HaveOccurred())
			Expect(isRunning).To(BeTrue(), "VM should be running")

			GinkgoWriter.Printf("VM %s is running and SSH is ready!\n", vmName)

			// ============================================================
			// Step 5: Verify flightctl-agent service is running
			// ============================================================

			By("Step 5: Verifying flightctl-agent service is running via console")

		Eventually(func() (string, error) {
			return getAgentServiceStatus(libvirtVM)
			}, vmBootTimeout, vmBootPollPeriod).Should(Equal("active"),
				"flightctl-agent service should be running")

			GinkgoWriter.Printf("flightctl-agent service is running on VM %s\n", vmName)

			// ============================================================
			// Step 6: Get enrollment ID from agent logs
			// ============================================================

			By("Step 6: Getting enrollment ID from agent logs")

			enrollmentID := ""
			Eventually(func() string {
				enrollmentID = getEnrollmentIDFromAgentLogs(libvirtVM)
				return enrollmentID
			}, vmBootTimeout, vmBootPollPeriod).ShouldNot(BeEmpty(), "Enrollment ID should appear in agent logs")

			GinkgoWriter.Printf("Found enrollment ID: %s\n", enrollmentID)

			// ============================================================
			// Step 7: Wait for enrollment request on server
			// ============================================================

			By("Step 7: Waiting for enrollment request on server")

			enrollmentRequest := workerHarness.WaitForEnrollmentRequest(enrollmentID)
			Expect(enrollmentRequest).ToNot(BeNil())
			GinkgoWriter.Printf("Enrollment request received for device: %s\n", enrollmentID)

			// ============================================================
			// Step 8: Approve the enrollment request
			// ============================================================

			By("Step 8: Approving enrollment request")

			workerHarness.ApproveEnrollment(enrollmentID, testutil.TestEnrollmentApproval())
			GinkgoWriter.Printf("Enrollment approved for device: %s\n", enrollmentID)

			// ============================================================
			// Step 9: Wait for device to be online
			// ============================================================

			By("Step 9: Waiting for device to be online")

			Eventually(workerHarness.GetDeviceWithStatusSummary, vmBootTimeout, vmBootPollPeriod).
				WithArguments(enrollmentID).
				Should(Equal(api.DeviceSummaryStatusOnline), "Device should be online")

			GinkgoWriter.Printf("Device %s is online\n", enrollmentID)

			// ============================================================
			// Step 10: Test flightctl console to device
			// ============================================================

			By("Step 10: Testing flightctl console to device")

			consoleOutput, err := workerHarness.RunConsoleCommand(enrollmentID, []string{"--notty"}, "hostname")
			Expect(err).ToNot(HaveOccurred(), "Console command should succeed")
			Expect(consoleOutput).ToNot(BeEmpty(), "Console should return hostname")
			GinkgoWriter.Printf("Console output (hostname): %s\n", consoleOutput)

			GinkgoWriter.Printf("All verification steps completed successfully for device %s\n", enrollmentID)
		})
	})
})

// --- Helper functions (no Expect calls) ---

func getAgentServiceStatus(libvirtVM vm.TestVMInterface) (string, error) {
	if libvirtVM == nil {
		GinkgoWriter.Printf("getAgentServiceStatus: VM is nil\n")
		return "", nil
	}
	stdout, err := libvirtVM.RunSSH([]string{"sudo", "systemctl", "is-active", agentServiceName}, nil)
	if err != nil {
		GinkgoWriter.Printf("getAgentServiceStatus: %v\n", err)
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

func getEnrollmentIDFromAgentLogs(libvirtVM vm.TestVMInterface) string {
	if libvirtVM == nil {
		GinkgoWriter.Printf("getEnrollmentIDFromAgentLogs: VM is nil\n")
		return ""
	}
	stdout, err := libvirtVM.RunSSH([]string{"sudo", "journalctl", "-u", agentServiceName, "--no-pager"}, nil)
	if err != nil {
		GinkgoWriter.Printf("getEnrollmentIDFromAgentLogs: %v\n", err)
		return ""
	}
	return testutil.GetEnrollmentIdFromText(stdout.String())
}
