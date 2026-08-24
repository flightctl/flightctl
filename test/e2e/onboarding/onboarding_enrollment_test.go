//go:build linux

package onboarding_test

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// EDM-4203 — Enrollment & Completion Flow (Polarion OCP-90423/90421/90425/
// 90461/90430/90463/90432).
//
// These specs drive the Cockpit onboarding wizard through the Flight Control
// enrollment path and assert the completion/rollback behaviour of the packaged
// flightctl-onboarding service. They reuse the EDM-4199 harness helpers
// (startBrowserSession, newLoggedInBrowser, expectCompletionMarker,
// saveScreenshotOnFailure) and the pre-onboarding VM snapshot from the suite
// setup.
//
// Environment-agnostic by design (runs on ACM / E2E_ENVIRONMENT=ocp and on
// quadlets): the Flight Control API endpoint is always taken from
// harness.ApiEndpoint() (the API_ENDPOINT env var computed by run_e2e_tests.sh)
// and the enrollment bearer token from login.LoginToEnvAsAdmin — never
// hardcoded. The enrollment token is a secret and is only ever typed into the
// wizard field via chromedp; it is never logged, echoed, or written to disk by
// these tests.
//
// Runtime prerequisites (documented, not enforced here):
//   - Specs that actually enroll (E1, E4) require the API endpoint to be
//     reachable from inside the single-NIC SLIRP guest (via the 10.0.2.2 NAT
//     gateway) and the endpoint to present a certificate the wizard accepts with
//     TLS verification disabled (WizardSetTLSInsecure).
//   - The suite deliberately does not run setup-network.sh, so the VM has no
//     flightctl-onboarding-ethernet *setup* profile; on a single-NIC rollback
//     (E6) SSH/cockpit survival relies on the base SLIRP connection restoring
//     10.0.2.15 via DHCP.

const (
	// existingCertSentinel is a recognizable, non-secret base64 blob written into
	// a pre-seeded /etc/flightctl/config.yaml for the "use existing" specs (E2,
	// E3). It stands in for client-certificate-data so read-flightctl-config.sh
	// detects an already-provisioned agent config; it is not a real certificate.
	existingCertSentinel = "ZTJlLWV4aXN0aW5nLWNlcnQ=" // base64("e2e-existing-cert")

	// onboardingScriptDir is the only directory apply-and-enroll.sh will run
	// enrollment scripts from (its ALLOWED_SCRIPT_DIRS allowlist). The wizard's
	// generated enrollment script therefore always resolves to a file here, which
	// is what E7 replaces with a controllable mock.
	onboardingScriptDir = "/usr/share/cockpit/system-onboarding/system-onboarding.d"

	// enrollMockRemotePath is where E7 stages the mock enrollment script before
	// bind-mounting it (root-owned, 0755) over the real enrollment script(s).
	enrollMockRemotePath = "/tmp/e2e-enroll-mock.sh"

	// enrollSentinelPath records that the E7 mock ran and what it received; it
	// never contains secret values. enrollExitCodePath lets the test flip the
	// mock's exit code between the failure and success attempts.
	enrollSentinelPath = "/var/tmp/e2e-enroll-invoked"
	enrollExitCodePath = "/var/tmp/e2e-enroll-exitcode"
)

// enrollMockScript is a stand-in third-party enrollment script for E7. It records
// that it was invoked and that it received a non-empty credential params file
// (WITHOUT writing any secret values), then exits with a test-controlled code so
// the wizard's exit-code handling can be asserted.
const enrollMockScript = `#!/bin/bash
# E2E third-party enrollment mock (EDM-4203 E7). Records invocation + credential
# receipt without logging any secret values, then exits with a test-controlled
# code read from ` + enrollExitCodePath + `.
set -u
PARAMS="${1:-}"
SENTINEL=` + enrollSentinelPath + `
EXITCODE_FILE=` + enrollExitCodePath + `
code=0
if [ -f "$EXITCODE_FILE" ]; then
    code=$(tr -cd '0-9' < "$EXITCODE_FILE")
    [ -n "$code" ] || code=0
fi
params_existed=false
params_nonempty=false
creds_present=false
endpoint_present=false
if [ -n "$PARAMS" ] && [ -f "$PARAMS" ]; then
    params_existed=true
    [ -s "$PARAMS" ] && params_nonempty=true
    grep -qiE 'token|credential' "$PARAMS" && creds_present=true
    grep -qiE 'endpoint|server|url' "$PARAMS" && endpoint_present=true
fi
{
    echo "invoked=true"
    echo "params_file_existed=${params_existed}"
    echo "params_file_nonempty=${params_nonempty}"
    echo "creds_present=${creds_present}"
    echo "endpoint_present=${endpoint_present}"
    echo "exit_code=${code}"
} > "$SENTINEL"
if [ "$code" -ne 0 ]; then
    echo "ERROR: mock enrollment failed (test-controlled exit ${code})" >&2
    exit "$code"
fi
echo "OK: mock enrollment succeeded"
exit 0
`

// seedExistingAgentConfig pre-provisions /etc/flightctl/config.yaml with a
// client-certificate-data line so the wizard's read-flightctl-config.sh detects
// an already-enrolled device and offers the "use existing" enrollment path. This
// is a legitimate test precondition (staging device state), not wizard-driven
// configuration. Returns nothing; the sentinel value is the package constant.
func seedExistingAgentConfig(h *e2e.Harness) {
	GinkgoHelper()
	// Minimal but structurally valid agent config. The indented
	// client-certificate-data line is what read-flightctl-config.sh greps for; the
	// .invalid server keeps the enrollment-reachability host unroutable so the
	// connectivity check cannot accidentally depend on it.
	cfg := fmt.Sprintf(`apiVersion: v1
kind: Config
enrollment-service:
  service:
    server: https://onboarding-e2e.invalid:7443
    certificate-authority-data: %s
  authentication:
    client-certificate-data: %s
`, existingCertSentinel, existingCertSentinel)

	_, err := h.VM.RunSSH([]string{"sudo", "mkdir", "-p", "/etc/flightctl"}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to create /etc/flightctl")
	// Redirect tee's stdout to /dev/null so the config (harmless here, but this is
	// the same path real certs would take) is not folded into any error output.
	_, err = h.VM.RunSSH(
		[]string{"sudo", "tee", "/etc/flightctl/config.yaml", ">", "/dev/null"},
		bytes.NewBufferString(cfg),
	)
	Expect(err).ToNot(HaveOccurred(), "failed to seed /etc/flightctl/config.yaml")
}

// onboardingNMProfiles returns the names of all flightctl-onboarding-* NM
// connection profiles currently defined. Unlike getOnboardingNMProfileOfType it
// does not fail when none exist, so it can assert that a rollback removed them.
func onboardingNMProfiles(h *e2e.Harness) []string {
	GinkgoHelper()
	out, err := h.VM.RunSSH([]string{"nmcli", "-t", "-f", "NAME", "con", "show"}, nil)
	Expect(err).ToNot(HaveOccurred(), "nmcli con show failed")
	var profiles []string
	for _, line := range strings.Split(out.String(), "\n") {
		name := strings.TrimSpace(line)
		if strings.HasPrefix(name, "flightctl-onboarding-") {
			profiles = append(profiles, name)
		}
	}
	return profiles
}

// navigateToEnrollmentStep advances the wizard from the (default) Network step to
// the Enrollment step, selecting the first NIC on the way. The caller lands on
// the Enrollment step ready to enable/configure enrollment.
func navigateToEnrollmentStep(browser *e2e.OnboardingBrowser) {
	GinkgoHelper()
	Expect(browser.WizardSelectNIC()).To(Succeed())
	Expect(browser.WizardClickNext()).To(Succeed()) // Network → Network Services
	Expect(browser.WizardClickNext()).To(Succeed()) // Network Services → Enrollment
}

// enrollmentReview advances from the Enrollment step to Review, sets a loopback
// connectivity host, and marks the connectivity test not required so an
// unreachable host degrades to a warning instead of failing the apply. The
// enrollment step itself (login + credential provisioning) remains the real gate.
func enrollmentReview(browser *e2e.OnboardingBrowser) {
	GinkgoHelper()
	Expect(browser.WizardClickNext()).To(Succeed()) // Enrollment → Labels
	Expect(browser.WizardClickNext()).To(Succeed()) // Labels → Review
	Expect(browser.WizardSetConnectivityHost("127.0.0.1")).To(Succeed())
	Expect(browser.WizardSetConnectivityRequired(false)).To(Succeed())
}

var _ = Describe("Onboarding enrollment and completion flow", func() {

	It("When enrollment is configured it should enroll the device and create an enrollment request", Label("90423"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		defer cleanup()
		// Runs before cleanup (LIFO) so the browser is still alive for WizardDebugState.
		defer func() {
			if CurrentSpecReport().Failed() {
				dumpEnrollmentDiagnostics(harness, browser)
			}
		}()

		endpoint := harness.ApiEndpoint()
		token, _, err := login.LoginToEnvAsAdmin(harness)
		Expect(err).ToNot(HaveOccurred(), "failed to obtain admin token for enrollment")
		Expect(token).ToNot(BeEmpty(), "admin token must not be empty")

		By("Configuring the Flight Control enrollment endpoint and token")
		navigateToEnrollmentStep(browser)
		Expect(browser.WizardConfigureEnrollment(endpoint, token)).To(Succeed())
		// The test deployment presents a certificate the guest does not trust; the
		// generated `flightctl login` must run with verification disabled to reach
		// the API and mint an enrollment certificate.
		Expect(browser.WizardSetTLSInsecure()).To(Succeed())

		By("Setting a hostname and applying")
		Expect(browser.WizardClickNext()).To(Succeed()) // Enrollment → Labels
		Expect(browser.WizardSetHostname("enroll-happy-path")).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // Labels → Review
		Expect(browser.WizardSetConnectivityHost("127.0.0.1")).To(Succeed())
		Expect(browser.WizardSetConnectivityRequired(false)).To(Succeed())
		Expect(browser.WizardClickApply()).To(Succeed())
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())

		By("Verifying the onboarding completion marker exists")
		expectCompletionMarker(harness)

		By("Verifying the agent enrolled: enrollment request appears, is approved, and the device comes online")
		// cleanup-onboarding.sh (run by apply-and-enroll.sh) starts flightctl-agent,
		// which reads the wizard-written /etc/flightctl/config.yaml and creates an
		// EnrollmentRequest. EnrollAndWaitForOnlineStatus reads the enrollment ID
		// from the in-VM agent journal, waits for the request, approves it, and
		// waits for the device to report online.
		deviceID, device := harness.EnrollAndWaitForOnlineStatus()
		Expect(deviceID).ToNot(BeEmpty(), "enrollment ID should be present in agent logs")
		Expect(device).ToNot(BeNil(), "device should be online after approval")
	})

	It("When an agent certificate is already provisioned it should skip credential provisioning", Label("90421"), func() {
		harness := e2e.GetWorkerHarness()

		By("Pre-provisioning an agent config with a client certificate")
		seedExistingAgentConfig(harness)

		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Enabling enrollment and reaching the Enrollment step")
		navigateToEnrollmentStep(browser)
		Expect(browser.WizardEnableEnrollment()).To(Succeed())

		By("Verifying the wizard auto-selected 'use existing' and hid the credential fields")
		Eventually(func() (bool, error) {
			return browser.WizardEnrollmentUsesExisting()
		}, 10*time.Second, 500*time.Millisecond).Should(BeTrue(),
			"wizard should auto-select the existing-certificate path when a client cert is present")

		credVisible, err := browser.WizardEnrollmentCredentialFieldVisible()
		Expect(err).ToNot(HaveOccurred())
		Expect(credVisible).To(BeFalse(),
			"credential-provisioning fields should be hidden on the use-existing path")

		By("Applying and completing without provisioning new credentials")
		enrollmentReview(browser)
		Expect(browser.WizardClickApply()).To(Succeed())
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())
		expectCompletionMarker(harness)

		By("Verifying the pre-provisioned config was not overwritten by a new certificate")
		// The use-existing path runs no enrollment script, so nothing performs a
		// `flightctl login` / certificate request; the seeded sentinel must survive,
		// proving credential provisioning was skipped.
		out, err := harness.VM.RunSSH([]string{"sudo", "cat", "/etc/flightctl/config.yaml"}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring(existingCertSentinel),
			"existing agent config should be left intact (no new credentials provisioned)")
	})

	It("When an agent certificate is already provisioned it should only perform connectivity verification", Label("90425"), func() {
		harness := e2e.GetWorkerHarness()

		By("Pre-provisioning an agent config with a client certificate")
		seedExistingAgentConfig(harness)

		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Enabling enrollment and confirming the connectivity-only (use-existing) path")
		navigateToEnrollmentStep(browser)
		Expect(browser.WizardEnableEnrollment()).To(Succeed())
		Eventually(func() (bool, error) {
			return browser.WizardEnrollmentUsesExisting()
		}, 10*time.Second, 500*time.Millisecond).Should(BeTrue(),
			"wizard should use the existing certificate rather than provisioning new credentials")

		By("Applying: only connectivity verification runs, then onboarding completes")
		// With no credential provisioning to do, the remaining enrollment work is the
		// connectivity check performed by apply-and-enroll.sh. A reachable loopback
		// host (not required) lets that verification run and complete.
		enrollmentReview(browser)
		Expect(browser.WizardClickApply()).To(Succeed())
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())
		expectCompletionMarker(harness)

		By("Verifying no new credentials were provisioned")
		out, err := harness.VM.RunSSH([]string{"sudo", "cat", "/etc/flightctl/config.yaml"}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring(existingCertSentinel),
			"existing agent config should be left intact; only connectivity was verified")
	})

	It("When enrollment fails it should allow correcting credentials and re-applying", Label("90461"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		defer cleanup()
		// Runs before cleanup (LIFO) so the browser is still alive for WizardDebugState.
		defer func() {
			if CurrentSpecReport().Failed() {
				dumpEnrollmentDiagnostics(harness, browser)
			}
		}()

		endpoint := harness.ApiEndpoint()
		token, _, err := login.LoginToEnvAsAdmin(harness)
		Expect(err).ToNot(HaveOccurred(), "failed to obtain admin token for enrollment")
		Expect(token).ToNot(BeEmpty())

		By("First attempt: configuring enrollment with an invalid token")
		navigateToEnrollmentStep(browser)
		// An invalid bearer token makes `flightctl login` inside the enrollment
		// script fail, so the enrollment script exits non-zero and the apply fails.
		Expect(browser.WizardConfigureEnrollment(endpoint, "invalid-e2e-enrollment-token")).To(Succeed())
		Expect(browser.WizardSetTLSInsecure()).To(Succeed())
		enrollmentReview(browser)
		Expect(browser.WizardClickApply()).To(Succeed())

		By("Verifying the first attempt surfaces a failure")
		Expect(browser.WizardWaitForFailure(wizardTimeout)).To(Succeed())

		By("Verifying onboarding did not complete on the failed attempt")
		_, err = harness.VM.RunSSH([]string{
			"sudo", "test", "!", "-f", "/var/lib/flightctl-onboarding/.onboarding-complete",
		}, nil)
		Expect(err).ToNot(HaveOccurred(), "completion marker must not exist after a failed enrollment")

		By("Navigating back to the Enrollment step and supplying a valid token")
		// Progress → Review → Labels → Enrollment = 3 Back clicks.
		Expect(browser.WizardNavigateBackwards(3)).To(Succeed())
		Expect(browser.WizardConfigureEnrollment(endpoint, token)).To(Succeed())
		Expect(browser.WizardSetTLSInsecure()).To(Succeed())

		By("Re-applying and completing successfully")
		enrollmentReview(browser)
		Expect(browser.WizardClickApply()).To(Succeed())
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())
		expectCompletionMarker(harness)

		By("Verifying the corrected attempt enrolled the device")
		deviceID, device := harness.EnrollAndWaitForOnlineStatus()
		Expect(deviceID).ToNot(BeEmpty())
		Expect(device).ToNot(BeNil())
	})

	It("When single-NIC apply drops the browser it should complete via systemd-run", Label("90430"), func() {
		harness := e2e.GetWorkerHarness()

		// This spec manages its own tunnel/browser so it can drop the browser
		// (simulating the operator's control channel going away) right after Apply
		// and then verify completion purely over SSH.
		//
		// It also needs the wizard's single-NIC path, where apply is delegated to a
		// systemd-run transient unit that survives the browser going away. That path
		// only fires when the plugin's isConnectedViaInterface() returns true, which
		// requires the browser to reach Cockpit via the guest's real interface
		// address (not loopback) — the default StartCockpitTunnel forwards to the
		// guest's localhost, which makes the wizard treat this as a multi-NIC inline
		// apply that dies with the browser. StartCockpitTunnelViaInterface forwards to
		// the guest's eth0 (10.0.2.15) and navigates via 127.0.0.2 so hostname is not
		// localhost. See StartCockpitTunnelViaInterface for the full rationale.
		workerID := GinkgoParallelProcess()
		sshPort := sshPortBase + workerID
		cockpitAddr, tunnelCleanup, err := e2e.StartCockpitTunnelViaInterface(sshPort, vmUser, vmPassword, slirpStaticIP)
		Expect(err).ToNot(HaveOccurred(), "failed to start Cockpit SSH tunnel")
		defer tunnelCleanup()

		browser := newLoggedInBrowser(cockpitAddr)
		defer saveScreenshotOnFailure(browser, "single-nic")

		By("Driving a minimal apply on the single-NIC VM")
		// On this single-NIC VM every apply is delegated to apply-and-enroll.sh under
		// a systemd-run transient unit (run-apply-enroll.sh), which survives the
		// browser/cockpit-bridge going away. Enrollment is not needed to exercise
		// that hand-off, so keep the flow minimal.
		Expect(browser.WizardSelectNIC()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Network Services
		Expect(browser.WizardClickNext()).To(Succeed()) // → Enrollment
		Expect(browser.WizardDisableEnrollment()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Labels
		Expect(browser.WizardSetHostname("single-nic-complete")).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Review
		Expect(browser.WizardSetConnectivityHost("127.0.0.1")).To(Succeed())
		Expect(browser.WizardSetConnectivityRequired(false)).To(Succeed())
		Expect(browser.WizardClickApply()).To(Succeed())

		By("Dropping the browser control channel immediately after Apply")
		// The transient unit is already launched by the time Apply returns, so
		// closing Chrome here cannot affect it — completion must proceed in the
		// background.
		browser.Close()

		By("Verifying onboarding completed in the background transient unit")
		expectCompletionMarker(harness)
		// The apply log is written only by apply-and-enroll.sh (the delegated unit),
		// so its completion line proves the systemd-run hand-off ran to completion
		// after the browser was gone.
		Eventually(func() (string, error) {
			out, err := harness.VM.RunSSH([]string{
				"sudo", "cat", "/var/log/flightctl-onboarding-apply.log",
			}, nil)
			if err != nil {
				return "", err
			}
			return out.String(), nil
		}, 60*time.Second, 2*time.Second).Should(ContainSubstring("Onboarding completed successfully"),
			"delegated apply unit should have completed after the browser disconnected")

		By("Verifying the applied hostname took effect")
		out, err := harness.VM.RunSSH([]string{"hostnamectl", "hostname"}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.TrimSpace(out.String())).To(Equal("single-nic-complete"))
	})

	It("When enrollment fails after network activation it should roll back and keep the wizard reachable", Label("90463"), func() {
		harness := e2e.GetWorkerHarness()
		endpoint := harness.ApiEndpoint()

		browser, cleanup := startBrowserSession()
		defer cleanup()
		// Runs before cleanup (LIFO) so the browser is still alive for WizardDebugState.
		defer func() {
			if CurrentSpecReport().Failed() {
				dumpEnrollmentDiagnostics(harness, browser)
			}
		}()

		By("Configuring enrollment with an invalid token to force a failure after network activation")
		navigateToEnrollmentStep(browser)
		Expect(browser.WizardConfigureEnrollment(endpoint, "invalid-e2e-enrollment-token")).To(Succeed())
		Expect(browser.WizardSetTLSInsecure()).To(Succeed())
		enrollmentReview(browser)
		Expect(browser.WizardClickApply()).To(Succeed())

		By("Verifying the apply fails")
		Expect(browser.WizardWaitForFailure(wizardTimeout)).To(Succeed())

		By("Verifying the network configuration was rolled back")
		// apply-and-enroll.sh activates the flightctl-onboarding NM profile before
		// running enrollment; on enrollment failure its EXIT trap runs rollback,
		// which deletes the applied profile and restores the setup network. Poll,
		// since rollback runs in the background transient unit.
		Eventually(func() []string {
			return onboardingNMProfiles(harness)
		}, 60*time.Second, 2*time.Second).Should(BeEmpty(),
			"flightctl-onboarding NM profile should be removed by rollback after enrollment failure")

		By("Verifying onboarding did not complete")
		_, err := harness.VM.RunSSH([]string{
			"sudo", "test", "!", "-f", "/var/lib/flightctl-onboarding/.onboarding-complete",
		}, nil)
		Expect(err).ToNot(HaveOccurred(), "completion marker must not exist after a rolled-back apply")

		By("Verifying cockpit is still listening after the rollback")
		Eventually(func() (string, error) {
			out, err := harness.VM.RunSSH([]string{"sudo", "ss", "-tlnH", "sport", "=", ":9090"}, nil)
			if err != nil {
				return "", err
			}
			return out.String(), nil
		}, 30*time.Second, 2*time.Second).Should(ContainSubstring(":9090"),
			"cockpit should remain reachable after network rollback")

		By("Verifying the wizard is reachable again (not stuck in a completed state)")
		browser2, cleanup2 := startBrowserSession()
		defer cleanup2()
		alreadyComplete, err := browser2.WizardIsAlreadyComplete()
		Expect(err).ToNot(HaveOccurred())
		Expect(alreadyComplete).To(BeFalse(),
			"wizard should be reachable for a retry after rollback, not marked complete")
	})

	It("When a third-party enrollment script is configured it should receive credentials and surface its exit code", Label("90432"), func() {
		harness := e2e.GetWorkerHarness()
		endpoint := harness.ApiEndpoint()
		token, _, err := login.LoginToEnvAsAdmin(harness)
		Expect(err).ToNot(HaveOccurred(), "failed to obtain admin token for enrollment")
		Expect(token).ToNot(BeEmpty())

		By("Installing a controllable mock over the enrollment script(s)")
		installEnrollmentMock(harness)

		// Start with a failing exit code to assert the wizard surfaces it.
		setEnrollMockExitCode(harness, 3)

		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Configuring enrollment so the wizard generates a credential params file for the script")
		navigateToEnrollmentStep(browser)
		Expect(browser.WizardConfigureEnrollment(endpoint, token)).To(Succeed())
		Expect(browser.WizardSetTLSInsecure()).To(Succeed())
		enrollmentReview(browser)
		Expect(browser.WizardClickApply()).To(Succeed())

		By("Verifying the wizard reflects the non-zero exit code as a failure")
		Expect(browser.WizardWaitForFailure(wizardTimeout)).To(Succeed())

		By("Verifying the script was invoked with a non-empty credential params file")
		sentinel := readEnrollSentinel(harness)
		Expect(sentinel).To(ContainSubstring("invoked=true"))
		Expect(sentinel).To(ContainSubstring("params_file_existed=true"))
		Expect(sentinel).To(ContainSubstring("params_file_nonempty=true"),
			"enrollment script should receive a non-empty credential params file")
		Expect(sentinel).To(ContainSubstring("creds_present=true"),
			"credential params file should contain credential material")
		Expect(sentinel).To(ContainSubstring("exit_code=3"))

		By("Switching the mock to succeed and re-applying")
		setEnrollMockExitCode(harness, 0)
		// Progress → Review = 1 Back click; the enrollment configuration is retained.
		Expect(browser.WizardNavigateBackwards(1)).To(Succeed())
		Expect(browser.WizardClickApply()).To(Succeed())

		By("Verifying the zero exit code lets onboarding complete")
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())
		expectCompletionMarker(harness)

		sentinel = readEnrollSentinel(harness)
		Expect(sentinel).To(ContainSubstring("invoked=true"))
		Expect(sentinel).To(ContainSubstring("exit_code=0"))
	})
})

// installEnrollmentMock stages enrollMockScript as a root-owned, 0755 file and
// bind-mounts it over every enrollment script in onboardingScriptDir. The wizard's
// generated enrollment script must live in that allowlisted directory, so
// replacing the scripts there guarantees the wizard invokes the mock regardless
// of the real script's filename.
//
// A bind mount is used rather than `install`/`cp` because the packaged scripts
// live under /usr, which is read-only in bootc image mode — overwriting the file
// in place fails with EROFS. Bind-mounting is a mount-namespace operation, not a
// write to the underlying filesystem, so it works over a read-only /usr; the
// delegated apply runs in the host mount namespace (no PrivateMounts), so it sees
// the mount. The mount source must be root-owned because apply-and-enroll.sh's
// validate_script_path rejects any script whose realpath is not owned by uid 0
// (`sudo tee` writes the staged file as root). All mounts are discarded when the
// suite reverts the VM to its pre-onboarding snapshot between specs, so no
// explicit unmount is needed.
func installEnrollmentMock(h *e2e.Harness) {
	GinkgoHelper()
	// sudo tee → the staged mock is owned by root (uid 0), which
	// validate_script_path requires; chmod makes it executable for the bind target.
	_, err := h.VM.RunSSH(
		[]string{"sudo", "tee", enrollMockRemotePath, ">", "/dev/null"},
		bytes.NewBufferString(enrollMockScript),
	)
	Expect(err).ToNot(HaveOccurred(), "failed to stage enrollment mock script")
	_, err = h.VM.RunSSH([]string{"sudo", "chmod", "0755", enrollMockRemotePath}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to make enrollment mock executable")

	out, err := h.VM.RunSSH([]string{"sudo", "ls", onboardingScriptDir}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to list enrollment script dir")

	safeName := regexp.MustCompile(`^[A-Za-z0-9._-]+\.sh$`)
	var scripts []string
	for _, line := range strings.Split(out.String(), "\n") {
		name := strings.TrimSpace(line)
		if safeName.MatchString(name) {
			scripts = append(scripts, name)
		}
	}
	Expect(scripts).ToNot(BeEmpty(),
		fmt.Sprintf("expected at least one enrollment script in %s to replace with the mock", onboardingScriptDir))

	for _, name := range scripts {
		_, err := h.VM.RunSSH([]string{
			"sudo", "mount", "--bind",
			enrollMockRemotePath, onboardingScriptDir + "/" + name,
		}, nil)
		Expect(err).ToNot(HaveOccurred(), "failed to bind-mount mock over "+name)
	}
}

// dumpEnrollmentDiagnostics captures wizard + guest state to the Ginkgo output
// when an enrollment spec fails, so a CI run reveals the real reason instead of a
// bare "timed out". It is strictly best-effort: every step tolerates its own
// failure (the browser context may already be unwinding, and any given file/unit
// may not exist), and it never asserts. Call it via a deferred guard that runs
// while the browser is still alive:
//
//	defer func() {
//	    if CurrentSpecReport().Failed() {
//	        dumpEnrollmentDiagnostics(harness, browser)
//	    }
//	}()
//
// It reads only non-secret diagnostics: the wizard's DOM state, the delegated
// apply log, the watchdog-status file, and the agent journal. It deliberately
// does not dump /etc/flightctl/config.yaml, which can carry certificate material.
func dumpEnrollmentDiagnostics(h *e2e.Harness, browser *e2e.OnboardingBrowser) {
	GinkgoWriter.Println("=== enrollment failure diagnostics ===")

	if browser != nil {
		GinkgoWriter.Printf("wizard state: %s\n", browser.WizardDebugState())
		if txt, err := browser.WizardGetReviewText(); err == nil && strings.TrimSpace(txt) != "" {
			GinkgoWriter.Printf("wizard page text:\n%s\n", txt)
		}
	}

	// Each entry is a label plus a plain argv (no shell metacharacters, so
	// RunSSH's space-joining is safe). Paths use the OLD flightctl-onboarding
	// naming the CI VM still ships (see the onboarding RPM naming note).
	diagnostics := []struct {
		label string
		argv  []string
	}{
		{"apply log", []string{"sudo", "cat", "/var/log/flightctl-onboarding-apply.log"}},
		{"watchdog status", []string{"sudo", "cat", "/var/lib/flightctl-onboarding/.watchdog-status"}},
		{"agent journal", []string{"sudo", "journalctl", "-u", "flightctl-agent", "--no-pager", "-n", "150"}},
		{"agent unit state", []string{"systemctl", "is-active", "flightctl-agent"}},
	}
	for _, d := range diagnostics {
		out, err := h.VM.RunSSH(d.argv, nil)
		if err != nil {
			GinkgoWriter.Printf("%s: unavailable (%v)\n", d.label, err)
			continue
		}
		GinkgoWriter.Printf("%s:\n%s\n", d.label, out.String())
	}
	GinkgoWriter.Println("=== end diagnostics ===")
}

// setEnrollMockExitCode writes the exit code the E7 mock will return next.
func setEnrollMockExitCode(h *e2e.Harness, code int) {
	GinkgoHelper()
	_, err := h.VM.RunSSH(
		[]string{"sudo", "tee", enrollExitCodePath, ">", "/dev/null"},
		bytes.NewBufferString(fmt.Sprintf("%d", code)),
	)
	Expect(err).ToNot(HaveOccurred(), "failed to set enrollment mock exit code")
}

// readEnrollSentinel returns the E7 mock's record of its last invocation. It
// polls because the enrollment script runs in the background transient unit, so
// the sentinel may not be written the instant the wizard reports a result.
func readEnrollSentinel(h *e2e.Harness) string {
	GinkgoHelper()
	var content string
	Eventually(func() (string, error) {
		out, err := h.VM.RunSSH([]string{"sudo", "cat", enrollSentinelPath}, nil)
		if err != nil {
			return "", err
		}
		content = out.String()
		return content, nil
	}, 30*time.Second, 2*time.Second).Should(ContainSubstring("invoked=true"),
		"enrollment mock sentinel should record an invocation")
	return content
}
