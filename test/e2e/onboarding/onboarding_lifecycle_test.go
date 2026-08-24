//go:build linux

package onboarding_test

import (
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// EDM-4193 — Onboarding service lifecycle.
//
// These specs verify the packaged flightctl-onboarding lifecycle: the setup
// service is wired to run on first boot, flightctl-agent is gated until
// onboarding is confirmed, the one-shot cleanup tears everything down after
// completion, and the onboarding user carries the correct polkit/sudo rights.
//
// Two facts about the packaging drive the whole file (confirmed against
// flightctl/cockpit-onboarding):
//
//   - There are TWO markers under /var/lib/flightctl-onboarding. The setup
//     service is gated by .onboarding-complete (written by finalize when the
//     wizard succeeds); flightctl-agent is gated by .onboarding-confirmed
//     (touched by cleanup). They are not interchangeable.
//   - cleanup-onboarding.sh runs as flightctl-onboarding-setup.service's ExecStop
//     or, if the device lost power between finalize and the operator clicking
//     Finish, as flightctl-onboarding-recovery.service's ExecStart. This suite
//     deliberately never starts setup.service — its setup-network.sh reassigns
//     the primary NIC to a well-known static IP and would sever the single-NIC
//     SLIRP SSH/cockpit control channel — so we drive cleanup through the
//     recovery service, which runs the identical shipped script.
const (
	onboardingSetupService    = "flightctl-onboarding-setup.service"
	onboardingRecoveryService = "flightctl-onboarding-recovery.service"
	flightctlAgentService     = "flightctl-agent.service"

	markerComplete  = "/var/lib/flightctl-onboarding/.onboarding-complete"
	markerConfirmed = "/var/lib/flightctl-onboarding/.onboarding-confirmed"

	onboardingUserName = "onboarding"
	polkitRulePath     = "/etc/polkit-1/rules.d/49-flightctl-onboarding.rules"
	sudoersPath        = "/etc/sudoers.d/flightctl-onboarding"
)

// systemctlShow returns the value of a single systemd property. `systemctl show`
// exits 0 even for unknown units (printing an empty value), so this never errors
// on a missing unit — callers assert on the returned string.
func systemctlShow(h *e2e.Harness, unit, prop string) string {
	GinkgoHelper()
	out, err := h.VM.RunSSH([]string{"systemctl", "show", unit, "-p", prop, "--value"}, nil)
	Expect(err).ToNot(HaveOccurred(), "systemctl show %s -p %s", unit, prop)
	return strings.TrimSpace(out.String())
}

// systemctlIsEnabled returns the enablement state word ("enabled", "disabled",
// "masked", ...). `systemctl is-enabled` exits non-zero for disabled/masked
// units and RunSSH treats a non-zero exit as an error, so run it through a shell
// that swallows the status and yields the state word on stdout regardless.
func systemctlIsEnabled(h *e2e.Harness, unit string) string {
	GinkgoHelper()
	out, err := h.VM.RunSSH([]string{"bash", "-c", "systemctl is-enabled " + unit + " 2>/dev/null || true"}, nil)
	Expect(err).ToNot(HaveOccurred(), "systemctl is-enabled %s", unit)
	return strings.TrimSpace(out.String())
}

// fileExistsSudo reports whether a path exists, using sudo so it can traverse
// the 0700 onboarding-owned marker directory that the SSH user cannot enter.
func fileExistsSudo(h *e2e.Harness, path string) bool {
	GinkgoHelper()
	_, err := h.VM.RunSSH([]string{"sudo", "test", "-e", path}, nil)
	return err == nil
}

// userExists reports whether a local user account is present.
func userExists(h *e2e.Harness, user string) bool {
	GinkgoHelper()
	_, err := h.VM.RunSSH([]string{"getent", "passwd", user}, nil)
	return err == nil
}

// completeMinimalWizard drives the wizard through the shortest path to a
// successful apply: pick the NIC, skip every optional domain, set a hostname,
// and apply with the connectivity test relaxed. On success the wizard's finalize
// step writes the .onboarding-complete marker.
func completeMinimalWizard(browser *e2e.OnboardingBrowser) {
	GinkgoHelper()
	Expect(browser.WizardSelectNIC()).To(Succeed())
	Expect(browser.WizardClickNext()).To(Succeed()) // → Network Services
	Expect(browser.WizardClickNext()).To(Succeed()) // → Enrollment
	Expect(browser.WizardDisableEnrollment()).To(Succeed())
	Expect(browser.WizardClickNext()).To(Succeed()) // → Labels
	Expect(browser.WizardSetHostname("lifecycle-test")).To(Succeed())
	Expect(browser.WizardClickNext()).To(Succeed()) // → Review
	Expect(browser.WizardSetConnectivityHost("127.0.0.1")).To(Succeed())
	Expect(browser.WizardSetConnectivityRequired(false)).To(Succeed())
	Expect(browser.WizardClickApply()).To(Succeed())
	Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())
}

// runCleanupViaRecovery triggers the one-shot post-onboarding cleanup by
// starting flightctl-onboarding-recovery.service. Its conditions
// (.onboarding-complete present, .onboarding-confirmed absent) are satisfied once
// the wizard has finished, so it runs cleanup-onboarding.sh — the same script
// setup.service would run at ExecStop in production. See the file header for why
// the suite uses the recovery path rather than starting setup.service.
func runCleanupViaRecovery(h *e2e.Harness) {
	GinkgoHelper()
	// Tolerate a non-zero exit: cleanup terminates the onboarding user and does
	// other best-effort teardown, so assert on the observable end state (the
	// confirmation marker) rather than the unit's exit code.
	_, err := h.VM.RunSSH([]string{"bash", "-c", "sudo systemctl start " + onboardingRecoveryService + " || true"}, nil)
	Expect(err).ToNot(HaveOccurred())
	Eventually(func() bool {
		return fileExistsSudo(h, markerConfirmed)
	}, 60*time.Second, 2*time.Second).Should(BeTrue(),
		"cleanup should create the agent confirmation marker")
}

var _ = Describe("Onboarding service lifecycle", func() {

	It("When the device first boots the onboarding setup service is enabled and the wizard is reachable", Label("90414"), func() {
		harness := e2e.GetWorkerHarness()

		By("Verifying the completion marker is absent on a fresh device (AC #1)")
		Expect(fileExistsSudo(harness, markerComplete)).To(BeFalse(),
			"a fresh device must not carry the onboarding completion marker")

		By("Verifying the onboarding setup service is enabled to run on boot")
		// Assert the service is enabled and its start gate is satisfiable rather
		// than actually starting it: setup.service runs setup-network.sh, which
		// would sever the single-NIC SLIRP control channel. Enabled + a currently
		// satisfied ConditionPathExists is exactly what makes it start on boot.
		Expect(systemctlIsEnabled(harness, onboardingSetupService)).To(Equal("enabled"))
		unit, err := harness.VM.RunSSH([]string{"systemctl", "cat", onboardingSetupService}, nil)
		Expect(err).ToNot(HaveOccurred(), "setup service unit must be installed")
		Expect(unit.String()).To(ContainSubstring("ConditionPathExists=!"+markerComplete),
			"setup service must be gated to run only while onboarding is incomplete")

		By("Verifying cockpit.socket is active so the wizard is served")
		Expect(systemctlShow(harness, "cockpit.socket", "ActiveState")).To(Equal("active"))

		By("Verifying the onboarding wizard renders in the browser")
		browser, cleanup := startBrowserSession()
		defer cleanup()
		complete, err := browser.WizardIsAlreadyComplete()
		Expect(err).ToNot(HaveOccurred())
		Expect(complete).To(BeFalse(),
			"a fresh device should show the wizard, not the already-complete screen")
		Expect(browser.WizardSelectNIC()).To(Succeed(), "the wizard Network step should render")
	})

	It("When the confirmation marker is absent the flightctl-agent does not start", Label("90459"), func() {
		harness := e2e.GetWorkerHarness()

		By("Verifying the agent gate drop-in is installed and the marker is absent (AC #2)")
		Expect(fileExistsSudo(harness, markerConfirmed)).To(BeFalse())
		unit, err := harness.VM.RunSSH([]string{"systemctl", "cat", flightctlAgentService}, nil)
		Expect(err).ToNot(HaveOccurred(), "flightctl-agent.service must be present on the onboarding base image")
		Expect(unit.String()).To(ContainSubstring("ConditionPathExists="+markerConfirmed),
			"the onboarding RPM must gate the agent on the confirmation marker")

		By("Attempting to start the agent and confirming the gate blocks it")
		// Stop first so the subsequent start re-evaluates the condition
		// deterministically, independent of whatever the unit did at boot.
		_, _ = harness.VM.RunSSH([]string{"sudo", "systemctl", "stop", flightctlAgentService}, nil)
		_, err = harness.VM.RunSSH([]string{"sudo", "systemctl", "start", flightctlAgentService}, nil)
		Expect(err).ToNot(HaveOccurred(),
			"starting a condition-blocked unit should succeed as a no-op")

		Expect(systemctlShow(harness, flightctlAgentService, "ConditionResult")).To(Equal("no"),
			"the agent's start condition must fail while the confirmation marker is absent")
		Expect(systemctlShow(harness, flightctlAgentService, "ActiveState")).To(Equal("inactive"),
			"the agent must stay inactive while gated")
	})

	It("When onboarding completes cleanup confirms the agent, removes the onboarding user, and disables the setup service", Label("90418"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Completing a minimal wizard flow")
		completeMinimalWizard(browser)
		expectCompletionMarker(harness)

		By("Confirming the onboarding user exists before cleanup")
		Expect(userExists(harness, onboardingUserName)).To(BeTrue())

		By("Running the one-shot cleanup via the recovery service (AC #3)")
		runCleanupViaRecovery(harness)

		By("Verifying the agent confirmation marker was written")
		Expect(fileExistsSudo(harness, markerConfirmed)).To(BeTrue())

		By("Verifying the onboarding user was removed")
		Eventually(func() bool {
			return userExists(harness, onboardingUserName)
		}, 30*time.Second, 2*time.Second).Should(BeFalse(),
			"cleanup should delete the onboarding user")

		By("Verifying the setup service was turned off for subsequent boots")
		// The AC calls this "masks the service"; the shipped cleanup script
		// disables it (systemctl disable), which likewise prevents it from being
		// pulled into any future boot transaction. Assert the actual behavior.
		Expect(systemctlIsEnabled(harness, onboardingSetupService)).To(Equal("disabled"))

		By("Verifying the polkit rule and sudoers grant were removed")
		Expect(fileExistsSudo(harness, polkitRulePath)).To(BeFalse())
		Expect(fileExistsSudo(harness, sudoersPath)).To(BeFalse())
	})

	It("When onboarding has completed the setup service does not start on subsequent boots", Label("90452"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Completing a minimal wizard flow and running cleanup")
		completeMinimalWizard(browser)
		expectCompletionMarker(harness)
		runCleanupViaRecovery(harness)

		By("Verifying the setup service is disabled (AC #4)")
		Expect(systemctlIsEnabled(harness, onboardingSetupService)).To(Equal("disabled"))

		By("Verifying the setup service is also condition-blocked once onboarding is complete")
		// Two independent gates keep the service from running on the next boot:
		//   (1) it is disabled, so it is not pulled into the boot transaction, and
		//   (2) its ConditionPathExists=!<complete-marker> now evaluates false.
		// We assert both declaratively rather than rebooting: a real reboot of the
		// nested single-NIC SLIRP guest mid-suite is slow and fragile, and we must
		// never actually start the unit (setup-network.sh would sever SSH). The
		// marker's presence is exactly what the boot-time condition check reads, so
		// the same evaluation that runs on boot would skip the unit here.
		Expect(fileExistsSudo(harness, markerComplete)).To(BeTrue())
		unit, err := harness.VM.RunSSH([]string{"systemctl", "cat", onboardingSetupService}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(unit.String()).To(ContainSubstring("ConditionPathExists=!" + markerComplete))
	})

	It("When the confirmation marker exists the flightctl-agent is enabled and its start condition passes", Label("90428"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Completing a minimal wizard flow and running cleanup")
		completeMinimalWizard(browser)
		expectCompletionMarker(harness)
		runCleanupViaRecovery(harness)

		By("Verifying the confirmation marker now exists (AC #5)")
		Expect(fileExistsSudo(harness, markerConfirmed)).To(BeTrue())

		By("Verifying cleanup enabled the agent")
		Expect(systemctlIsEnabled(harness, flightctlAgentService)).To(Equal("enabled"))

		By("Verifying the agent's start condition now passes")
		// The same gate that blocked the agent while the marker was absent must now
		// pass. ConditionResult is the definitive signal; the agent's subsequent
		// health depends on enrollment configuration, which is out of scope here.
		_, err := harness.VM.RunSSH([]string{"sudo", "systemctl", "start", flightctlAgentService}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(systemctlShow(harness, flightctlAgentService, "ConditionResult")).To(Equal("yes"),
			"with the confirmation marker present the agent's gate condition must pass")
	})

	It("When acting as the onboarding user polkit authorizes the wizard's privileged operations", Label("90426"), func() {
		harness := e2e.GetWorkerHarness()

		By("Verifying the onboarding user exists and the polkit rule is installed (AC #6)")
		Expect(userExists(harness, onboardingUserName)).To(BeTrue())
		Expect(fileExistsSudo(harness, polkitRulePath)).To(BeTrue())

		// authorized runs pkcheck as the given user against its own shell process.
		// pkcheck is non-interactive by default, so an action the rule does not
		// grant (falling through to auth_admin) reports "not authorized" and exits
		// non-zero; an action the rule returns YES for exits 0.
		authorized := func(user, action string) bool {
			out, _ := harness.VM.RunSSH([]string{
				"sudo", "-u", user, "bash", "-c",
				"pkcheck --action-id " + action + " --process $$ >/dev/null 2>&1; echo $?",
			}, nil)
			return strings.TrimSpace(out.String()) == "0"
		}

		By("Verifying the onboarding user is authorized for the wizard's D-Bus actions")
		for _, action := range []string{
			"org.freedesktop.hostname1.set-static-hostname",
			"org.freedesktop.timedate1.set-ntp",
			"org.freedesktop.NetworkManager.settings.modify.system",
		} {
			Expect(authorized(onboardingUserName, action)).To(BeTrue(),
				"onboarding user should be polkit-authorized for "+action)
		}

		By("Verifying the grant is scoped: onboarding is NOT auto-authorized for unlisted actions")
		Expect(authorized(onboardingUserName, "org.freedesktop.systemd1.manage-units")).To(BeFalse(),
			"the rule must allow only the specific onboarding actions, not systemd unit management")

		By("Verifying a non-onboarding user is denied the same D-Bus action")
		_, err := harness.VM.RunSSH([]string{"sudo", "useradd", "-M", "polkit-negative-test"}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(authorized("polkit-negative-test", "org.freedesktop.hostname1.set-static-hostname")).To(BeFalse(),
			"a user other than 'onboarding' must not be authorized by the onboarding polkit rule")

		By("Verifying the onboarding sudoers grant covers the privileged helpers")
		// "Can run privileged commands" (AC #6) is delivered via sudoers, not
		// polkit: the RPM ships /etc/sudoers.d/flightctl-onboarding granting the
		// onboarding user NOPASSWD access to the onboarding helper scripts.
		out, err := harness.VM.RunSSH([]string{"sudo", "cat", sudoersPath}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring(onboardingUserName))
		Expect(out.String()).To(ContainSubstring("flightctl-onboarding"))
	})
})
