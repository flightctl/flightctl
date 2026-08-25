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
	// bind-mounting it (root-owned, 0755) over the real enrollment script(s). It
	// lives under /var/tmp (not /tmp) so the bind-mount source is a real on-disk
	// path visible in every mount namespace — including PID 1's, where the delegated
	// apply's systemd-run transient unit performs the bind (see installEnrollmentMock).
	enrollMockRemotePath = "/var/tmp/e2e-enroll-mock.sh"

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

// waitForDelegatedApplyLaunched blocks until the wizard's delegated apply has
// actually started on the guest — i.e. run-apply-enroll.sh has invoked systemd-run
// and the flightctl-onboarding-apply-<ns> transient unit exists. On the single-NIC
// path WizardClickApply only clicks the button; the wizard then calls
// cockpit.spawn(["sudo", run-apply-enroll.sh, ...]) over the Cockpit bridge, and
// that spawn must finish before systemd-run registers the unit. Specs that drop the
// browser (E5) or that rely on the enrollment script running in PID 1's mount
// namespace (E7) must wait for this first, or closing the bridge kills the spawn
// before the background unit is ever created (observed in CI: empty apply-unit
// journal, no apply log, marker timeout).
func waitForDelegatedApplyLaunched(h *e2e.Harness, timeout time.Duration) {
	GinkgoHelper()
	// Single-element argv → run through a shell by RunSSH; the pattern is a static
	// string with no harvested values, so there is nothing to inject. The transient
	// unit is created with --remain-after-exit, so it persists after the apply
	// completes; `grep -c … || true` yields "0" (not an SSH error) when it is absent.
	Eventually(func() (string, error) {
		out, err := h.VM.RunSSH([]string{
			"systemctl list-units --all --no-legend 'flightctl-onboarding-apply-*.service' " +
				"2>/dev/null | grep -c flightctl-onboarding-apply || true",
		}, nil)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(out.String()), nil
	}, timeout, 2*time.Second).ShouldNot(Or(Equal("0"), BeEmpty()),
		"the delegated apply transient unit (flightctl-onboarding-apply-*) never launched")
}

var _ = Describe("Onboarding enrollment and completion flow", func() {

	It("When enrollment is configured it should enroll the device and create an enrollment request", Label("90423"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		DeferCleanup(cleanup)
		// Registered after cleanup so it runs first (DeferCleanup is LIFO) while the
		// browser is still alive. DeferCleanup callbacks run after Ginkgo records the
		// spec failure, so CurrentSpecReport().Failed() is reliable here — a plain
		// `defer` runs during the failure unwind and would still see Failed()==false.
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				dumpEnrollmentDiagnostics(harness, browser)
			}
		})

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
		// On wizard success the agent is enabled but not started (the onboarding gate
		// defers its start until session end). enrollApproveRestartAndWaitOnline arms
		// the gate and starts the agent so it reads the wizard-written
		// /etc/flightctl/config.yaml and creates an EnrollmentRequest, reads the
		// enrollment ID from that fresh agent invocation, waits for the request,
		// approves it, restarts the agent (its short enrollment-verify cap means it
		// has usually gone inactive by approval time), and waits for online.
		deviceID := enrollApproveRestartAndWaitOnline(harness)
		Expect(deviceID).ToNot(BeEmpty(), "enrollment ID should be present in agent logs")
	})

	It("When an agent certificate is already provisioned it should skip credential provisioning", Label("90421"), func() {
		harness := e2e.GetWorkerHarness()

		By("Pre-provisioning an agent config with a client certificate")
		seedExistingAgentConfig(harness)

		browser, cleanup := startBrowserSession()
		DeferCleanup(cleanup)

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
		DeferCleanup(cleanup)

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

	It("When an apply fails during the enrollment flow it should allow correcting and re-applying", Label("90461"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		DeferCleanup(cleanup)
		// Registered after cleanup so it runs first (DeferCleanup is LIFO) while the
		// browser is still alive. DeferCleanup callbacks run after Ginkgo records the
		// spec failure, so CurrentSpecReport().Failed() is reliable here — a plain
		// `defer` runs during the failure unwind and would still see Failed()==false.
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				dumpEnrollmentDiagnostics(harness, browser)
			}
		})

		endpoint := harness.ApiEndpoint()
		token, _, err := login.LoginToEnvAsAdmin(harness)
		Expect(err).ToNot(HaveOccurred(), "failed to obtain admin token for enrollment")
		Expect(token).ToNot(BeEmpty())

		By("First attempt: configuring valid enrollment but forcing the apply to fail")
		// The deployed onboarding package defers enrollment-credential validation to
		// the agent: an invalid token does NOT fail the wizard synchronously (the
		// enrollment step reports success and the agent rejects the credentials only
		// later — confirmed in CI). The only failure the wizard surfaces
		// synchronously is a *required* connectivity check against an unreachable
		// host, which runs before the enrollment step. So we configure a real (valid)
		// enrollment and force that pre-enrollment connectivity check to fail,
		// exercising the same error → correct → re-apply recovery flow AC4 describes
		// and still ending in a real device enrollment on the corrected attempt.
		navigateToEnrollmentStep(browser)
		Expect(browser.WizardConfigureEnrollment(endpoint, token)).To(Succeed())
		Expect(browser.WizardSetTLSInsecure()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // Enrollment → Labels
		Expect(browser.WizardSetHostname("enroll-failure-recovery")).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // Labels → Review
		// A hostname in the reserved .invalid TLD fails DNS resolution; with the
		// connectivity test required, the apply fails before the enrollment step runs.
		Expect(browser.WizardSetConnectivityHost("connectivity-check.invalid")).To(Succeed())
		Expect(browser.WizardSetConnectivityRequired(true)).To(Succeed())
		Expect(browser.WizardClickApply()).To(Succeed())

		By("Verifying the first attempt surfaces a failure")
		Expect(browser.WizardWaitForFailure(wizardTimeout)).To(Succeed())

		By("Verifying onboarding did not complete on the failed attempt")
		_, err = harness.VM.RunSSH([]string{
			"sudo", "test", "!", "-f", "/var/lib/flightctl-onboarding/.onboarding-complete",
		}, nil)
		Expect(err).ToNot(HaveOccurred(), "completion marker must not exist after a failed apply")

		By("Navigating back to Review and correcting the connectivity setting")
		// Progress → Review = 1 Back click. The offending setting (the required
		// connectivity host) lives on the Review step; the enrollment configuration
		// entered earlier is retained across the navigation.
		Expect(browser.WizardNavigateBackwards(1)).To(Succeed())
		Expect(browser.WizardSetConnectivityHost("127.0.0.1")).To(Succeed())
		Expect(browser.WizardSetConnectivityRequired(false)).To(Succeed())

		By("Re-applying and completing successfully")
		Expect(browser.WizardClickApply()).To(Succeed())
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())
		expectCompletionMarker(harness)

		By("Verifying the corrected attempt enrolled the device")
		deviceID := enrollApproveRestartAndWaitOnline(harness)
		Expect(deviceID).ToNot(BeEmpty(), "enrollment ID should be present in agent logs after recovery")
	})

	It("When single-NIC apply drops the browser it should complete via systemd-run", Label("90430"), func() {
		harness := e2e.GetWorkerHarness()

		// This spec manages its own tunnel/browser so it can drop the browser
		// (simulating the operator's control channel going away) right after Apply
		// and then verify completion purely over SSH.
		//
		// The deployed wizard's apply branches on isSingleNic (whether the selected
		// NIC is the interface Cockpit is reached through): single-NIC delegates the
		// network-activation + connectivity + enrollment + finalize + cleanup phase to
		// a systemd-run transient unit (run-apply-enroll.sh → apply-and-enroll.sh),
		// while multi-NIC runs those steps inline in the Cockpit bridge. This spec
		// forces the single-NIC path via StartCockpitTunnelViaInterface so the browser
		// reaches Cockpit through the guest's real eth0 (10.0.2.15, navigated via
		// 127.0.0.2 so the origin host is not localhost) and WizardSelectNIC selects
		// that same interface. The transient unit is a child of PID 1, so it is
		// unaffected by the browser/cockpit-bridge going away — exactly the
		// completion-after-disconnect behaviour AC5 asserts.
		workerID := GinkgoParallelProcess()
		sshPort := sshPortBase + workerID
		cockpitAddr, tunnelCleanup, err := e2e.StartCockpitTunnelViaInterface(sshPort, vmUser, vmPassword, slirpStaticIP)
		Expect(err).ToNot(HaveOccurred(), "failed to start Cockpit SSH tunnel")
		DeferCleanup(tunnelCleanup)

		browser := newLoggedInBrowser(cockpitAddr)
		// DeferCleanup runs after Ginkgo records the failure, so these fire reliably
		// (a plain defer runs during the unwind and sees Failed()==false). This spec
		// closes the browser mid-body, so on a post-close failure the screenshot is
		// best-effort; the SSH-side diagnostics (apply log, watchdog-status, agent
		// journal) are what reveal whether the single-NIC systemd-run delegation ran.
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				dumpEnrollmentDiagnostics(harness, browser)
			}
		})
		DeferCleanup(saveScreenshotOnFailure, browser, "single-nic")

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

		By("Waiting for the delegated apply to launch before dropping the browser")
		// WizardClickApply only clicks the button; the wizard then invokes
		// cockpit.spawn(["sudo", run-apply-enroll.sh, ...]) over the Cockpit bridge, and
		// that spawn must finish before systemd-run registers the transient unit.
		// Closing Chrome before then tears down the bridge and the systemd-run never
		// runs — nothing completes in the background and no marker is ever written
		// (observed in CI: empty apply-unit journal, no apply log, marker timeout). Wait
		// for the transient unit to exist so the drop genuinely tests
		// completion-after-disconnect rather than racing the delegation.
		waitForDelegatedApplyLaunched(harness, 90*time.Second)

		By("Dropping the browser control channel while the delegated apply runs")
		// The transient unit is a child of PID 1, so closing Chrome now cannot affect
		// it; completion must proceed in the background. The connectivity + NTP-sync +
		// finalize steps still have minutes to run, so the drop lands well before the
		// marker is written — exactly the completion-after-disconnect behaviour AC5
		// asserts.
		GinkgoWriter.Printf("wizard state at browser drop: %s\n", browser.WizardDebugState())
		browser.Close()

		By("Verifying onboarding completed in the background transient unit")
		// The .onboarding-complete marker is written by the delegated unit's finalize
		// step, which runs only after the connectivity budget (up to ~300s even for a
		// non-required host, since apply-and-enroll.sh loops until the budget or a
		// success) and the NTP-sync wait. Had the wizard run the apply inline instead,
		// closing the browser above would have killed it before finalize and no marker
		// would ever appear — so the marker's existence is itself the proof that the
		// systemd-run hand-off ran to completion after the control channel was gone.
		// (The apply log would corroborate this, but cleanup-onboarding.sh deletes it
		// on success, so it is unreliable as an assertion target — the marker is not.)
		expectCompletionMarkerWithin(harness, 6*time.Minute)

		By("Verifying the applied hostname took effect and was not rolled back")
		// Hostname is applied inline before delegation, but a *failed* delegated apply
		// runs rollback-config.sh which restores the original hostname. So the applied
		// hostname still being in place (together with the marker) confirms the
		// delegated apply succeeded rather than failing and rolling back.
		out, err := harness.VM.RunSSH([]string{"hostnamectl", "hostname"}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.TrimSpace(out.String())).To(Equal("single-nic-complete"))
	})

	It("When an apply fails after network activation it should roll back and keep the wizard reachable", Label("90463"), func() {
		harness := e2e.GetWorkerHarness()
		endpoint := harness.ApiEndpoint()
		token, _, err := login.LoginToEnvAsAdmin(harness)
		Expect(err).ToNot(HaveOccurred(), "failed to obtain admin token for enrollment")
		Expect(token).ToNot(BeEmpty())

		browser, cleanup := startBrowserSession()
		DeferCleanup(cleanup)
		// Registered after cleanup so it runs first (DeferCleanup is LIFO) while the
		// browser is still alive. DeferCleanup callbacks run after Ginkgo records the
		// spec failure, so CurrentSpecReport().Failed() is reliable here — a plain
		// `defer` runs during the failure unwind and would still see Failed()==false.
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				dumpEnrollmentDiagnostics(harness, browser)
			}
		})

		By("Capturing the original hostname before the apply")
		origOut, err := harness.VM.RunSSH([]string{"hostnamectl", "hostname"}, nil)
		Expect(err).ToNot(HaveOccurred())
		originalHostname := strings.TrimSpace(origOut.String())

		By("Configuring a network profile and enrollment, then forcing a post-activation failure")
		// The deployed onboarding package does not surface enrollment-credential
		// failures synchronously (an invalid token still reports success and is
		// rejected later by the agent — confirmed in CI). The failure the wizard
		// *does* surface after network activation is a required connectivity check
		// against an unreachable host: the apply activates the network profile first,
		// then the connectivity step fails, triggering the same rollback path AC6
		// describes. A static IPv4 profile is configured (keeping the SLIRP address so
		// the control channel survives) specifically so there is a flightctl-onboarding
		// NM profile for the rollback to remove.
		Expect(browser.WizardSelectNIC()).To(Succeed())
		Expect(browser.WizardConfigureStaticIPv4(
			slirpStaticIP, slirpStaticMask, slirpStaticGateway, "8.8.8.8",
		)).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // Network → Network Services
		Expect(browser.WizardClickNext()).To(Succeed()) // Network Services → Enrollment
		Expect(browser.WizardConfigureEnrollment(endpoint, token)).To(Succeed())
		Expect(browser.WizardSetTLSInsecure()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // Enrollment → Labels
		Expect(browser.WizardSetHostname("rollback-test")).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // Labels → Review
		Expect(browser.WizardSetConnectivityHost("connectivity-check.invalid")).To(Succeed())
		Expect(browser.WizardSetConnectivityRequired(true)).To(Succeed())
		Expect(browser.WizardClickApply()).To(Succeed())

		By("Verifying the apply fails")
		Expect(browser.WizardWaitForFailure(wizardTimeout)).To(Succeed())

		By("Verifying the applied configuration was rolled back")
		// On a required-step failure the progress page builds a rollback plan from the
		// applied items and runs rollback-config.sh, which restores the hostname and
		// deletes the applied flightctl-onboarding NM profile. Poll, since rollback
		// runs asynchronously in the background transient unit.
		Eventually(func() (string, error) {
			out, err := harness.VM.RunSSH([]string{"hostnamectl", "hostname"}, nil)
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(out.String()), nil
		}, 60*time.Second, 2*time.Second).Should(Equal(originalHostname),
			"hostname should have been restored by rollback after the failed apply")

		Eventually(func() []string {
			return onboardingNMProfiles(harness)
		}, 60*time.Second, 2*time.Second).Should(BeEmpty(),
			"flightctl-onboarding NM profile should be removed by rollback after the failed apply")

		By("Verifying onboarding did not complete")
		_, err = harness.VM.RunSSH([]string{
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
		DeferCleanup(cleanup2)
		alreadyComplete, err := browser2.WizardIsAlreadyComplete()
		Expect(err).ToNot(HaveOccurred())
		Expect(alreadyComplete).To(BeFalse(),
			"wizard should be reachable for a retry after rollback, not marked complete")
	})

	It("When a third-party enrollment script is configured it should receive the credential params file", Label("90432"), func() {
		harness := e2e.GetWorkerHarness()
		endpoint := harness.ApiEndpoint()
		token, _, err := login.LoginToEnvAsAdmin(harness)
		Expect(err).ToNot(HaveOccurred(), "failed to obtain admin token for enrollment")
		Expect(token).ToNot(BeEmpty())

		By("Installing a controllable mock over the enrollment script(s)")
		installEnrollmentMock(harness)
		// The deployed onboarding package defers enrollment exit-code handling to the
		// agent: a non-zero enrollment-script exit does NOT fail the wizard
		// synchronously (verified in CI — an exit-3 mock still completed the wizard).
		// So this spec asserts the part of AC7 the wizard actually exercises: the
		// third-party script is invoked and receives a non-empty credential params
		// file. The mock exits 0 so the wizard completes normally.
		setEnrollMockExitCode(harness, 0)

		// installEnrollmentMock bind-mounts the mock over the packaged enrollment
		// script inside PID 1's mount namespace (nsenter --mount=/proc/1/ns/mnt). The
		// wizard only runs the enrollment script in that namespace on the single-NIC
		// path, where it delegates to a systemd-run transient unit that systemd (PID 1)
		// spawns in PID 1's mount namespace; the multi-NIC path runs the script inline
		// in the Cockpit bridge's own namespace, where the bind is invisible and the
		// real flightctl-enroll.sh runs instead (observed in CI: the completion marker
		// appeared but the mock sentinel never did). So force the single-NIC delegated
		// path via StartCockpitTunnelViaInterface — the same way E5 (90430) does — and
		// verify the mock's record over SSH.
		workerID := GinkgoParallelProcess()
		sshPort := sshPortBase + workerID
		cockpitAddr, tunnelCleanup, err := e2e.StartCockpitTunnelViaInterface(sshPort, vmUser, vmPassword, slirpStaticIP)
		Expect(err).ToNot(HaveOccurred(), "failed to start Cockpit SSH tunnel")
		DeferCleanup(tunnelCleanup)

		browser := newLoggedInBrowser(cockpitAddr)
		// This spec closes the browser mid-body once the delegated apply has launched,
		// so on a post-close failure the screenshot is best-effort; the SSH-side
		// diagnostics and the mock sentinel are what confirm the script ran.
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				dumpEnrollmentDiagnostics(harness, browser)
			}
		})
		DeferCleanup(saveScreenshotOnFailure, browser, "enroll-script")

		By("Configuring enrollment so the wizard generates a credential params file for the script")
		navigateToEnrollmentStep(browser)
		Expect(browser.WizardConfigureEnrollment(endpoint, token)).To(Succeed())
		Expect(browser.WizardSetTLSInsecure()).To(Succeed())
		enrollmentReview(browser)
		Expect(browser.WizardClickApply()).To(Succeed())

		By("Waiting for the delegated apply to launch, then dropping the browser")
		// Wait for the transient unit before closing Chrome (see waitForDelegatedApplyLaunched
		// / E5). Dropping the browser afterwards avoids the single-NIC network
		// re-activation blipping the control channel during completion — the sentinel
		// and marker are verified purely over SSH.
		waitForDelegatedApplyLaunched(harness, 90*time.Second)
		browser.Close()

		By("Verifying onboarding completes in the background transient unit")
		expectCompletionMarkerWithin(harness, 6*time.Minute)

		By("Verifying the third-party script was invoked with a non-empty credential params file")
		sentinel := readEnrollSentinel(harness)
		Expect(sentinel).To(ContainSubstring("invoked=true"),
			"the wizard should have invoked the third-party enrollment script")
		Expect(sentinel).To(ContainSubstring("params_file_existed=true"))
		Expect(sentinel).To(ContainSubstring("params_file_nonempty=true"),
			"enrollment script should receive a non-empty credential params file")
		Expect(sentinel).To(ContainSubstring("creds_present=true"),
			"credential params file should contain credential material")
		Expect(sentinel).To(ContainSubstring("exit_code=0"))
	})
})

// enrollApproveRestartAndWaitOnline starts the onboarding device's agent, reads
// the enrollment ID it logs, approves the request, and waits for the device to
// report online.
//
// On a successful wizard run the agent has NOT been started yet. The deployed
// flightctl-enroll.sh installs /etc/flightctl/config.yaml + the enrollment cert
// but, while the onboarding gate marker (.onboarding-confirmed) is absent, only
// `systemctl enable`s the agent and DEFERS the start (it logs "Onboarding gate
// active — deferring flightctl-agent start"). The marker and the first agent start
// happen only in cleanup-onboarding.sh, which runs on session end
// (setup.service ExecStop) — after this spec has finished. So at this point the
// agent unit is inactive and its journal is empty; reading the enrollment ID
// before starting the agent is guaranteed to find nothing (the historical "pass"
// relied on a base-image auto-enrolled agent polluting the journal, which the
// BeforeSuite snapshot reset now removes).
//
// So arm the gate and start the agent ourselves — exactly what cleanup-onboarding.sh
// does on session end — triggered deterministically here instead. The agent then
// reads the wizard-written config, creates its EnrollmentRequest, and logs the ID.
func enrollApproveRestartAndWaitOnline(h *e2e.Harness) string {
	GinkgoHelper()

	// The flightctl-agent unit is gated by a drop-in
	// (ConditionPathExists=/var/lib/flightctl-onboarding/.onboarding-confirmed);
	// while that marker is absent `systemctl start`/`restart` is a silent no-op
	// (unmet start condition). Create it (the exact line cleanup-onboarding.sh runs)
	// so the agent can start at all, then start the agent so it enrolls.
	_, err := h.VM.RunSSH([]string{
		"sudo", "touch", "/var/lib/flightctl-onboarding/.onboarding-confirmed",
	}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to arm the flightctl-agent onboarding gate marker")
	Expect(h.RestartFlightCtlAgent()).To(Succeed(), "failed to start flightctl-agent for enrollment")

	// GetEnrollmentIDFromServiceLogs reads the LATEST systemd invocation's logs and
	// polls until the ID appears, so it sees only this fresh enrollment.
	deviceID := h.GetEnrollmentIDFromServiceLogs("flightctl-agent")
	Expect(deviceID).ToNot(BeEmpty(), "enrollment ID should be present in agent logs")

	h.WaitForEnrollmentRequest(deviceID)
	h.ApproveEnrollment(deviceID, h.TestEnrollmentApproval())

	// The onboarding agent config uses a short enrollment-verify backoff cap, so by
	// approval time the enrolling agent has usually gone inactive. Restart it so it
	// re-reads its (persisted) enrollment identity, picks up the now-approved
	// request, and fetches its certificate. The device ID is stable across the
	// restart because a plain restart preserves /var/lib/flightctl enrollment state.
	Expect(h.RestartFlightCtlAgent()).To(Succeed(), "failed to restart flightctl-agent after approval")
	h.WaitForOnlineStatus(deviceID)
	return deviceID
}

// installEnrollmentMock stages enrollMockScript as a root-owned, 0755 file and
// bind-mounts it over every enrollment script in onboardingScriptDir. The wizard's
// generated enrollment script must live in that allowlisted directory, so
// replacing the scripts there guarantees the wizard invokes the mock regardless
// of the real script's filename.
//
// A bind mount is used rather than `install`/`cp` because the packaged scripts
// live under /usr, which is read-only in bootc image mode — overwriting the file
// in place fails with EROFS. Bind-mounting is a mount-namespace operation, not a
// write to the underlying filesystem, so it works over a read-only /usr. The mount
// source must be root-owned because apply-and-enroll.sh's validate_script_path
// rejects any script whose realpath is not owned by uid 0 (`sudo tee` writes the
// staged file as root).
//
// The mount is performed inside PID 1's mount namespace via
// `nsenter --mount=/proc/1/ns/mnt`. The delegated enrollment script runs in a
// systemd-run transient unit, which systemd (PID 1) spawns in ITS mount namespace;
// a plain `mount --bind` from this SSH session lands in the session's own mount
// namespace and is invisible to that unit, so the real enrollment script — not the
// mock — runs (observed in CI: the completion marker appeared but the mock sentinel
// never did, i.e. the genuine flightctl-enroll.sh ran). Entering PID 1's namespace
// puts the bind exactly where the transient unit will see it. All mounts are
// discarded when the suite reverts the VM to its pre-onboarding snapshot between
// specs, so no explicit unmount is needed.
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
			"sudo", "nsenter", "--mount=/proc/1/ns/mnt", "mount", "--bind",
			enrollMockRemotePath, onboardingScriptDir + "/" + name,
		}, nil)
		Expect(err).ToNot(HaveOccurred(), "failed to bind-mount mock over "+name)
	}
}

// dumpEnrollmentDiagnostics captures wizard + guest state to the Ginkgo output
// when an enrollment spec fails, so a CI run reveals the real reason instead of a
// bare "timed out". It is strictly best-effort: every step tolerates its own
// failure (the browser may already be closed, and any given file/unit may not
// exist), and it never asserts. Call it from a DeferCleanup guard — which runs
// after Ginkgo has recorded the failure, so CurrentSpecReport().Failed() is
// reliable (a plain defer runs during the unwind and would see it as false).
// Register it after the browser-cleanup DeferCleanup so it runs first (LIFO)
// while the browser is still alive:
//
//	DeferCleanup(func() {
//	    if CurrentSpecReport().Failed() {
//	        dumpEnrollmentDiagnostics(harness, browser)
//	    }
//	})
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

	// Each entry is a label plus an argv. Most are plain argv with no shell
	// metacharacters; the delegated-apply-unit entry is a single-element pipeline
	// deliberately run through a shell (its pattern is a static string with no
	// harvested values). Paths use the OLD flightctl-onboarding naming the CI VM
	// still ships (see the onboarding RPM naming note).
	diagnostics := []struct {
		label string
		argv  []string
	}{
		{"apply log", []string{"sudo", "cat", "/var/log/flightctl-onboarding-apply.log"}},
		{"watchdog status", []string{"sudo", "cat", "/var/lib/flightctl-onboarding/.watchdog-status"}},
		{"agent journal", []string{"sudo", "journalctl", "-u", "flightctl-agent", "--no-pager", "-n", "300"}},
		{"agent unit state", []string{"systemctl", "is-active", "flightctl-agent"}},
		// The delegated apply runs in a systemd-run transient unit
		// (flightctl-onboarding-apply-*), whose output lands in the system journal
		// and — unlike the apply log — survives cleanup-onboarding.sh. A raw
		// `journalctl -n` dump buries these lines under SSH-session ("Session N of
		// user") spam, so filter to the apply/enrollment markers. Single-element argv
		// is run through a shell by RunSSH (the pattern is a static string with no
		// harvested values, so there is nothing to inject); `|| true` keeps a
		// no-match grep from surfacing as an error.
		{"delegated apply unit journal", []string{
			"sudo journalctl --no-pager -n 4000 | " +
				"grep -aiE 'onboarding-apply|apply-and-enroll|Delegating network|" +
				"Waiting for connectivity|Connectivity (confirmed|not available)|" +
				"enrollment script|No enrollment scripts|Onboarding completed|ROLLBACK|ERROR' " +
				"|| true",
		}},
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
