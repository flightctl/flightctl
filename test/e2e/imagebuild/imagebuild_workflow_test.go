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
	repoAccessibleTime   = 1 * time.Minute
	processingPollPeriod = 5 * time.Second
	vmBootTimeout        = 2 * time.Minute
	cancelTimeout        = 1 * time.Minute

	// Source image (centos-bootc from quay.io)
	sourceRegistry  = "quay.io"
	sourceImageName = "centos-bootc/centos-bootc"
	sourceImageTag  = "stream9"

	// Destination image
	destImageName = "centos-bootc-custom"

	// VM SSH port base for this test (use high port to avoid conflicts)
	vmSSHPortBase = 22800

	// Label keys for testing
	labelEnvironment = "environment"
)

var _ = Describe("ImageBuild", Label("imagebuild"), func() {
	Context("ImageBuild end-to-end workflow", Label("87335", "imagebuild", "slow"), func() {
		It("should verify basic image build process - build, export, download, use in an agent", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()
			Expect(harness).ToNot(BeNil())
			Expect(harness.ImageBuilderClient).ToNot(BeNil(), "ImageBuilderClient must be available")

			// Get test ID for unique resource names
			testID := harness.GetTestIDFromContext()
			registryAddress := harness.RegistryEndpoint()

			// Resource names
			sourceRepoName := fmt.Sprintf("source-repo-%s", testID)
			destRepoName := fmt.Sprintf("dest-repo-%s", testID)
			imageBuildName := fmt.Sprintf("test-build-%s", testID)
			imageExportName := fmt.Sprintf("test-export-qcow2-%s", testID)

			// Note: Resources are automatically cleaned up by AfterEach via CleanUpAllTestResources()
			// All ImageBuilds, ImageExports, and Repositories created with test labels will be deleted

			// ============================================================
			// Step 1: Create repositories
			// ============================================================

			By("Step 1: Creating repositories")

			_, err := resources.CreateOCIRepository(harness, sourceRepoName, sourceRegistry,
				lo.ToPtr(api.Https), lo.ToPtr(api.Read), false, nil)
			Expect(err).ToNot(HaveOccurred())

			_, err = resources.CreateOCIRepository(harness, destRepoName, registryAddress,
				lo.ToPtr(api.Https), lo.ToPtr(api.ReadWrite), true, nil)
			Expect(err).ToNot(HaveOccurred())

			// ============================================================
			// Step 2: Create ImageBuild with user configuration
			// ============================================================

			By("Step 2: Creating ImageBuild with SSH key user configuration")

			sshPubKeyPath := filepath.Join("..", "..", "..", "bin", ".ssh", "id_rsa.pub")
			sshPubKeyBytes, err := os.ReadFile(sshPubKeyPath)
			Expect(err).ToNot(HaveOccurred(), "Should be able to read SSH public key from %s", sshPubKeyPath)
			sshPublicKey := strings.TrimSpace(string(sshPubKeyBytes))

			const imageBuildUsername = "flightctl"

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

			_, err = harness.CreateImageBuildWithLabels(imageBuildName, spec, map[string]string{
				labelEnvironment: "production",
			})
			Expect(err).ToNot(HaveOccurred())

			// Wait for the build to start processing
			_, err = harness.WaitForImageBuildProcessing(imageBuildName, processingTimeout, processingPollPeriod)
			Expect(err).ToNot(HaveOccurred(), "ImageBuild should start processing")

			// Stream logs until build completes or times out
			finalBuild, err := harness.WaitForImageBuildWithLogs(imageBuildName, imageBuildTimeout)
			Expect(err).ToNot(HaveOccurred())
			Expect(finalBuild).ToNot(BeNil())

			// Verify ImageBuild status via client
			verifiedBuild, err := harness.GetImageBuild(imageBuildName)
			Expect(err).ToNot(HaveOccurred())
			Expect(verifiedBuild).ToNot(BeNil())
			GinkgoWriter.Printf("ImageBuild %s status verified\n", imageBuildName)

			// Assert the build completed successfully
			reason, _ := harness.GetImageBuildConditionReason(imageBuildName)
			Expect(reason).To(Equal(string(imagebuilderapi.ImageBuildConditionReasonCompleted)),
				"Expected ImageBuild to complete successfully")

			// ============================================================
			// Step 3: Build the image export
			// Expected: Verify that the state of the image export is completed
			// ============================================================

			By("Step 3: Creating QCOW2 ImageExport from the ImageBuild")

			exportSpec := e2e.NewImageExportSpec(imageBuildName, imagebuilderapi.ExportFormatTypeQCOW2)

			imageExport, err := harness.CreateImageExport(imageExportName, exportSpec)
			Expect(err).ToNot(HaveOccurred())
			Expect(imageExport).ToNot(BeNil())
			// Wait for the export to start processing
			_, err = harness.WaitForImageExportProcessing(imageExportName, processingTimeout, processingPollPeriod)
			Expect(err).ToNot(HaveOccurred(), "ImageExport should start processing")

			// Stream logs until export completes or times out
			finalExport, err := harness.WaitForImageExportWithLogs(imageExportName, imageExportTimeout)
			Expect(err).ToNot(HaveOccurred())
			Expect(finalExport).ToNot(BeNil())

			// Verify ImageExport status via client
			verifiedExport, err := harness.GetImageExport(imageExportName)
			Expect(err).ToNot(HaveOccurred())
			Expect(verifiedExport).ToNot(BeNil())
			GinkgoWriter.Printf("ImageExport %s status verified\n", imageExportName)

			exportReason, _ := harness.GetImageExportConditionReason(imageExportName)
			Expect(exportReason).To(Equal(string(imagebuilderapi.ImageExportConditionReasonCompleted)),
				"Expected ImageExport to complete successfully")

			// ============================================================
			// Step 4: Create a device using the disk image
			// Expected: Download QCOW2, boot VM, verify agent is running
			// ============================================================

			By("Step 4: Downloading the QCOW2 artifact and creating a device")

			tempDir, err := os.MkdirTemp("", "imagebuild-test-*")
			Expect(err).ToNot(HaveOccurred())
			defer os.RemoveAll(tempDir)

			qcow2Path := filepath.Join(tempDir, "disk.qcow2")

			body, contentLength, err := harness.DownloadImageExport(imageExportName)
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

			// Boot VM with SSH key authentication
			vmName := fmt.Sprintf("imagebuild-test-%s", testID)
			sshPort := vmSSHPortBase + GinkgoParallelProcess()

			// Use local SSH private key path for VM authentication
			// Path is relative to test directory: test/e2e/imagebuild -> bin/.ssh/id_rsa
			sshPrivateKeyPath := filepath.Join("..", "..", "..", "bin", ".ssh", "id_rsa")

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
			// Step 5: Connect to the device using a console
			// Expected: Verify flightctl-agent service is running
			// ============================================================

			By("Step 5: Verifying flightctl-agent service is running via console")

			Eventually(func() (string, error) {
				stdout, err := libvirtVM.RunSSH([]string{"sudo", "systemctl", "is-active", "flightctl-agent"}, nil)
				if err != nil {
					GinkgoWriter.Printf("flightctl-agent not ready yet: %v\n", err)
					return "", err
				}
				return stdout.String(), nil
			}, vmBootTimeout, 5*time.Second).Should(ContainSubstring("active"),
				"flightctl-agent service should be running")

			GinkgoWriter.Printf("flightctl-agent service is running on VM %s\n", vmName)

			// ============================================================
			// Step 6: Get enrollment ID from agent logs
			// Expected: Enrollment ID should appear in service logs
			// ============================================================

			By("Step 6: Getting enrollment ID from agent logs")

			var enrollmentID string
			Eventually(func() string {
				stdout, err := libvirtVM.RunSSH([]string{"sudo", "journalctl", "-u", "flightctl-agent", "--no-pager"}, nil)
				if err != nil {
					GinkgoWriter.Printf("Failed to get agent logs: %v\n", err)
					return ""
				}
				enrollmentID = testutil.GetEnrollmentIdFromText(stdout.String())
				return enrollmentID
			}, vmBootTimeout, 5*time.Second).ShouldNot(BeEmpty(), "Enrollment ID should appear in agent logs")

			GinkgoWriter.Printf("Found enrollment ID: %s\n", enrollmentID)

			// ============================================================
			// Step 7: Wait for enrollment request on server
			// Expected: Server should receive the enrollment request
			// ============================================================

			By("Step 7: Waiting for enrollment request on server")

			enrollmentRequest := harness.WaitForEnrollmentRequest(enrollmentID)
			Expect(enrollmentRequest).ToNot(BeNil())
			GinkgoWriter.Printf("Enrollment request received for device: %s\n", enrollmentID)

			// ============================================================
			// Step 8: Approve the enrollment request
			// Expected: Enrollment should be approved successfully
			// ============================================================

			By("Step 8: Approving enrollment request")

			harness.ApproveEnrollment(enrollmentID, harness.TestEnrollmentApproval())
			GinkgoWriter.Printf("Enrollment approved for device: %s\n", enrollmentID)

			// ============================================================
			// Step 9: Wait for device to be online
			// Expected: Device should report online status
			// ============================================================

			By("Step 9: Waiting for device to be online")

			Eventually(func() string {
				res, err := harness.GetDeviceWithStatusSummary(enrollmentID)
				if err != nil {
					return ""
				}
				return string(res)
			}, vmBootTimeout, 5*time.Second).Should(Equal(string(api.DeviceSummaryStatusOnline)),
				"Device should be online")

			GinkgoWriter.Printf("Device %s is online\n", enrollmentID)

			// ============================================================
			// Step 10: Open console to device using flightctl console
			// Expected: Console session should work and allow command execution
			// ============================================================

			By("Step 10: Testing flightctl console to device")

			consoleOutput, err := harness.RunConsoleCommand(enrollmentID, []string{"--notty"}, "hostname")
			Expect(err).ToNot(HaveOccurred(), "Console command should succeed")
			Expect(consoleOutput).ToNot(BeEmpty(), "Console should return hostname")
			GinkgoWriter.Printf("Console output (hostname): %s\n", consoleOutput)

			GinkgoWriter.Printf("All verification steps completed successfully for device %s\n", enrollmentID)
		})
	})
})
