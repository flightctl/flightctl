package certificate_rotation_test

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo/common"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	// certExpirySeconds controls the lifetime of management certificates
	// issued by the API server. 180s gives ~90s buffer after renewal window +
	// failure detection in the network disruption test (OCP-87911).
	certExpirySeconds = "180"

	// certRenewBeforeSeconds is the time before certificate expiration that
	// the agent will begin renewal. Renewal window opens at T+90s.
	certRenewBeforeSeconds = "90"

	// certManagerSyncInterval controls how often the agent checks if
	// a certificate renewal is needed.
	certManagerSyncInterval = "2s"

	// certBackoffMax controls the maximum backoff delay for certificate renewal
	// by the agent.
	certBackoffMax = "2s"

	// certRotationTimeout is the timeout for operations that involve waiting
	// for certificate rotation cycles to complete, including network
	// disruption recovery. 15 minutes accommodates slower OCP CI environments.
	certRotationTimeout = "15m"

	agentCertPath       = "/var/lib/flightctl/certs/agent.crt"
	agentCertBackupPath = "/var/lib/flightctl/certs/agent.crt.bak"
)

var _ = Describe("Certificate Rotation", Label("certificate-rotation"), func() {
	var (
		harness  *e2e.Harness
		deviceId string
	)

	BeforeEach(func() {
		harness = e2e.GetWorkerHarness()

		var dev *v1beta1.Device
		deviceId, dev = harness.EnrollAndWaitForOnlineStatus()
		Expect(dev).ToNot(BeNil())
		GinkgoWriter.Printf("Device enrolled: %s\n", deviceId)
	})

	Context("pre-rotation behavior", func() {
		It("should have device online with valid certificate", Label("87904"), func() {
			By("Verifying device is online")
			status, err := harness.GetDeviceWithStatusSummary(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(status).To(Equal(v1beta1.DeviceSummaryStatusOnline))

			By("Verifying system info is populated")
			sysInfo := harness.GetDeviceSystemInfo(deviceId)
			Expect(sysInfo).ToNot(BeNil())
			Expect(sysInfo.AgentVersion).ToNot(BeEmpty())
			Expect(sysInfo.Architecture).ToNot(BeEmpty())

			By("Waiting for management certificate info to appear in system info")
			var certNotAfter, certSerial string
			Eventually(func() bool {
				serial, notAfter, fetchErr := getDeviceCertInfo(harness, deviceId)
				if fetchErr != nil {
					return false
				}
				certSerial = serial
				certNotAfter = notAfter
				return true
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "management cert info should appear in system info")

			notAfterTime, err := time.Parse(time.RFC3339, certNotAfter)
			Expect(err).ToNot(HaveOccurred(), "managementCertNotAfter should be a valid RFC3339 timestamp")
			Expect(notAfterTime.After(time.Now())).To(BeTrue(), "certificate notAfter should be in the future")
			Expect(certSerial).ToNot(BeEmpty())

			By("Verifying certificate metrics via agent metrics endpoint")
			metricsOutput, err := harness.GetAgentMetrics()
			Expect(err).ToNot(HaveOccurred())

			loaded := e2e.ParseMetricValue(metricsOutput, "flightctl_device_mgmt_cert_loaded")
			Expect(loaded).To(Equal("1"), "flightctl_device_mgmt_cert_loaded should be 1")

			notAfterTS := e2e.ParseMetricValue(metricsOutput, "flightctl_device_mgmt_cert_not_after_timestamp_seconds")
			Expect(notAfterTS).ToNot(BeEmpty(), "cert not_after timestamp metric should be present")
		})
	})

	Context("certificate rotation", func() {
		It("should rotate certificate while device stays online", Label("87905"), func() {
			By("Waiting for initial certificate info to be reported")
			var initialSerial, initialNotAfter string
			Eventually(func() bool {
				serial, notAfter, fetchErr := getDeviceCertInfo(harness, deviceId)
				if fetchErr != nil {
					return false
				}
				initialSerial = serial
				initialNotAfter = notAfter
				return true
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "initial cert info should appear in system info")

			By("Waiting for certificate rotation (serial change)")
			Eventually(func() string {
				serial, _, fetchErr := getDeviceCertInfo(harness, deviceId)
				if fetchErr != nil {
					return initialSerial
				}
				return serial
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).ShouldNot(Equal(initialSerial), "certificate serial should change after rotation")

			newSerial, newNotAfter, err := getDeviceCertInfo(harness, deviceId)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("New cert serial: %s, notAfter: %s\n", newSerial, newNotAfter)

			By("Verifying device remains online after rotation")
			status, err := harness.GetDeviceWithStatusSummary(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(status).To(Equal(v1beta1.DeviceSummaryStatusOnline))

			By("Verifying device identity unchanged (no re-enrollment)")
			device, err := harness.GetDevice(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(*device.Metadata.Name).To(Equal(deviceId))

			By("Verifying certificate notAfter changed")
			Expect(newNotAfter).ToNot(Equal(initialNotAfter), "notAfter should differ after rotation")

			By("Checking renewal success metric")
			successCount, err := harness.GetAgentMetricValue(`flightctl_device_mgmt_cert_renewal_attempts_total{result="success"}`)
			Expect(err).ToNot(HaveOccurred())
			Expect(successCount).ToNot(BeEmpty(), "renewal success metric should be present")
			Expect(successCount).ToNot(Equal("0"), "at least one successful renewal should have occurred")
		})
	})

	Context("post-rotation validation", func() {
		It("should complete a second rotation cycle", Label("87906"), func() {
			By("Waiting for initial certificate info to be reported")
			var initialSerial string
			Eventually(func() bool {
				serial, _, fetchErr := getDeviceCertInfo(harness, deviceId)
				if fetchErr != nil {
					return false
				}
				initialSerial = serial
				return true
			}, e2e.LONGTIMEOUT, e2e.POLLING).Should(BeTrue(), "initial cert info should appear in system info")

			By("Waiting for the first certificate rotation")
			Eventually(func() string {
				serial, _, fetchErr := getDeviceCertInfo(harness, deviceId)
				if fetchErr != nil {
					return initialSerial
				}
				return serial
			}, e2e.LONGTIMEOUT, e2e.POLLING).ShouldNot(Equal(initialSerial), "first rotation should occur")

			firstRotationSerial, firstRotationNotAfter, err := getDeviceCertInfo(harness, deviceId)
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for the second certificate rotation")
			Eventually(func() string {
				serial, _, fetchErr := getDeviceCertInfo(harness, deviceId)
				if fetchErr != nil {
					return firstRotationSerial
				}
				return serial
			}, e2e.LONGTIMEOUT, e2e.POLLING).ShouldNot(Equal(firstRotationSerial), "second rotation should occur")

			_, secondRotationNotAfter, err := getDeviceCertInfo(harness, deviceId)
			Expect(err).ToNot(HaveOccurred())

			By("Verifying device remains online after second rotation")
			status, err := harness.GetDeviceWithStatusSummary(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(status).To(Equal(v1beta1.DeviceSummaryStatusOnline))

			By("Verifying device identity unchanged")
			device, err := harness.GetDevice(deviceId)
			Expect(err).ToNot(HaveOccurred())
			Expect(*device.Metadata.Name).To(Equal(deviceId))

			By("Verifying notAfter changed from first rotation")
			Expect(secondRotationNotAfter).ToNot(Equal(firstRotationNotAfter),
				"notAfter should differ between first and second rotation")

			By("Checking renewal success metric count >= 2")
			successCount, err := harness.GetAgentMetricValue(`flightctl_device_mgmt_cert_renewal_attempts_total{result="success"}`)
			Expect(err).ToNot(HaveOccurred())
			Expect(successCount).ToNot(BeEmpty(), "renewal success metric should be present")
			// The count should be at least 2 after two successful rotations
			Expect(successCount).ToNot(Equal("0"))
			Expect(successCount).ToNot(Equal("1"), "at least 2 successful renewals should have occurred")
		})
	})

	Context("negative tests", func() {
		It("should handle renewal with intermittent network disruption", Label("87911"), func() {
			By("Verifying metrics endpoint is accessible")
			Eventually(func() error {
				_, err := harness.GetAgentMetrics()
				return err
			}, e2e.TIMEOUT, e2e.POLLINGLONG).Should(Succeed(), "metrics endpoint should be accessible before test starts")

			By("Waiting for initial certificate info to be reported")
			var initialSerial, initialNotAfter string
			Eventually(func() bool {
				serial, notAfter, fetchErr := getDeviceCertInfo(harness, deviceId)
				if fetchErr != nil {
					GinkgoWriter.Printf("[87911] waiting for initial cert info: %v\n", fetchErr)
					return false
				}
				initialSerial = serial
				initialNotAfter = notAfter
				return true
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "initial cert info should appear in system info")
			GinkgoWriter.Printf("[87911] initial cert serial: %s, notAfter: %s\n", initialSerial, initialNotAfter)

			notAfterTime, err := time.Parse(time.RFC3339, initialNotAfter)
			Expect(err).ToNot(HaveOccurred(), "initialNotAfter should be a valid RFC3339 timestamp")

			By("Extracting API server endpoint from VM agent config")
			apiIP, apiPort, err := harness.GetAPIEndpointFromVM()
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("[87911] resolved API endpoint: %s:%s\n", apiIP, apiPort)

			By("Blocking agent→API traffic to prevent CSR submission")
			harness.BlockTrafficOnVM(apiIP, apiPort)
			DeferCleanup(func() {
				harness.UnblockTrafficOnVM(apiIP, apiPort)
			})

			By("Verifying iptables block is effective")
			verifyOutput, verifyErr := harness.VM.RunSSH([]string{
				"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
				"--connect-timeout", "5", "--max-time", "10",
				"-k", fmt.Sprintf("https://%s:%s/", apiIP, apiPort),
			}, nil)
			if verifyErr != nil {
				GinkgoWriter.Printf("[87911] block verified: curl failed as expected: %v\n", verifyErr)
			} else {
				GinkgoWriter.Printf("[87911] WARNING: curl succeeded despite block (status=%s), iptables may not be effective\n",
					strings.TrimSpace(verifyOutput.String()))
			}

			// Block traffic until 60s before cert expiry. This ensures:
			// 1. The renewal window is open (opens at certExpiry - certRenewBefore = 90s after issuance)
			// 2. At least one renewal attempt has failed (within 2s of the window opening)
			// 3. The agent still has a valid cert (60s remaining) to authenticate the renewal after unblock
			//
			// Without this safeguard, the cert can expire while blocked, permanently
			// locking the agent out of mTLS and making renewal impossible.
			safeBlockEnd := notAfterTime.Add(-60 * time.Second)
			blockDuration := time.Until(safeBlockEnd)
			if blockDuration > 0 {
				GinkgoWriter.Printf("[87911] blocking traffic for %v (until 60s before cert expiry at %s)\n", blockDuration, initialNotAfter)
				time.Sleep(blockDuration)
			}

			By("Restoring network connectivity")
			harness.UnblockTrafficOnVM(apiIP, apiPort)
			GinkgoWriter.Printf("[87911] traffic unblocked, %v remaining before cert expiry\n", time.Until(notAfterTime))

			By("Waiting for certificate rotation")
			var newNotAfter string
			Eventually(func() bool {
				serial, notAfter, fetchErr := getDeviceCertInfo(harness, deviceId)
				if fetchErr != nil {
					GinkgoWriter.Printf("[87911] waiting for rotation: %v\n", fetchErr)
					return false
				}
				GinkgoWriter.Printf("[87911] current serial: %s (initial: %s)\n", serial, initialSerial)
				if serial != initialSerial {
					newNotAfter = notAfter
					return true
				}
				return false
			}, certRotationTimeout, e2e.POLLINGLONG).Should(BeTrue(), "cert serial should change after unblocking traffic")

			By("Verifying new certificate notAfter is in the future")
			parsedNotAfter, parseErr := time.Parse(time.RFC3339, newNotAfter)
			Expect(parseErr).ToNot(HaveOccurred(), "new cert notAfter should be valid RFC3339")
			Expect(parsedNotAfter.After(time.Now())).To(BeTrue(), "new cert notAfter should be in the future")

			By("Verifying device remains online after certificate renewal")
			Eventually(func() v1beta1.DeviceSummaryStatusType {
				status, _ := harness.GetDeviceWithStatusSummary(deviceId)
				return status
			}, certRotationTimeout, e2e.POLLINGLONG).Should(Equal(v1beta1.DeviceSummaryStatusOnline))

			By("Checking renewal metrics")
			failCount, _ := harness.GetAgentMetricValue(`flightctl_device_mgmt_cert_renewal_attempts_total{result="failure"}`)
			GinkgoWriter.Printf("[87911] renewal failure count: %s\n", failCount)

			successCount, mErr := harness.GetAgentMetricValue(`flightctl_device_mgmt_cert_renewal_attempts_total{result="success"}`)
			Expect(mErr).ToNot(HaveOccurred())
			GinkgoWriter.Printf("[87911] renewal success count: %s\n", successCount)
			Expect(successCount).ToNot(BeEmpty(), "renewal success metric should be present")
			Expect(successCount).ToNot(Equal("0"), "at least one successful renewal should have occurred")
		})

		It("should handle certificate expiration", Label("87909"), func() {
			By("Waiting for initial certificate info to be reported")
			var initialSerial, initialNotAfter string
			Eventually(func() bool {
				serial, notAfter, fetchErr := getDeviceCertInfo(harness, deviceId)
				if fetchErr != nil {
					return false
				}
				initialSerial = serial
				initialNotAfter = notAfter
				return true
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "initial cert info should appear in system info")
			GinkgoWriter.Printf("Initial cert serial: %s, notAfter: %s\n", initialSerial, initialNotAfter)

			By("Extracting API server endpoint from VM agent config")
			apiIP, apiPort, err := harness.GetAPIEndpointFromVM()
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("API endpoint: %s:%s\n", apiIP, apiPort)

			By("Blocking agent→API traffic on the VM")
			harness.BlockTrafficOnVM(apiIP, apiPort)
			DeferCleanup(func() {
				harness.UnblockTrafficOnVM(apiIP, apiPort)
			})

			By("Waiting for renewal failure indicators in metrics")
			Eventually(func() bool {
				failCount, mErr := harness.GetAgentMetricValue(`flightctl_device_mgmt_cert_renewal_attempts_total{result="failure"}`)
				if mErr != nil {
					return false
				}
				return failCount != "" && failCount != "0"
			}, e2e.TIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "renewal failure metric should appear")

			By("Waiting for certificate to expire")
			notAfterTime, err := time.Parse(time.RFC3339, initialNotAfter)
			Expect(err).ToNot(HaveOccurred(), "initialNotAfter should be a valid RFC3339 timestamp")
			sleepDuration := time.Until(notAfterTime) + 5*time.Second
			if sleepDuration > 0 {
				GinkgoWriter.Printf("Sleeping %v until cert expires (notAfter=%s)\n", sleepDuration, initialNotAfter)
				time.Sleep(sleepDuration)
			}

			By("Unblocking agent→API traffic")
			harness.UnblockTrafficOnVM(apiIP, apiPort)

			By("Verifying certificate serial does NOT change (expired cert cannot renew)")
			Consistently(func() string {
				serial, _, fetchErr := getDeviceCertInfo(harness, deviceId)
				if fetchErr != nil {
					return initialSerial
				}
				return serial
			}, "30s", e2e.POLLINGLONG).Should(Equal(initialSerial), "cert serial should not change — expired cert cannot authenticate for renewal")
		})

		It("should handle invalid or corrupted certificate on disk", Label("87910"), func() {
			By("Waiting for initial certificate info to be reported")
			Eventually(func() bool {
				_, _, fetchErr := getDeviceCertInfo(harness, deviceId)
				return fetchErr == nil
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "initial cert info should appear in system info")

			By("Backing up the agent certificate")
			backupCertFile(harness)
			DeferCleanup(func() {
				restoreCertFile(harness)
				_ = harness.RestartFlightCtlAgent()
			})

			By("Corrupting the agent certificate file")
			corruptCertFile(harness)

			By("Restarting the agent with the corrupted certificate")
			err := harness.RestartFlightCtlAgent()
			Expect(err).ToNot(HaveOccurred())

			By("Verifying agent fails to start with corrupted certificate")
			Eventually(func() bool {
				return !harness.IsAgentServiceRunning()
			}, e2e.TIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "agent service should fail to start with a corrupted certificate")

			By("Restoring the certificate backup")
			restoreCertFile(harness)

			By("Restarting agent with restored certificate")
			err = harness.RestartFlightCtlAgent()
			Expect(err).ToNot(HaveOccurred())

			By("Verifying agent is running after cert restore")
			Eventually(func() bool {
				return harness.IsAgentServiceRunning()
			}, e2e.TIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "agent service should be running after cert restore")

			By("Verifying device comes back online after cert restore")
			Eventually(func() v1beta1.DeviceSummaryStatusType {
				status, _ := harness.GetDeviceWithStatusSummary(deviceId)
				return status
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(Equal(v1beta1.DeviceSummaryStatusOnline))
		})

		It("should reflect failure outcomes in metrics", Label("87912"), func() {
			By("Waiting for initial certificate info to be reported")
			Eventually(func() bool {
				_, _, fetchErr := getDeviceCertInfo(harness, deviceId)
				return fetchErr == nil
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "initial cert info should appear in system info")

			By("Verifying cert_loaded metric is 1 before inducing failure")
			Eventually(func() string {
				val, mErr := harness.GetAgentMetricValue("flightctl_device_mgmt_cert_loaded")
				if mErr != nil {
					return ""
				}
				return val
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(Equal("1"), "cert_loaded should be 1 initially")

			By("Verifying not_after timestamp is populated initially")
			notAfterTS, err := harness.GetAgentMetricValue("flightctl_device_mgmt_cert_not_after_timestamp_seconds")
			Expect(err).ToNot(HaveOccurred())
			Expect(notAfterTS).ToNot(BeEmpty(), "not_after timestamp should be present initially")
			Expect(notAfterTS).ToNot(Equal("0"), "not_after timestamp should be non-zero initially")

			By("Extracting API server endpoint from VM agent config")
			apiIP, apiPort, err := harness.GetAPIEndpointFromVM()
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("API endpoint: %s:%s\n", apiIP, apiPort)

			By("Blocking agent→API traffic to cause renewal failures")
			harness.BlockTrafficOnVM(apiIP, apiPort)
			DeferCleanup(func() {
				harness.UnblockTrafficOnVM(apiIP, apiPort)
			})

			By("Verifying agent service remains running while blocked")
			Consistently(func() bool {
				return harness.IsAgentServiceRunning()
			}, "30s", e2e.POLLINGLONG).Should(BeTrue(), "agent service should remain running with network blocked")

			By("Waiting for renewal failure or pending metrics to appear")
			Eventually(func() bool {
				metricsOutput, mErr := harness.GetAgentMetrics()
				if mErr != nil {
					return false
				}
				failCount := e2e.ParseMetricValue(metricsOutput, `flightctl_device_mgmt_cert_renewal_attempts_total{result="failure"}`)
				pendingCount := e2e.ParseMetricValue(metricsOutput, `flightctl_device_mgmt_cert_renewal_attempts_total{result="pending"}`)
				return (failCount != "" && failCount != "0") || (pendingCount != "" && pendingCount != "0")
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "failure or pending renewal metrics should exist")

			By("Verifying cert_loaded metric remains 1 (old cert still on disk)")
			metricsOutput, err := harness.GetAgentMetrics()
			Expect(err).ToNot(HaveOccurred())
			loaded := e2e.ParseMetricValue(metricsOutput, "flightctl_device_mgmt_cert_loaded")
			Expect(loaded).To(Equal("1"), "existing cert should still be loaded while blocked")

			By("Checking for renewal duration histogram metric")
			hasDuration := strings.Contains(metricsOutput, "flightctl_device_mgmt_cert_renewal_duration_seconds")
			GinkgoWriter.Printf("Has duration metric: %v\n", hasDuration)

			By("Restoring network connectivity")
			harness.UnblockTrafficOnVM(apiIP, apiPort)

			By("Verifying device recovers connectivity after network restore")
			Eventually(func() v1beta1.DeviceSummaryStatusType {
				status, _ := harness.GetDeviceWithStatusSummary(deviceId)
				return status
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(Equal(v1beta1.DeviceSummaryStatusOnline))
		})
	})
})

// getDeviceCertInfo extracts the management certificate serial and notAfter
// from the device's system info.
func getDeviceCertInfo(harness *e2e.Harness, deviceID string) (serial string, notAfter string, err error) {
	sysInfo := harness.GetDeviceSystemInfo(deviceID)
	if sysInfo == nil {
		return "", "", fmt.Errorf("system info not available for device %s", deviceID)
	}

	serial, serialFound := sysInfo.Get(common.ManagementCertSerialKey)
	if !serialFound {
		return "", "", fmt.Errorf("managementCertSerial not found in system info")
	}

	notAfter, notAfterFound := sysInfo.Get(common.ManagementCertNotAfterKey)
	if !notAfterFound {
		return "", "", fmt.Errorf("managementCertNotAfter not found in system info")
	}

	return serial, notAfter, nil
}

func backupCertFile(harness *e2e.Harness) {
	_, err := harness.VM.RunSSH([]string{
		"sudo", "cp", agentCertPath, agentCertBackupPath,
	}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to backup cert file")
}

func restoreCertFile(harness *e2e.Harness) {
	_, err := harness.VM.RunSSH([]string{
		"sudo", "cp", agentCertBackupPath, agentCertPath,
	}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to restore cert file from backup")
}

func corruptCertFile(harness *e2e.Harness) {
	_, err := harness.VM.RunSSH([]string{
		"sudo", "tee", agentCertPath,
	}, bytes.NewBufferString("invalid-cert-data\n"))
	Expect(err).ToNot(HaveOccurred(), "failed to corrupt cert file")
}
