package tpm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

var (
	logLookbackDuration = "10 minutes ago"
)

var _ = Describe("TPM Device Authentication", func() {
	var (
		ctx      context.Context
		harness  *e2e.Harness
		workerID int
	)

	AfterEach(func() {
		harness = e2e.GetWorkerHarness()
		suiteCtx := e2e.GetWorkerContext()

		// Always dump the tpm-blob contents as a backup in case a test ever does something
		// detrimental like take ownership of the storage hierarchy on a real TPM.
		if harness.VM != nil {
			blobOutput, blobErr := harness.VM.RunSSH([]string{"sudo", "cat", "/var/lib/flightctl/tpm-blob.yaml"}, nil)
			if blobErr != nil {
				logrus.Warnf("Failed to read tpm-blob.yaml: %v", blobErr)
			} else {
				logrus.Infof("tpm-blob.yaml contents:\n%s", blobOutput.String())
			}
		}

		harness.PrintAgentLogsIfFailed()

		if CurrentSpecReport().Failed() {
			if err := runTPMDiagnostic(harness); err != nil {
				logrus.Warnf("Failed to run TPM diagnostics: %v", err)
			}
		}

		ctx = util.StartSpecTracerForGinkgo(suiteCtx)
		harness.SetTestContext(ctx)

		err := harness.CleanUpTestResources()
		Expect(err).ToNot(HaveOccurred())
	})

	Context("software TPM", func() {
		BeforeEach(func() {
			workerID = GinkgoParallelProcess()
			harness = e2e.GetWorkerHarness()
			suiteCtx := e2e.GetWorkerContext()

			ctx = util.StartSpecTracerForGinkgo(suiteCtx)
			harness.SetTestContext(ctx)

			err := harness.SetupVMFromPoolWithTPM(workerID, e2e.TPMTypeSwtpm)
			Expect(err).ToNot(HaveOccurred())

			GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Test setup completed\n", workerID)
		})

		It("should enroll device with swtpm and verify attestation", Label("83974", "tpm", "sanity", "tpm-sw"), func() {
			runTPMEnrollmentTest(harness)
		})
	})

	Context("real TPM passthrough", func() {
		BeforeEach(func() {
			if !hasRealTPM {
				Skip(fmt.Sprintf("Skipping real TPM test: %s not available on host", realTPMDevice))
			}

			workerID = GinkgoParallelProcess()
			harness = e2e.GetWorkerHarness()
			suiteCtx := e2e.GetWorkerContext()

			ctx = util.StartSpecTracerForGinkgo(suiteCtx)
			harness.SetTestContext(ctx)

			err := harness.SetupVMWithTPMPassthrough(workerID, realTPMDevice)
			Expect(err).ToNot(HaveOccurred())

			By("verifying VM is using real TPM hardware (not swtpm)")
			detectedType, err := harness.DetectTPMType()
			Expect(err).ToNot(HaveOccurred())
			Expect(detectedType).To(Equal(e2e.TPMTypeReal), "Expected real TPM but detected swtpm - passthrough may have failed")
			GinkgoWriter.Printf("Confirmed real TPM hardware in VM\n")
		})

		It("should enroll device with real TPM passthrough and verify attestation", Label("83974", "tpm", "tpm-real"), func() {
			runTPMEnrollmentTest(harness)
		})
	})
})

func runTPMDiagnostic(harness *e2e.Harness) error {
	By("running TPM diagnostic commands inside VM")

	// Stop the agent so we have exclusive TPM access
	stopOutput, err := harness.VM.RunSSH([]string{"sudo", "systemctl", "stop", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("stopping flightctl-agent: %w (output: %s)", err, stopOutput)
	}

	script := `#!/bin/bash
echo "=== TPM Devices ==="
ls -la /dev/tpm* 2>/dev/null || echo "No TPM devices found"

echo ""
echo "=== TPM Fixed Properties ==="
tpm2_getcap properties-fixed 2>/dev/null || echo "Failed to get fixed properties"

echo ""
echo "=== TPM Variable Properties ==="
tpm2_getcap properties-variable 2>/dev/null || echo "Failed to get variable properties"

echo ""
echo "=== Persistent Handles ==="
tpm2_getcap handles-persistent 2>/dev/null || echo "No persistent handles"

echo ""
echo "=== Persistent EK Handles ==="
echo "RSA EK (0x81010001):"
tpm2_readpublic -c 0x81010001 2>&1 | head -5 || echo "  Not present"
echo "ECC EK (0x81010002):"
tpm2_readpublic -c 0x81010002 2>&1 | head -5 || echo "  Not present"

echo ""
echo "=== NV Indices (EK certs and templates) ==="
tpm2_nvreadpublic 0x01c00002 2>/dev/null && echo "RSA EK cert (0x01c00002): PRESENT" || echo "RSA EK cert (0x01c00002): ABSENT"
tpm2_nvreadpublic 0x01c0000a 2>/dev/null && echo "ECC EK cert (0x01c0000a): PRESENT" || echo "ECC EK cert (0x01c0000a): ABSENT"
tpm2_nvreadpublic 0x01c00004 2>/dev/null && echo "RSA EK template (0x01c00004): PRESENT" || echo "RSA EK template (0x01c00004): ABSENT"
tpm2_nvreadpublic 0x01c0000c 2>/dev/null && echo "ECC EK template (0x01c0000c): PRESENT" || echo "ECC EK template (0x01c0000c): ABSENT"

echo ""
echo "=== Transient Handles ==="
tpm2_getcap handles-transient 2>/dev/null || echo "No transient handles"

echo ""
echo "=== swtpm processes (should be empty for real TPM) ==="
ps aux | grep -i swtpm | grep -v grep || echo "No swtpm processes"

echo ""
echo "=== libvirt session domcapabilities (TPM) ==="
virsh -c qemu:///session domcapabilities 2>/dev/null | grep -A 10 '<tpm' || echo "Failed to get session domcapabilities"

echo ""
echo "=== libvirt session emulator ==="
virsh -c qemu:///session domcapabilities 2>/dev/null | grep '<path>' || echo "Failed"

echo ""
echo "=== libvirt session domain XML (TPM section) ==="
for dom in $(virsh -c qemu:///session list --name 2>/dev/null); do
    echo "Domain: ${dom}"
    virsh -c qemu:///session dumpxml "${dom}" 2>/dev/null | grep -A 5 '<tpm' || echo "  No TPM config"
done
`
	scriptStdin := bytes.NewBufferString(script)
	output, err := harness.VM.RunSSH([]string{"sudo", "bash"}, scriptStdin)
	if err != nil {
		return fmt.Errorf("TPM diagnostic commands failed: %w (output: %s)", err, output)
	}
	if output != nil {
		GinkgoWriter.Printf("%s\n", output.String())
	}

	// Restart the agent
	_, err = harness.VM.RunSSH([]string{"sudo", "systemctl", "start", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("restarting flightctl-agent: %w (output: %s)", err, output)
	}
	return nil
}

func runTPMEnrollmentTest(harness *e2e.Harness) {
	By("verifying agent reports TPM usage in logs")
	util.EventuallySlow(harness.ReadPrimaryVMAgentLogs).
		WithArguments(logLookbackDuration, util.FLIGHTCTL_AGENT_SERVICE).
		Should(ContainSubstring("Using TPM-based identity provider"))

	By("waiting for enrollment request with TPM attestation")
	var enrollmentID string

	Eventually(func() error {
		enrollmentID = harness.GetEnrollmentIDFromServiceLogs("flightctl-agent")
		if enrollmentID == "" {
			return errors.New("enrollment ID not found in agent logs")
		}
		logrus.Infof("Enrollment ID found: %s", enrollmentID)
		return nil
	}, util.TIMEOUT, util.POLLING).Should(Succeed())

	Eventually(func() error {
		enrollmentRequest := harness.WaitForEnrollmentRequest(enrollmentID)
		if enrollmentRequest == nil {
			return errors.New("enrollment request not found")
		}
		if enrollmentRequest.Spec.DeviceStatus == nil {
			return errors.New("device status is nil")
		}
		if enrollmentRequest.Spec.DeviceStatus.SystemInfo.IsEmpty() {
			return errors.New("system info is empty")
		}

		err := harness.VerifyEnrollmentTPMAttestationData(enrollmentRequest.Spec.DeviceStatus.SystemInfo)
		if err != nil {
			return err
		}

		logrus.Info("TPM attestation data found in enrollment request")
		return nil
	}, util.TIMEOUT, 5*util.POLLING).Should(Succeed())

	By("verifying TPM verified condition on enrollment request")
	Eventually(func() bool {
		resp, err := harness.Client.GetEnrollmentRequestWithResponse(harness.Context, enrollmentID)
		if err != nil || resp.JSON200 == nil || resp.JSON200.Status == nil {
			return false
		}
		cond := v1beta1.FindStatusCondition(resp.JSON200.Status.Conditions, v1beta1.ConditionTypeEnrollmentRequestTPMVerified)
		if cond == nil {
			return false
		}
		logrus.Infof("EnrollmentRequest TPMVerified condition: status=%s, reason=%s", cond.Status, cond.Reason)
		return cond.Status == v1beta1.ConditionStatusTrue
	}, 5*util.TIMEOUT, 5*util.POLLING).Should(BeTrue(), "EnrollmentRequest should have TPMVerified condition set to True")

	By("approving enrollment and waiting for device online")
	deviceId, _ := harness.EnrollAndWaitForOnlineStatus()

	By("checking TPM key persistence")
	Eventually(func() error {
		_, err := harness.VM.RunSSH([]string{"ls", "-la", "/var/lib/flightctl/tpm-blob.yaml"}, nil)
		return err
	}, util.TIMEOUT, 5*util.POLLING).Should(Succeed())

	By("verifying TPM integrity verification reports Verified status")
	var device *v1beta1.Device
	Eventually(func() bool {
		resp, err := harness.Client.GetDeviceWithResponse(harness.Context, deviceId)
		if err != nil || resp.JSON200 == nil {
			return false
		}
		device = resp.JSON200

		if device.Status == nil {
			return false
		}
		if device.Status.Integrity.Tpm == nil || device.Status.Integrity.DeviceIdentity == nil {
			return false
		}

		logrus.Infof("Integrity status - TPM: %s, DeviceIdentity: %s, Overall: %s",
			device.Status.Integrity.Tpm.Status,
			device.Status.Integrity.DeviceIdentity.Status,
			device.Status.Integrity.Status)

		return device.Status.Integrity.Tpm.Status == v1beta1.DeviceIntegrityCheckStatusVerified &&
			device.Status.Integrity.DeviceIdentity.Status == v1beta1.DeviceIntegrityCheckStatusVerified &&
			device.Status.Integrity.Status == v1beta1.DeviceIntegrityStatusVerified
	}, 2*util.TIMEOUT, 5*util.POLLING).Should(BeTrue(), "TPM integrity verification should reach Verified status")

	By("verifying TPM attestation data is present in device system info")
	err := harness.VerifyDeviceTPMAttestationData(device)
	Expect(err).ToNot(HaveOccurred())

	By("verifying TPM-based identity is used for communication")
	newRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceId)
	Expect(err).ToNot(HaveOccurred())

	testConfig := v1beta1.ConfigProviderSpec{}
	testFilePath := "/tmp/tpm-test-marker"
	testFileContent := fmt.Sprintf("TPM test marker - %d", time.Now().Unix())

	inlineConfig := v1beta1.InlineConfigProviderSpec{
		Name: "tpm-test-config",
		Inline: []v1beta1.FileSpec{
			{
				Path:    testFilePath,
				Content: testFileContent,
				Mode:    lo.ToPtr(0644),
			},
		},
	}
	err = testConfig.FromInlineConfigProviderSpec(inlineConfig)
	Expect(err).ToNot(HaveOccurred())

	err = harness.UpdateDeviceConfigWithRetries(deviceId, []v1beta1.ConfigProviderSpec{testConfig}, newRenderedVersion)
	Expect(err).ToNot(HaveOccurred())

	By("verifying the configuration was applied")
	var configOutput *bytes.Buffer
	configOutput, err = harness.VM.RunSSH([]string{"cat", testFilePath}, nil)
	Expect(err).ToNot(HaveOccurred())
	Expect(configOutput.String()).To(ContainSubstring("TPM test marker"))
}
