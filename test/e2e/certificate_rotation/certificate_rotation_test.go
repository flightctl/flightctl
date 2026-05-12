package certificate_rotation_test

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"text/template"
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

	metricCertLoaded                    = "flightctl_device_mgmt_cert_loaded"
	metricCertNotAfterTimestamp         = "flightctl_device_mgmt_cert_not_after_timestamp_seconds"
	metricRenewalDurationSecondsPrefix  = "flightctl_device_mgmt_cert_renewal_duration_seconds"
	metricRenewalAttemptsSuccess        = `flightctl_device_mgmt_cert_renewal_attempts_total{result="success"}`
	metricRenewalAttemptsFailure        = `flightctl_device_mgmt_cert_renewal_attempts_total{result="failure"}`
	metricRenewalAttemptsPending        = `flightctl_device_mgmt_cert_renewal_attempts_total{result="pending"}`
	errTokenCSR                         = "csr"
	errTokenMismatch                    = "mismatch"
	httpNoResponseStatusCode            = "000"
	rejectedRenewalSubmissionContext    = "mismatched renewal submission"
	rejectedRenewalResponseContext      = "renewal rejection"
	scriptErrorMarker                   = "script_error"
	stabilizationWindow                 = "30s"
	rejectedCSRMetricObservationWindow  = 10 * time.Second
	certExpiryGraceWindow               = 5 * time.Second
	outageLeadTime                      = 15 * time.Second
	minimumObservationDuration          = 5 * time.Second
	remainingBeforeUnblockSafetySeconds = 60 * time.Second
)

var errImmutableBitUnsupported = errors.New("immutable bit unsupported")

//go:embed testdata/renewal_auth_reject.sh.tmpl
var renewalAuthRejectScriptTemplate string

//go:embed testdata/csr_list_status.sh.tmpl
var csrListStatusScriptTemplate string

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
			certSerial, certNotAfter, err := waitForInitialCertInfo(harness, deviceId, e2e.LONGTIMEOUT, e2e.POLLINGLONG)
			Expect(err).ToNot(HaveOccurred(), "management cert info should appear in system info")

			notAfterTime, err := time.Parse(time.RFC3339, certNotAfter)
			Expect(err).ToNot(HaveOccurred(), "managementCertNotAfter should be a valid RFC3339 timestamp")
			Expect(notAfterTime.After(time.Now())).To(BeTrue(), "certificate notAfter should be in the future")
			Expect(certSerial).ToNot(BeEmpty())

			By("Verifying certificate metrics via agent metrics endpoint")
			metricsOutput, err := harness.GetAgentMetrics()
			Expect(err).ToNot(HaveOccurred())

			loaded := e2e.ParseMetricValue(metricsOutput, metricCertLoaded)
			Expect(loaded).To(Equal("1"), "flightctl_device_mgmt_cert_loaded should be 1")

			notAfterTS := e2e.ParseMetricValue(metricsOutput, metricCertNotAfterTimestamp)
			Expect(notAfterTS).ToNot(BeEmpty(), "cert not_after timestamp metric should be present")
		})
	})

	Context("certificate rotation", func() {
		It("should rotate certificate while device stays online", Label("87905"), func() {
			By("Waiting for initial certificate info to be reported")
			initialSerial, initialNotAfter, err := waitForInitialCertInfo(harness, deviceId, e2e.LONGTIMEOUT, e2e.POLLINGLONG)
			Expect(err).ToNot(HaveOccurred(), "initial cert info should appear in system info")

			By("Waiting for certificate rotation (serial change)")
			_, err = waitForCertSerialChange(harness, deviceId, initialSerial, e2e.LONGTIMEOUT, e2e.POLLINGLONG)
			Expect(err).ToNot(HaveOccurred(), "certificate serial should change after rotation")

			_, newNotAfter, err := getDeviceCertInfo(harness, deviceId)
			Expect(err).ToNot(HaveOccurred())

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
			successCount, err := harness.GetAgentMetricValue(metricRenewalAttemptsSuccess)
			Expect(err).ToNot(HaveOccurred())
			Expect(successCount).ToNot(BeEmpty(), "renewal success metric should be present")
			Expect(successCount).ToNot(Equal("0"), "at least one successful renewal should have occurred")
		})
	})

	Context("post-rotation validation", func() {
		It("should complete a second rotation cycle", Label("87906"), func() {
			By("Waiting for initial certificate info to be reported")
			initialSerial, _, err := waitForInitialCertInfo(harness, deviceId, e2e.LONGTIMEOUT, e2e.POLLING)
			Expect(err).ToNot(HaveOccurred(), "initial cert info should appear in system info")

			By("Waiting for the first certificate rotation")
			firstRotationSerial, err := waitForCertSerialChange(harness, deviceId, initialSerial, e2e.LONGTIMEOUT, e2e.POLLING)
			Expect(err).ToNot(HaveOccurred(), "first rotation should occur")

			_, firstRotationNotAfter, err := getDeviceCertInfo(harness, deviceId)
			Expect(err).ToNot(HaveOccurred())

			By("Waiting for the second certificate rotation")
			_, err = waitForCertSerialChange(harness, deviceId, firstRotationSerial, e2e.LONGTIMEOUT, e2e.POLLING)
			Expect(err).ToNot(HaveOccurred(), "second rotation should occur")

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
			successCount, err := harness.GetAgentMetricValue(metricRenewalAttemptsSuccess)
			Expect(err).ToNot(HaveOccurred())
			Expect(successCount).ToNot(BeEmpty(), "renewal success metric should be present")
			// The count should be at least 2 after two successful rotations
			Expect(successCount).ToNot(Equal("0"))
			Expect(successCount).ToNot(Equal("1"), "at least 2 successful renewals should have occurred")
		})
	})

	Context("negative tests", func() {
		It("should reject renewal requests when CSR identity does not match presenting device identity", Label("88804"), func() {
			By("Waiting for the device management certificate info to be reported")
			_, _, err := waitForInitialCertInfo(harness, deviceId, e2e.LONGTIMEOUT, e2e.POLLINGLONG)
			Expect(err).ToNot(HaveOccurred(), "initial cert info should appear in system info")

			By("Capturing baseline renewal metrics before sending a rejected CSR")
			baselineCounts, err := getRenewalAttemptSnapshot(harness)
			Expect(err).ToNot(HaveOccurred())

			By("Extracting API server endpoint from VM agent config")
			apiHost, apiIP, apiPort, err := harness.GetAPIEndpointHostIPPortFromVM()
			Expect(err).ToNot(HaveOccurred())
			Expect(apiHost).ToNot(BeEmpty())
			Expect(apiIP).ToNot(BeEmpty())
			Expect(apiPort).ToNot(BeEmpty())

			By("Verifying the management API is reachable from this VM before sending the mismatched CSR")
			Eventually(managementAPIReachableCheckFunc(harness, apiHost, apiIP, apiPort), e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(Succeed(),
				"management API should be reachable before submitting mismatched renewal CSR")

			By("Submitting a renewal CSR with a mismatched CSR common name while authenticating as the device")
			var statusCode, responseBody string
			Eventually(rejectedRenewalValidationCheckFunc(harness, deviceId, apiHost, apiIP, apiPort, &statusCode, &responseBody), e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(Succeed(),
				"renewal request should eventually reach the API and return a 4xx validation status")

			By("Verifying the API handled the CSR request and returned a validation error")
			Expect(statusCode).ToNot(Equal(httpNoResponseStatusCode), "request should reach the API and not fail at transport/TLS layer")
			statusCodeInt, convErr := parseHTTPStatusCode(statusCode, rejectedRenewalResponseContext)
			Expect(convErr).ToNot(HaveOccurred(), "renewal rejection should return a numeric HTTP status code")
			Expect(statusCodeInt).To(BeNumerically(">=", 400), "renewal request with mismatched CSR identity should be rejected")
			Expect(statusCodeInt).To(BeNumerically("<", 500), "renewal request with mismatched CSR identity should fail with 4xx")
			responseBodyLower := strings.ToLower(strings.TrimSpace(responseBody))
			if responseBodyLower != "" {
				Expect(responseBodyLower).To(ContainSubstring(errTokenCSR))
				Expect(responseBodyLower).To(ContainSubstring(errTokenMismatch))
			}

			By("Verifying device remains online after the rejected request")
			harness.WaitForDeviceContents(deviceId, "device remains online after rejected renewal request", isDeviceOnline, e2e.TIMEOUT)

			By("Verifying renewal counter deltas stay within expected background activity")
			observationWindow := rejectedCSRMetricObservationWindow
			syncIntervalDuration, parseErr := time.ParseDuration(certManagerSyncInterval)
			Expect(parseErr).ToNot(HaveOccurred(), "certManagerSyncInterval should be parseable as duration")
			Expect(syncIntervalDuration).To(BeNumerically(">", 0), "certManagerSyncInterval should be > 0")
			allowedBackgroundRenewals := math.Ceil(float64(observationWindow)/float64(syncIntervalDuration)) + 1

			observationStart := time.Now()
			var afterCounts map[string]float64
			Eventually(renewalAttemptSnapshotObservedFunc(harness, observationStart, observationWindow, &afterCounts), e2e.TIMEOUT, e2e.POLLINGLONG).Should(BeTrue(),
				"should capture a valid renewal snapshot after the observation window")

			for result, delta := range map[string]float64{
				"success": afterCounts["success"] - baselineCounts["success"],
				"failure": afterCounts["failure"] - baselineCounts["failure"],
				"pending": afterCounts["pending"] - baselineCounts["pending"],
			} {
				Expect(delta).To(BeNumerically(">=", 0), fmt.Sprintf("%s counter should be monotonic", result))
				Expect(delta).To(BeNumerically("<=", allowedBackgroundRenewals),
					fmt.Sprintf("%s counter delta exceeded expected background renewal activity", result))
			}
		})

		It("should recover after management service unavailability", Serial, Label("88805"), func() {
			By("Waiting for initial certificate info to be reported")
			initialSerial, initialNotAfter, err := waitForInitialCertInfo(harness, deviceId, e2e.LONGTIMEOUT, e2e.POLLINGLONG)
			Expect(err).ToNot(HaveOccurred(), "initial cert info should appear in system info")
			notAfterTime, parseErr := time.Parse(time.RFC3339, initialNotAfter)
			Expect(parseErr).ToNot(HaveOccurred(), "initialNotAfter should be a valid RFC3339 timestamp")

			renewBeforeSeconds, atoiErr := strconv.Atoi(certRenewBeforeSeconds)
			Expect(atoiErr).ToNot(HaveOccurred(), "certRenewBeforeSeconds should be parseable")
			renewWindowStart := notAfterTime.Add(-time.Duration(renewBeforeSeconds) * time.Second)
			// Block slightly before the renewal window opens so the first renewal
			// attempt happens while management API connectivity is disrupted.
			outageWindowStart := renewWindowStart.Add(-outageLeadTime)
			if time.Until(outageWindowStart) > 0 {
				By("Waiting to block API traffic shortly before the renewal window opens")
				waitUntilTime(outageWindowStart, e2e.LONGTIMEOUT, e2e.POLLINGLONG,
					"should reach the outage start time before blocking API traffic")
			}

			apiIP, apiPort, err := harness.GetAPIEndpointFromVM()
			Expect(err).ToNot(HaveOccurred())
			Expect(apiIP).ToNot(BeEmpty())
			Expect(apiPort).ToNot(BeEmpty())

			DeferCleanup(func() {
				harness.UnblockTrafficOnVM(apiIP, apiPort)
			})

			By("Blocking this worker VM from reaching the API during the renewal window")
			harness.BlockTrafficOnVM(apiIP, apiPort)

			By("Verifying the management endpoint becomes unreachable from this VM")
			Eventually(managementAPIUnreachableStatusFunc(harness, apiIP, apiPort), e2e.LONGTIMEOUT, e2e.POLLINGLONG).ShouldNot(BeEmpty(),
				"management endpoint should fail with a connection error or timeout while blocked")

			By("Verifying the current certificate remains installed while the API is unavailable")
			observeUntil := renewWindowStart.Add(outageLeadTime)
			observeDuration := time.Until(observeUntil)
			if observeDuration < minimumObservationDuration {
				observeDuration = minimumObservationDuration
			}
			Consistently(deviceCertSerialOrFallbackFunc(harness, deviceId, initialSerial), observeDuration.String(), e2e.POLLINGLONG).
				Should(Equal(initialSerial), "certificate serial should remain unchanged while the API is unavailable")

			By("Restoring API connectivity for this worker VM")
			harness.UnblockTrafficOnVM(apiIP, apiPort)

			By("Waiting for certificate rotation after service recovery")
			Eventually(deviceCertSerialOrFallbackFunc(harness, deviceId, initialSerial), certRotationTimeout, e2e.POLLINGLONG).
				ShouldNot(Equal(initialSerial), "certificate should rotate after the API service recovers")

			By("Verifying the device remains online after recovery")
			harness.WaitForDeviceContents(deviceId, "device remains online after management API recovery", isDeviceOnline, certRotationTimeout)
		})

		It("should preserve the current certificate when certificate installation fails", Label("88806"), func() {
			By("Waiting for initial certificate info to be reported")
			initialSerial, _, err := waitForInitialCertInfo(harness, deviceId, e2e.LONGTIMEOUT, e2e.POLLINGLONG)
			Expect(err).ToNot(HaveOccurred(), "initial cert info should appear in system info")

			DeferCleanup(func() {
				Expect(clearImmutableCertFlag(harness)).To(Succeed(), "cleanup should clear immutable bit")
			})

			By("Making the management certificate file immutable")
			if err := ensureImmutableBitSupported(harness); errors.Is(err, errImmutableBitUnsupported) {
				Skip(err.Error())
			} else {
				Expect(err).ToNot(HaveOccurred())
			}
			Expect(makeCertImmutable(harness)).To(Succeed())

			By("Waiting for renewal failures while the new certificate cannot be installed")
			Eventually(agentMetricNonZeroFunc(harness, metricRenewalAttemptsFailure), e2e.LONGTIMEOUT, e2e.POLLINGLONG).
				Should(BeTrue(), "renewal failures should be recorded when install fails")

			By("Verifying the current certificate remains active")
			Consistently(deviceCertSerialOrFallbackFunc(harness, deviceId, initialSerial), stabilizationWindow, e2e.POLLINGLONG).
				Should(Equal(initialSerial), "certificate serial should remain unchanged when install fails")

			By("Clearing the immutable flag and waiting for recovery")
			Expect(clearImmutableCertFlag(harness)).To(Succeed())

			_, err = waitForCertSerialChange(harness, deviceId, initialSerial, certRotationTimeout, e2e.POLLINGLONG)
			Expect(err).ToNot(HaveOccurred(), "certificate should rotate after install becomes possible again")
		})

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
			GinkgoWriter.Printf("[87911] resolved API endpoint: %s:%s\n", apiIP, apiPort)
			Expect(err).ToNot(HaveOccurred())

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
			safeBlockEnd := notAfterTime.Add(-remainingBeforeUnblockSafetySeconds)
			if time.Until(safeBlockEnd) > 0 {
				By("Waiting until it is safe to restore connectivity before certificate expiry")
				waitUntilTime(safeBlockEnd, certRotationTimeout, e2e.POLLINGLONG,
					"should reach the safe unblock time before restoring connectivity")
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
			successCount, mErr := harness.GetAgentMetricValue(metricRenewalAttemptsSuccess)
			GinkgoWriter.Printf("[87911] renewal success count: %s\n", successCount)
			Expect(mErr).ToNot(HaveOccurred())
			Expect(successCount).ToNot(BeEmpty(), "renewal success metric should be present")
			Expect(successCount).ToNot(Equal("0"), "at least one successful renewal should have occurred")
		})

		It("should handle certificate expiration", Label("87909"), func() {
			By("Waiting for initial certificate info to be reported")
			initialSerial, initialNotAfter, err := waitForInitialCertInfo(harness, deviceId, e2e.LONGTIMEOUT, e2e.POLLINGLONG)
			Expect(err).ToNot(HaveOccurred(), "initial cert info should appear in system info")
			GinkgoWriter.Printf("[87909] initial cert serial: %s, notAfter: %s\n", initialSerial, initialNotAfter)

			By("Extracting API server endpoint from VM agent config")
			apiIP, apiPort, err := harness.GetAPIEndpointFromVM()
			GinkgoWriter.Printf("[87909] resolved API endpoint: %s:%s\n", apiIP, apiPort)
			Expect(err).ToNot(HaveOccurred())

			By("Blocking agent→API traffic on the VM")
			harness.BlockTrafficOnVM(apiIP, apiPort)
			DeferCleanup(func() {
				harness.UnblockTrafficOnVM(apiIP, apiPort)
			})

			By("Waiting for renewal failure indicators in metrics")
			Eventually(func() bool {
				failCount, mErr := harness.GetAgentMetricValue(metricRenewalAttemptsFailure)
				if mErr != nil {
					return false
				}
				return failCount != "" && failCount != "0"
			}, e2e.TIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "renewal failure metric should appear")

			By("Waiting for certificate to expire")
			notAfterTime, err := time.Parse(time.RFC3339, initialNotAfter)
			Expect(err).ToNot(HaveOccurred(), "initialNotAfter should be a valid RFC3339 timestamp")
			expirationObservationTime := notAfterTime.Add(certExpiryGraceWindow)
			if time.Until(expirationObservationTime) > 0 {
				waitUntilTime(expirationObservationTime, certRotationTimeout, e2e.POLLINGLONG,
					"should reach the certificate expiration observation time")
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
			}, stabilizationWindow, e2e.POLLINGLONG).Should(Equal(initialSerial), "cert serial should not change — expired cert cannot authenticate for renewal")
		})

		It("should handle invalid or corrupted certificate on disk", Label("87910"), func() {
			By("Waiting for initial certificate info to be reported")
			Eventually(func() bool {
				_, _, fetchErr := getDeviceCertInfo(harness, deviceId)
				return fetchErr == nil
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "initial cert info should appear in system info")

			By("Backing up the agent certificate")
			Expect(backupCertFile(harness)).To(Succeed())
			DeferCleanup(func() {
				Expect(restoreCertFile(harness)).To(Succeed(), "cleanup should restore the original agent certificate")
				Expect(harness.RestartFlightCtlAgent()).To(Succeed(), "cleanup should restart the agent after certificate restore")
			})

			By("Corrupting the agent certificate file")
			Expect(corruptCertFile(harness)).To(Succeed())

			By("Restarting the agent with the corrupted certificate")
			err := harness.RestartFlightCtlAgent()
			Expect(err).ToNot(HaveOccurred())

			By("Verifying agent fails to start with corrupted certificate")
			Eventually(func() bool {
				return !harness.IsAgentServiceRunning()
			}, e2e.TIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "agent service should fail to start with a corrupted certificate")

			By("Restoring the certificate backup")
			Expect(restoreCertFile(harness)).To(Succeed())

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
				val, mErr := harness.GetAgentMetricValue(metricCertLoaded)
				if mErr != nil {
					return ""
				}
				return val
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(Equal("1"), "cert_loaded should be 1 initially")

			By("Verifying not_after timestamp is populated initially")
			notAfterTS, err := harness.GetAgentMetricValue(metricCertNotAfterTimestamp)
			Expect(err).ToNot(HaveOccurred())
			Expect(notAfterTS).ToNot(BeEmpty(), "not_after timestamp should be present initially")
			Expect(notAfterTS).ToNot(Equal("0"), "not_after timestamp should be non-zero initially")

			By("Extracting API server endpoint from VM agent config")
			apiIP, apiPort, err := harness.GetAPIEndpointFromVM()
			GinkgoWriter.Printf("[87912] resolved API endpoint: %s:%s\n", apiIP, apiPort)
			Expect(err).ToNot(HaveOccurred())

			By("Blocking agent→API traffic to cause renewal failures")
			harness.BlockTrafficOnVM(apiIP, apiPort)
			DeferCleanup(func() {
				harness.UnblockTrafficOnVM(apiIP, apiPort)
			})

			By("Verifying agent service remains running while blocked")
			Consistently(func() bool {
				return harness.IsAgentServiceRunning()
			}, stabilizationWindow, e2e.POLLINGLONG).Should(BeTrue(), "agent service should remain running with network blocked")

			By("Waiting for renewal failure or pending metrics to appear")
			Eventually(func() bool {
				metricsOutput, mErr := harness.GetAgentMetrics()
				if mErr != nil {
					return false
				}
				failCount := e2e.ParseMetricValue(metricsOutput, metricRenewalAttemptsFailure)
				pendingCount := e2e.ParseMetricValue(metricsOutput, metricRenewalAttemptsPending)
				return (failCount != "" && failCount != "0") || (pendingCount != "" && pendingCount != "0")
			}, e2e.LONGTIMEOUT, e2e.POLLINGLONG).Should(BeTrue(), "failure or pending renewal metrics should exist")

			By("Verifying cert_loaded metric remains 1 (old cert still on disk)")
			metricsOutput, err := harness.GetAgentMetrics()
			Expect(err).ToNot(HaveOccurred())
			loaded := e2e.ParseMetricValue(metricsOutput, metricCertLoaded)
			Expect(loaded).To(Equal("1"), "existing cert should still be loaded while blocked")

			By("Checking for renewal duration histogram metric")
			hasDuration := strings.Contains(metricsOutput, metricRenewalDurationSecondsPrefix)
			Expect(hasDuration).To(BeTrue(), "renewal duration histogram metric should be present")

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

// waitForInitialCertInfo polls until management certificate serial/notAfter are available.
func waitForInitialCertInfo(harness *e2e.Harness, deviceID, timeout, polling string) (serial, notAfter string, err error) {
	return pollForCertInfo(harness, deviceID, timeout, polling, func(currentSerial, _ string) bool {
		return strings.TrimSpace(currentSerial) != ""
	})
}

// waitForCertSerialChange polls until the management certificate serial changes from the baseline.
func waitForCertSerialChange(harness *e2e.Harness, deviceID, initialSerial, timeout, polling string) (newSerial string, err error) {
	serial, _, err := pollForCertInfo(harness, deviceID, timeout, polling, func(currentSerial, _ string) bool {
		return currentSerial != "" && currentSerial != initialSerial
	})
	return serial, err
}

func waitUntilTime(target time.Time, timeout, polling, failureMessage string) {
	Eventually(func() bool {
		return !time.Now().Before(target)
	}, timeout, polling).Should(BeTrue(), failureMessage)
}

func isDeviceOnline(device *v1beta1.Device) bool {
	return device != nil &&
		device.Status != nil &&
		device.Status.Summary.Status == v1beta1.DeviceSummaryStatusOnline
}

func deviceCertSerialOrFallbackFunc(harness *e2e.Harness, deviceID, fallbackSerial string) func() string {
	return func() string {
		if harness == nil {
			GinkgoWriter.Printf("cert serial check skipped: harness is nil for device %s\n", deviceID)
			return fallbackSerial
		}

		serial, _, err := getDeviceCertInfo(harness, deviceID)
		if err != nil {
			GinkgoWriter.Printf("cert serial check failed for device %s: %v\n", deviceID, err)
			return fallbackSerial
		}
		if strings.TrimSpace(serial) == "" {
			GinkgoWriter.Printf("cert serial check returned empty serial for device %s\n", deviceID)
			return fallbackSerial
		}

		return serial
	}
}

func managementAPIReachableCheckFunc(harness *e2e.Harness, apiHost, apiIP, apiPort string) func() error {
	return func() error {
		statusCode, curlRC, stderr, err := getCSRListStatusWithManagementCertAtEndpoint(harness, apiHost, apiIP, apiPort)
		if err != nil {
			GinkgoWriter.Printf("management API reachability check failed: %v\n", err)
			return err
		}
		if strings.TrimSpace(curlRC) != "0" {
			return fmt.Errorf("curl exited with rc=%q stderr=%q", curlRC, stderr)
		}

		trimmedCode := strings.TrimSpace(statusCode)
		if trimmedCode == "" || trimmedCode == httpNoResponseStatusCode {
			return fmt.Errorf("unexpected HTTP status code %q stderr=%q", trimmedCode, stderr)
		}

		return nil
	}
}

func rejectedRenewalValidationCheckFunc(
	harness *e2e.Harness,
	deviceID, apiHost, apiIP, apiPort string,
	statusCodeOut, responseBodyOut *string,
) func() error {
	return func() error {
		currentStatusCode, currentResponseBody, err := submitRenewalCSRWithMismatchedIdentityAtEndpoint(harness, deviceID, apiHost, apiIP, apiPort)
		if err != nil {
			GinkgoWriter.Printf("rejected renewal validation attempt failed for device %s: %v\n", deviceID, err)
			return err
		}

		if statusCodeOut != nil {
			*statusCodeOut = strings.TrimSpace(currentStatusCode)
		}
		if responseBodyOut != nil {
			*responseBodyOut = currentResponseBody
		}

		statusCodeInt, err := parseHTTPStatusCode(currentStatusCode, rejectedRenewalSubmissionContext)
		if err != nil {
			return err
		}
		if statusCodeInt == 0 {
			return fmt.Errorf("transport-level status %q (request did not get HTTP response)", currentStatusCode)
		}
		if statusCodeInt >= 500 {
			return fmt.Errorf("transient server status %d", statusCodeInt)
		}
		if statusCodeInt >= 400 && statusCodeInt < 500 {
			return nil
		}

		return fmt.Errorf("unexpected non-4xx status %d", statusCodeInt)
	}
}

func renewalAttemptSnapshotObservedFunc(
	harness *e2e.Harness,
	observationStart time.Time,
	observationWindow time.Duration,
	afterCounts *map[string]float64,
) func() bool {
	return func() bool {
		if harness == nil {
			GinkgoWriter.Printf("renewal snapshot check skipped: harness is nil\n")
			return false
		}

		snapshot, err := getRenewalAttemptSnapshot(harness)
		if err != nil {
			GinkgoWriter.Printf("renewal snapshot check failed: %v\n", err)
			return false
		}
		if afterCounts != nil {
			*afterCounts = snapshot
		}

		return time.Since(observationStart) >= observationWindow
	}
}

func managementAPIUnreachableStatusFunc(harness *e2e.Harness, apiIP, apiPort string) func() string {
	return func() string {
		statusCode, curlRC, _, err := getCSRListStatusWithManagementCertAtEndpoint(harness, apiIP, apiIP, apiPort)
		if err != nil {
			GinkgoWriter.Printf("management API unreachable check failed: %v\n", err)
			return scriptErrorMarker
		}
		if curlRC != "0" {
			return curlRC
		}
		if statusCode == httpNoResponseStatusCode {
			return statusCode
		}
		return ""
	}
}

func agentMetricNonZeroFunc(harness *e2e.Harness, metricName string) func() bool {
	return func() bool {
		if harness == nil {
			GinkgoWriter.Printf("metric check skipped: harness is nil for metric %s\n", metricName)
			return false
		}

		value, err := harness.GetAgentMetricValue(metricName)
		if err != nil {
			GinkgoWriter.Printf("metric check failed for %s: %v\n", metricName, err)
			return false
		}

		trimmedValue := strings.TrimSpace(value)
		return trimmedValue != "" && trimmedValue != "0"
	}
}

// pollForCertInfo repeatedly fetches certificate info until the stop predicate returns true or timeout expires.
func pollForCertInfo(
	harness *e2e.Harness,
	deviceID, timeout, polling string,
	stop func(serial, notAfter string) bool,
) (serial, notAfter string, err error) {
	if harness == nil {
		return "", "", fmt.Errorf("nil harness")
	}
	if stop == nil {
		return "", "", fmt.Errorf("nil stop predicate")
	}

	timeoutDuration, err := time.ParseDuration(timeout)
	if err != nil {
		return "", "", fmt.Errorf("parsing timeout %q: %w", timeout, err)
	}
	pollingDuration, err := time.ParseDuration(polling)
	if err != nil {
		return "", "", fmt.Errorf("parsing polling interval %q: %w", polling, err)
	}
	if pollingDuration <= 0 {
		return "", "", fmt.Errorf("polling interval must be > 0, got %s", polling)
	}

	deadline := time.Now().Add(timeoutDuration)
	var lastErr error
	for {
		currentSerial, currentNotAfter, fetchErr := getDeviceCertInfo(harness, deviceID)
		if fetchErr == nil {
			serial = currentSerial
			notAfter = currentNotAfter
			if stop(currentSerial, currentNotAfter) {
				return serial, notAfter, nil
			}
		} else {
			lastErr = fetchErr
		}

		if time.Now().After(deadline) {
			if lastErr != nil {
				return "", "", fmt.Errorf("timed out waiting for cert info for device %s: %w", deviceID, lastErr)
			}
			return "", "", fmt.Errorf("timed out waiting for cert info condition for device %s", deviceID)
		}
		time.Sleep(pollingDuration)
	}
}

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

// backupCertFile copies the active management certificate to a backup path on the VM.
func backupCertFile(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("nil harness")
	}
	return harness.CopyAgentFile(agentCertPath, agentCertBackupPath)
}

// restoreCertFile restores the management certificate from the backup path on the VM.
func restoreCertFile(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("nil harness")
	}
	return harness.CopyAgentFile(agentCertBackupPath, agentCertPath)
}

// corruptCertFile overwrites the management certificate with invalid content.
func corruptCertFile(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("nil harness")
	}
	return harness.WriteAgentFile(agentCertPath, "invalid-cert-data\n")
}

func submitRenewalCSRWithMismatchedIdentityAtEndpoint(
	harness *e2e.Harness,
	deviceID, apiHost, apiIP, apiPort string,
) (statusCode string, responseBody string, err error) {
	if harness == nil {
		return "", "", fmt.Errorf("nil harness")
	}
	if strings.TrimSpace(deviceID) == "" {
		return "", "", fmt.Errorf("deviceID must not be empty")
	}
	if strings.TrimSpace(apiHost) == "" || strings.TrimSpace(apiIP) == "" || strings.TrimSpace(apiPort) == "" {
		return "", "", fmt.Errorf("API endpoint must not be empty")
	}

	output, err := renderAndRunScriptOnVM(harness, renewalAuthRejectScriptTemplate, map[string]string{
		"MismatchedCN": deviceID + "-mismatch",
		"DeviceID":     deviceID,
		"APIHost":      apiHost,
		"APIIP":        apiIP,
		"APIPort":      apiPort,
	})
	if err != nil {
		return "", "", err
	}

	parts := strings.SplitN(output.String(), "\n__BODY__\n", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("unexpected renewal auth output: %q", output.String())
	}
	statusCode = strings.TrimSpace(parts[0])
	responseBody = strings.TrimSpace(parts[1])
	if statusCode == "" {
		return "", "", fmt.Errorf("empty renewal auth status code in output: %q", output.String())
	}
	return statusCode, responseBody, nil
}

func getCSRListStatusWithManagementCertAtEndpoint(
	harness *e2e.Harness,
	apiHost, apiIP, apiPort string,
) (statusCode, curlRC, stderr string, err error) {
	if harness == nil {
		return "", "", "", fmt.Errorf("nil harness")
	}
	if strings.TrimSpace(apiHost) == "" || strings.TrimSpace(apiIP) == "" || strings.TrimSpace(apiPort) == "" {
		return "", "", "", fmt.Errorf("API endpoint must not be empty")
	}

	output, err := renderAndRunScriptOnVM(harness, csrListStatusScriptTemplate, map[string]string{
		"APIHost": apiHost,
		"APIIP":   apiIP,
		"APIPort": apiPort,
	})
	if err != nil {
		return "", "", "", err
	}

	parts := strings.SplitN(output.String(), "\n__CURL_RC__\n", 2)
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("unexpected CSR list output: %q", output.String())
	}

	rcParts := strings.SplitN(parts[1], "\n__STDERR__\n", 2)
	if len(rcParts) != 2 {
		return "", "", "", fmt.Errorf("unexpected CSR list curl output: %q", output.String())
	}

	return strings.TrimSpace(parts[0]), strings.TrimSpace(rcParts[0]), strings.TrimSpace(rcParts[1]), nil
}

// renderShellScript applies template data to a shell script template.
func renderShellScript(scriptTemplate string, data any) (string, error) {
	tpl, err := template.New("ssh-script").Parse(scriptTemplate)
	if err != nil {
		return "", err
	}

	var script bytes.Buffer
	if err := tpl.Execute(&script, data); err != nil {
		return "", err
	}

	return script.String(), nil
}

func renderAndRunScriptOnVM(harness *e2e.Harness, scriptTemplate string, data any) (*bytes.Buffer, error) {
	command, err := renderShellScript(scriptTemplate, data)
	if err != nil {
		return nil, err
	}
	return harness.RunScriptOnVM(command)
}

func parseHTTPStatusCode(rawStatusCode, context string) (int, error) {
	statusCode := strings.TrimSpace(rawStatusCode)
	if statusCode == "" {
		return 0, fmt.Errorf("empty status code from %s", context)
	}

	statusCodeInt, err := strconv.Atoi(statusCode)
	if err != nil {
		return 0, fmt.Errorf("non-numeric status code %q from %s", statusCode, context)
	}

	return statusCodeInt, nil
}

// getRenewalAttemptSnapshot reads current renewal attempt counters by result label.
func getRenewalAttemptSnapshot(harness *e2e.Harness) (map[string]float64, error) {
	if harness == nil {
		return nil, fmt.Errorf("nil harness")
	}
	results := map[string]float64{}
	for result, metric := range map[string]string{
		"success": metricRenewalAttemptsSuccess,
		"failure": metricRenewalAttemptsFailure,
		"pending": metricRenewalAttemptsPending,
	} {
		rawValue, err := harness.GetAgentMetricValue(metric)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(rawValue) == "" {
			results[result] = 0
			continue
		}

		value, parseErr := strconv.ParseFloat(strings.TrimSpace(rawValue), 64)
		if parseErr != nil {
			return nil, fmt.Errorf("parsing renewal metric for result=%s: %w", result, parseErr)
		}
		results[result] = value
	}
	return results, nil
}

// ensureImmutableBitSupported verifies immutable file attributes are supported for the cert file.
func ensureImmutableBitSupported(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("nil harness")
	}
	output, err := harness.VM.RunSSH([]string{"bash", "-lc", "command -v chattr >/dev/null && echo yes || echo no"}, nil)
	if err != nil {
		return fmt.Errorf("checking chattr availability: %w", err)
	}
	if strings.TrimSpace(output.String()) != "yes" {
		return fmt.Errorf("%w: chattr is not available on the test VM", errImmutableBitUnsupported)
	}

	_, err = harness.VM.RunSSH([]string{"sudo", "chattr", "+i", agentCertPath}, nil)
	if err != nil {
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "operation not supported") ||
			strings.Contains(errMsg, "operation not permitted") ||
			strings.Contains(errMsg, "not supported") ||
			strings.Contains(errMsg, "inappropriate ioctl") {
			return fmt.Errorf("%w: immutable bit is not supported for %s on this test VM: %v", errImmutableBitUnsupported, agentCertPath, err)
		}
		return fmt.Errorf("unable to persist immutable bit on %s in this environment: %w", agentCertPath, err)
	}

	return clearImmutableCertFlag(harness)
}

// makeCertImmutable sets the immutable bit on the management certificate file.
func makeCertImmutable(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("nil harness")
	}
	_, err := harness.VM.RunSSH([]string{"sudo", "chattr", "+i", agentCertPath}, nil)
	if err != nil {
		return fmt.Errorf("failed to make cert immutable: %w", err)
	}
	return nil
}

// clearImmutableCertFlag clears the immutable bit on the management certificate file.
func clearImmutableCertFlag(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("nil harness")
	}
	_, err := harness.VM.RunSSH([]string{"sudo", "chattr", "-i", agentCertPath}, nil)
	if err != nil {
		return fmt.Errorf("clearing immutable bit on %s: %w", agentCertPath, err)
	}
	return nil
}
