package tpm_test

import (
	"context"
	"path/filepath"

	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var suiteCtx context.Context

var _ = BeforeSuite(func() {
	suiteCtx = context.Background()
})

var mode = 0644

var _ = Describe("TPM Device Authentication", func() {
	var (
		ctx     context.Context
		harness *e2e.Harness
	)

	BeforeEach(func() {
		ctx = util.StartSpecTracerForGinkgo(suiteCtx)
		harness = e2e.NewTestHarness(ctx)

		// Configure TPM in the agent config
		agentConfig := &agentcfg.Config{
			TPM: agentcfg.TPM{
				Enabled:          true,
				DevicePath:      "/dev/tpm0",
				StorageFilePath: filepath.Join(agentcfg.DefaultDataDir, agentcfg.DefaultTPMKeyFile),
				AuthEnabled:     true,
			},
		}
		err := harness.SetAgentConfig(agentConfig)
		Expect(err).ToNot(HaveOccurred())

		// Start the VM and wait for SSH
		err = harness.VM.RunAndWaitForSSH()
		Expect(err).ToNot(HaveOccurred())

		// Login to API
		login.LoginToAPIWithToken(harness)
	})

	AfterEach(func() {
		err := harness.CleanUpAllResources()
		Expect(err).ToNot(HaveOccurred())
		harness.Cleanup(true)
	})

	Context("TPM Enrollment", func() {
		It("Should enroll device with TPM enabled", Label("tpm", "sanity"), func() {
			By("verifying TPM device presence")
			stdout, err := harness.VM.RunSSH([]string{"ls", "-la", "/dev/tpm*"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("/dev/tpm0"))

			By("verifying TPM version")
			stdout, err = harness.VM.RunSSH([]string{"cat", "/sys/class/tpm/tpm0/tpm_version_major"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("2"))

			By("ensuring TPM configuration is enabled in agent config")
			// Get the enrollment Request ID from the console output
			enrollmentID := harness.GetEnrollmentIDFromConsole()
			logrus.Infof("Enrollment ID found in VM console output: %s", enrollmentID)

			// Wait for the device to create the enrollment request with TPM information
			enrollmentRequest := harness.WaitForEnrollmentRequest(enrollmentID)
			Expect(enrollmentRequest.Spec).ToNot(BeNil())
			Expect(enrollmentRequest.Spec.DeviceStatus).ToNot(BeNil())
			Expect(enrollmentRequest.Spec.DeviceStatus.SystemInfo.IsEmpty()).NotTo(BeTrue())

			// Verify TPM-specific enrollment data
			Expect(enrollmentRequest.Spec.TPMInfo).ToNot(BeNil())
			Expect(enrollmentRequest.Spec.TPMInfo.EndorsementKeyCertificate).ToNot(BeEmpty())

			// Approve the enrollment and wait for the device
			harness.ApproveEnrollment(enrollmentID, testutil.TestEnrollmentApproval())
			
			By("checking TPM key persistence")
			stdout, err = harness.VM.RunSSH([]string{"ls", "-la", "/var/lib/flightctl/tpm-blob.yaml"}, nil)
			Expect(err).ToNot(HaveOccurred())
			
			By("verifying device reports online status with TPM identity")
			deviceId, device := harness.WaitForDeviceOnlineStatus(enrollmentID)
			Expect(device.Status.AuthenticationMethod).To(Equal("tpm"))
			Expect(device.Status.TPMInfo.Manufacturer).ToNot(BeEmpty())
			
			By("verifying TPM-based identity is used for communication")
			// Make a config change to verify TPM-signed communication works
			newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
			Expect(err).ToNot(HaveOccurred())

			// Add an inline config to test TPM-authenticated updates
			err = harness.UpdateDeviceWithRetries(deviceId, func(device *v1alpha1.Device) {
				// Create inline config provider spec
				var configProviderSpec v1alpha1.ConfigProviderSpec
				inlineConfig := v1alpha1.InlineConfigProviderSpec{
					Name: "tpm-test-config",
					Inline: []v1alpha1.FileSpec{{
						Content: "TPM authenticated device",
						Mode:    &mode,
						Path:    "/etc/motd",
					}},
				}
				err := configProviderSpec.FromInlineConfigProviderSpec(inlineConfig)
				Expect(err).ToNot(HaveOccurred())
				
				device.Spec.Config = &[]v1alpha1.ConfigProviderSpec{configProviderSpec}
			})
			Expect(err).ToNot(HaveOccurred())

			// Verify config is applied using TPM identity
			logrus.Infof("Waiting for device to apply TPM-authenticated config update")
			harness.WaitForDeviceNewRenderedVersion(deviceId, newRenderedVersion)
			stdout, err = harness.VM.RunSSH([]string{"cat", "/etc/motd"}, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(stdout.String()).To(ContainSubstring("TPM authenticated device"))
		})
	})
})
