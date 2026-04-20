package agent_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	bootcTimerUnit                  = "bootc-fetch-apply-updates.timer"
	bootcTimerUnitFile              = "/usr/lib/systemd/system/bootc-fetch-apply-updates.timer"
	alertmanagerProxyPort           = 8443
	alertmanagerProxyService        = "flightctl-alertmanager-proxy"
	alertmanagerAlertsPath          = "/api/v2/alerts"
	alertNameBootcTimerNonCompliant = "DeviceBootcTimerNonCompliant"
)

var _ = Describe("Bootc timer compliance", func() {
	It("should monitor bootc timer compliance and generate events/alerts", Label("bootc-timer", "sanity", "agent"), func() {
		harness := e2e.GetWorkerHarness()
		testProviders := setup.GetDefaultProviders()

		By("Enrolling a device")
		deviceName := harness.StartVMAndEnroll()

		By("Waiting for device to come online")
		_, err := harness.CheckDeviceStatus(deviceName, v1beta1.DeviceSummaryStatusOnline)
		Expect(err).ToNot(HaveOccurred())

		By("Checking if bootc timer file exists on the device")
		stdout, err := harness.VM.RunSSH([]string{"test", "-f", bootcTimerUnitFile, "&&", "echo", "exists", "||", "echo", "not-exists"}, nil)
		Expect(err).ToNot(HaveOccurred())
		timerFileExists := strings.TrimSpace(stdout.String()) == "exists"

		if !timerFileExists {
			Skip("Bootc timer unit file does not exist on this device - skipping bootc timer compliance test")
		}

		By("Verifying bootc timer is masked by default")
		// Check for the actual mask symlink rather than relying on systemctl is-enabled output
		// which may vary depending on how the mask was created (via systemctl vs manual symlink)
		stdout, err = harness.VM.RunSSH([]string{"sh", "-c", "readlink /etc/systemd/system/" + bootcTimerUnit + " 2>/dev/null || echo ''"}, nil)
		Expect(err).ToNot(HaveOccurred())
		symlinkTarget := strings.TrimSpace(stdout.String())
		if symlinkTarget != "/dev/null" {
			Skip("Bootc timer is not masked by default on this system - skipping compliance test")
		}

		By("Verifying BootcTimerCompliant condition is True")
		harness.WaitForDeviceContents(deviceName, "bootc timer should be compliant", func(device *v1beta1.Device) bool {
			cond := v1beta1.FindStatusCondition(device.Status.Conditions, "BootcTimerCompliant")
			if cond == nil {
				return false
			}
			return cond.Status == v1beta1.ConditionStatusTrue && cond.Reason == "Masked"
		}, TIMEOUT)

		By("Unmasking the bootc timer")
		_, err = harness.VM.RunSSH([]string{"sudo", "systemctl", "unmask", bootcTimerUnit}, nil)
		Expect(err).ToNot(HaveOccurred())

		// Ensure timer is re-masked even if test fails
		DeferCleanup(func() {
			_, _ = harness.VM.RunSSH([]string{"sudo", "systemctl", "mask", bootcTimerUnit}, nil)
		})

		By("Verifying timer is unmasked")
		// Check that the mask symlink has been removed
		_, err = harness.VM.RunSSH([]string{"sh", "-c", "test -L /etc/systemd/system/" + bootcTimerUnit}, nil)
		Expect(err).To(HaveOccurred(), "bootc timer mask symlink should not exist after unmasking")

		By("Restarting the agent to trigger re-check")
		_, err = harness.VM.RunSSH([]string{"sudo", "systemctl", "restart", "flightctl-agent"}, nil)
		Expect(err).ToNot(HaveOccurred())

		By("Waiting for agent to restart")
		time.Sleep(10 * time.Second)

		By("Verifying BootcTimerCompliant condition changes to False")
		harness.WaitForDeviceContents(deviceName, "bootc timer should be non-compliant", func(device *v1beta1.Device) bool {
			cond := v1beta1.FindStatusCondition(device.Status.Conditions, "BootcTimerCompliant")
			if cond == nil {
				return false
			}
			return cond.Status == v1beta1.ConditionStatusFalse && cond.Reason == "NotMasked"
		}, TIMEOUT)

		By("Checking for DeviceBootcTimerNonCompliant event")
		Eventually(func() string {
			out, err := harness.RunGetEvents("--field-selector", fmt.Sprintf("involvedObject.name=%s,type=Warning,reason=%s", deviceName, "DeviceBootcTimerNonCompliant"))
			if err != nil {
				return ""
			}
			return out
		}, TIMEOUT, POLLING).Should(ContainSubstring("bootc-fetch-apply-updates.timer is not masked"))

		// Only check alerts if running in a Kubernetes environment
		envType := testProviders.Infra.GetEnvironmentType()
		if envType == "kind" || envType == "ocp" {
			By("Checking for alert in Alertmanager")
			_, err := login.LoginToAPIWithToken(harness)
			Expect(err).ToNot(HaveOccurred())

			orgID, err := harness.GetOrganizationID()
			Expect(err).ToNot(HaveOccurred())

			baseURL, client, cleanup, err := harness.StartServiceAccess(
				alertmanagerProxyService,
				[]string{"flightctl-external", "flightctl", util.E2E_NAMESPACE},
				alertmanagerProxyPort,
				true,
				10*time.Second,
			)
			Expect(err).ToNot(HaveOccurred(), "Alertmanager proxy should be accessible in kind/ocp environment")
			defer cleanup()

			authToken, err := harness.GetClientAccessToken()
			Expect(err).ToNot(HaveOccurred())

			alertsPath := fmt.Sprintf("%s?filter=org_id=%s&filter=alertname=%s&filter=resource=%s",
				alertmanagerAlertsPath, orgID, alertNameBootcTimerNonCompliant, deviceName)

			Eventually(func() bool {
				statusCode, body, err := harness.HTTPGet(client, baseURL, alertsPath, authToken)
				if err != nil || statusCode != http.StatusOK {
					return false
				}

				var alerts []map[string]interface{}
				if err := json.Unmarshal([]byte(body), &alerts); err != nil {
					return false
				}

				// Check for firing alerts
				for _, alert := range alerts {
					if status, ok := alert["status"].(map[string]interface{}); ok {
						if state, ok := status["state"].(string); ok && state == "firing" {
							return true
						}
					}
				}
				return false
			}, TIMEOUT, POLLING).Should(BeTrue(), "DeviceBootcTimerNonCompliant alert should be firing")
		}

		By("Re-masking the bootc timer")
		_, err = harness.VM.RunSSH([]string{"sudo", "systemctl", "mask", bootcTimerUnit}, nil)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying timer is masked again")
		stdout, err = harness.VM.RunSSH([]string{"sh", "-c", "readlink /etc/systemd/system/" + bootcTimerUnit}, nil)
		Expect(err).ToNot(HaveOccurred())
		symlinkTarget = strings.TrimSpace(stdout.String())
		Expect(symlinkTarget).To(Equal("/dev/null"), "bootc timer should be masked (symlink to /dev/null)")

		By("Restarting the agent to trigger re-check")
		_, err = harness.VM.RunSSH([]string{"sudo", "systemctl", "restart", "flightctl-agent"}, nil)
		Expect(err).ToNot(HaveOccurred())

		By("Waiting for agent to restart")
		time.Sleep(10 * time.Second)

		By("Verifying BootcTimerCompliant condition returns to True")
		harness.WaitForDeviceContents(deviceName, "bootc timer should be compliant again", func(device *v1beta1.Device) bool {
			cond := v1beta1.FindStatusCondition(device.Status.Conditions, "BootcTimerCompliant")
			if cond == nil {
				return false
			}
			return cond.Status == v1beta1.ConditionStatusTrue && cond.Reason == "Masked"
		}, TIMEOUT)

		By("Checking for DeviceBootcTimerCompliant event")
		Eventually(func() string {
			out, err := harness.RunGetEvents("--field-selector", fmt.Sprintf("involvedObject.name=%s,type=Normal,reason=%s", deviceName, "DeviceBootcTimerCompliant"))
			if err != nil {
				return ""
			}
			return out
		}, TIMEOUT, POLLING).Should(ContainSubstring("bootc-fetch-apply-updates.timer is properly masked"))

		// Check that alert is resolved
		if envType == "kind" || envType == "ocp" {
			By("Verifying alert is resolved in Alertmanager")
			baseURL, client, cleanup, err := harness.StartServiceAccess(
				alertmanagerProxyService,
				[]string{"flightctl-external", "flightctl", util.E2E_NAMESPACE},
				alertmanagerProxyPort,
				true,
				10*time.Second,
			)
			Expect(err).ToNot(HaveOccurred(), "Alertmanager proxy should be accessible in kind/ocp environment")
			defer cleanup()

			authToken, err := harness.GetClientAccessToken()
			Expect(err).ToNot(HaveOccurred())

			orgID, err := harness.GetOrganizationID()
			Expect(err).ToNot(HaveOccurred())

			alertsPath := fmt.Sprintf("%s?filter=org_id=%s&filter=alertname=%s&filter=resource=%s",
				alertmanagerAlertsPath, orgID, alertNameBootcTimerNonCompliant, deviceName)

			Eventually(func() bool {
				statusCode, body, err := harness.HTTPGet(client, baseURL, alertsPath, authToken)
				if err != nil || statusCode != http.StatusOK {
					return false
				}

				var alerts []map[string]interface{}
				if err := json.Unmarshal([]byte(body), &alerts); err != nil {
					return false
				}

				// Alert should either not exist or be marked as resolved
				if len(alerts) == 0 {
					return true
				}

				// Check if all matching alerts are resolved
				for _, alert := range alerts {
					if labels, ok := alert["labels"].(map[string]interface{}); ok {
						if labels["resource"] == deviceName && labels["alertname"] == alertNameBootcTimerNonCompliant {
							// Alert exists - check if it's resolved
							if status, ok := alert["status"].(map[string]interface{}); ok {
								if state, ok := status["state"].(string); ok && state == "firing" {
									return false // Alert is still firing
								}
							}
						}
					}
				}
				return true
			}, TIMEOUT, POLLING).Should(BeTrue(), "DeviceBootcTimerNonCompliant alert should be resolved")
		}
	})
})
