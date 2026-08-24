//go:build linux

package onboarding_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	sshPortBase = 2233
	// vmUser/vmPassword are NOT secrets: they are the well-known, hard-coded
	// login baked into the ephemeral e2e base VM image (see the VM provisioning
	// harness). The image fixes this account, so the value cannot be injected at
	// runtime; it only ever authenticates to a throwaway local libvirt guest.
	vmUser     = "user"
	vmPassword = "user" // gitleaks:allow -- fixed credential of the ephemeral test VM image
	// cockpitUser is the passwordless "onboarding" user created by
	// create-onboarding-user.sh. The wizard's privileged apply operations
	// (hostname/NTP/NetworkManager via D-Bus) are authorized by the polkit rule
	// 49-cockpit-system-onboarding.rules ONLY for subject.user == "onboarding".
	// Logging in as any other user (e.g. "user") makes every apply step fail
	// polkit authorization. SSH access for this user is intentionally blocked, so
	// the SSH tunnel and all system verification still use vmUser/vmPassword.
	// cockpitPassword is not a secret either: the suite sets it on the throwaway
	// onboarding account purely so headless Chrome can log in deterministically.
	cockpitUser     = "onboarding"
	cockpitPassword = "onboarding" // gitleaks:allow -- test-only password for the ephemeral onboarding account
	wizardTimeout   = 120 * time.Second

	// SLIRP-matching static IPv4 values. The nested VM has a single NIC on QEMU
	// user-mode (SLIRP) networking; the guest is fixed at 10.0.2.15 with gateway
	// 10.0.2.2, and SSH (thus the cockpit/chromedp tunnel) reaches it there. Static
	// IPv4 wizard tests must use these values so applying the profile preserves the
	// guest address and does not sever the control channel mid-apply. The resulting
	// NM profile is still ipv4.method=manual, so it exercises the static-IP path.
	slirpStaticIP      = "10.0.2.15"
	slirpStaticMask    = "255.255.255.0"
	slirpStaticGateway = "10.0.2.2"
)

// newLoggedInBrowser creates a headless Chrome session and logs it in to the
// Cockpit wizard as the onboarding user. The caller owns the returned browser
// and must Close() it. Sharing this helper keeps tunnel-independent session
// setup (creation, login, and its failure messages) identical across every spec.
func newLoggedInBrowser(cockpitAddr string) *e2e.OnboardingBrowser {
	GinkgoHelper()
	browser, err := e2e.NewOnboardingBrowser(context.Background())
	Expect(err).ToNot(HaveOccurred(), "failed to create headless Chrome session")

	if err := browser.CockpitLogin(cockpitAddr, cockpitUser, cockpitPassword); err != nil {
		// Close the just-created Chrome session before the failing assertion
		// aborts the spec, so a login failure does not leak the browser.
		browser.Close()
		Expect(err).ToNot(HaveOccurred(), "failed to log in to Cockpit")
	}
	return browser
}

// startBrowserSession creates an SSH tunnel to Cockpit and a headless Chrome
// session logged in to the wizard. Returns the browser and a cleanup function.
func startBrowserSession() (*e2e.OnboardingBrowser, func()) {
	workerID := GinkgoParallelProcess()
	sshPort := sshPortBase + workerID

	cockpitAddr, tunnelCleanup, err := e2e.StartCockpitTunnel(sshPort, vmUser, vmPassword)
	Expect(err).ToNot(HaveOccurred(), "failed to start Cockpit SSH tunnel")

	// If newLoggedInBrowser aborts the spec (its Expect panics through Ginkgo's
	// Fail), the caller never receives the cleanup func below, so tear the tunnel
	// down as we unwind. Ownership passes to the returned cleanup once the browser
	// is up.
	tunnelOwned := false
	defer func() {
		if !tunnelOwned {
			tunnelCleanup()
		}
	}()

	browser := newLoggedInBrowser(cockpitAddr)
	tunnelOwned = true

	cleanup := func() {
		saveScreenshotOnFailure(browser, "")
		browser.Close()
		tunnelCleanup()
	}
	return browser, cleanup
}

// saveScreenshotOnFailure writes a PNG of the current browser state to the
// artifacts directory when the running spec has failed. It is best-effort:
// capture and write errors are logged to the Ginkgo output but never fatal,
// since it runs on the teardown/failure path (often while the browser context
// is about to be cancelled). The file lands in
// artifacts/onboarding-screenshots/<spec-slug>[-<label>].png; label
// disambiguates specs that drive more than one browser session.
func saveScreenshotOnFailure(browser *e2e.OnboardingBrowser, label string) {
	if !CurrentSpecReport().Failed() {
		return
	}
	png, err := browser.Screenshot()
	if err != nil {
		GinkgoWriter.Printf("screenshot: capture failed: %v\n", err)
		return
	}
	dir := filepath.Join(util.GetTopLevelDir(), "artifacts", "onboarding-screenshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		GinkgoWriter.Printf("screenshot: mkdir %s failed: %v\n", dir, err)
		return
	}
	name := sanitizeFileName(CurrentSpecReport().LeafNodeText)
	if label != "" {
		name += "-" + label
	}
	path := filepath.Join(dir, name+".png")
	if err := os.WriteFile(path, png, 0o600); err != nil {
		GinkgoWriter.Printf("screenshot: write %s failed: %v\n", path, err)
		return
	}
	GinkgoWriter.Printf("screenshot: saved failure screenshot to %s\n", path)
	AddReportEntry("failure-screenshot", path)
}

// sanitizeFileName reduces an arbitrary spec description to a filesystem-safe
// slug: every run of non-alphanumeric characters collapses to a single hyphen.
func sanitizeFileName(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// getOnboardingNMProfileOfType returns the name of the flightctl-onboarding-* NM
// connection profile of the given connection type ("802-3-ethernet", "vlan",
// ...). Selecting by type matters for the VLAN spec: a VLAN apply leaves two
// flightctl-onboarding-* profiles — the ethernet parent and the VLAN child — and
// `nmcli con show` ordering is not guaranteed, so matching by prefix alone could
// return the parent and make the VLAN assertions fail.
func getOnboardingNMProfileOfType(h *e2e.Harness, connType string) string {
	GinkgoHelper()
	out, err := h.VM.RunSSH([]string{"nmcli", "-t", "-f", "NAME,TYPE", "con", "show"}, nil)
	Expect(err).ToNot(HaveOccurred(), "nmcli con show failed")

	var seen []string
	for _, line := range strings.Split(out.String(), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "flightctl-onboarding-") {
			continue
		}
		seen = append(seen, line)
		if fields[1] == connType {
			return fields[0]
		}
	}
	Fail("no flightctl-onboarding-* NM profile of type " + connType + " found; profiles: " + strings.Join(seen, ", "))
	return ""
}

// expectCompletionMarker asserts the onboarding completion marker eventually
// exists. Two subtleties require care here:
//
//   - The marker lives under /var/lib/flightctl-onboarding, which
//     create-onboarding-user.sh creates as 0700 owned by onboarding:onboarding.
//     The SSH verification user ("user") cannot traverse that directory, so a
//     plain `test -f` returns exit 1 (permission denied on the dir) whether or
//     not the file exists. The check must run under sudo.
//   - The wizard fires the apply via `systemd-run --no-block`
//     (run-apply-enroll.sh), so apply-and-enroll.sh — and its finalize step
//     that writes the marker — runs asynchronously in a transient unit. The UI
//     success state can surface before that background unit reaches finalize,
//     so poll rather than checking once.
func expectCompletionMarker(h *e2e.Harness) {
	Eventually(func() error {
		_, err := h.VM.RunSSH([]string{
			"sudo", "test", "-f", "/var/lib/flightctl-onboarding/.onboarding-complete",
		}, nil)
		return err
	}, 60*time.Second, 2*time.Second).Should(Succeed(), "completion marker not found")
}

// expectNTPServer asserts the wizard-configured NTP server landed in the
// cockpit-generated NTP config. configure-ntp.sh writes the server to a
// backend-specific file — /etc/chrony/sources.d/cockpit.sources for chronyd or
// /etc/systemd/timesyncd.conf.d/50-cockpit.conf for systemd-timesyncd — never
// to /etc/chrony.conf or /etc/chrony.d/ (grepping those only matches the distro
// default pool and, when /etc/chrony.d/ is absent, makes grep exit 2). Read both
// candidate files under sudo, tolerating whichever backend is not in use.
func expectNTPServer(h *e2e.Harness, server string) {
	Eventually(func() (string, error) {
		out, err := h.VM.RunSSH([]string{
			"sudo cat /etc/chrony/sources.d/cockpit.sources " +
				"/etc/systemd/timesyncd.conf.d/50-cockpit.conf 2>/dev/null || true",
		}, nil)
		if err != nil {
			return "", err
		}
		return out.String(), nil
	}, 30*time.Second, 2*time.Second).Should(ContainSubstring(server),
		"cockpit-generated NTP config should contain the configured server")
}

// navigateFromNetworkToApply advances the wizard from the Network step through
// Review and clicks Apply. The caller must have already configured the Network
// step (at minimum selected a NIC).
func navigateFromNetworkToApply(browser *e2e.OnboardingBrowser) {
	// Network → Network Services
	Expect(browser.WizardClickNext()).To(Succeed())
	// Network Services → Enrollment
	Expect(browser.WizardClickNext()).To(Succeed())
	// Enrollment is selected by default; disable it so Apply is not bounced back
	// to the Enrollment step for a missing server address.
	Expect(browser.WizardDisableEnrollment()).To(Succeed())
	// Enrollment → Labels
	Expect(browser.WizardClickNext()).To(Succeed())
	// Labels → Review
	Expect(browser.WizardClickNext()).To(Succeed())
	// The Review step is invalid until a connectivity-test host is set. Use a
	// loopback IP (IPs skip DNS) and mark the connectivity test as not required so
	// an unreachable host degrades to a warning instead of failing the apply — the
	// e2e VM has no guaranteed outbound reachability and these tests don't enroll.
	Expect(browser.WizardSetConnectivityHost("127.0.0.1")).To(Succeed())
	Expect(browser.WizardSetConnectivityRequired(false)).To(Succeed())
	// Review → Apply
	Expect(browser.WizardClickApply()).To(Succeed())
}

var _ = Describe("Onboarding wizard configuration flow", func() {

	It("When all config domains are set it should apply all configurations successfully", Label("90408"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Selecting NIC and configuring static IPv4")
		Expect(browser.WizardSelectNIC()).To(Succeed())
		// The nested VM has a single NIC on QEMU user-mode (SLIRP) networking with
		// the guest fixed at 10.0.2.15 (gateway 10.0.2.2). SSH — and therefore the
		// cockpit/chromedp tunnel — reaches the guest via that address, so the static
		// config must preserve it; a foreign subnet would break the control channel
		// mid-apply. The NM profile is still ipv4.method=manual, satisfying AC #4.
		Expect(browser.WizardConfigureStaticIPv4(
			slirpStaticIP, slirpStaticMask, slirpStaticGateway, "8.8.8.8",
		)).To(Succeed())

		By("Navigating to Network Services and configuring NTP and proxy")
		Expect(browser.WizardClickNext()).To(Succeed())
		Expect(browser.WizardConfigureNTP("pool.ntp.org")).To(Succeed())
		Expect(browser.WizardConfigureProxy("squid.local", "3128", "", "")).To(Succeed())

		By("Skipping Enrollment and setting hostname + labels")
		Expect(browser.WizardClickNext()).To(Succeed()) // → Enrollment
		Expect(browser.WizardDisableEnrollment()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Labels
		Expect(browser.WizardSetHostnameAndLabels("onboarding-e2e-host", map[string]string{
			"env":  "test",
			"role": "edge",
		})).To(Succeed())

		By("Reviewing and applying")
		Expect(browser.WizardClickNext()).To(Succeed()) // → Review
		Expect(browser.WizardSetConnectivityHost("127.0.0.1")).To(Succeed())
		Expect(browser.WizardSetConnectivityRequired(false)).To(Succeed())
		Expect(browser.WizardClickApply()).To(Succeed())
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())

		By("Verifying hostname was applied (AC #2)")
		out, err := harness.VM.RunSSH([]string{"hostnamectl", "hostname"}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.TrimSpace(out.String())).To(Equal("onboarding-e2e-host"))

		By("Verifying labels file was written (AC #3)")
		out, err = harness.VM.RunSSH([]string{"sudo", "cat", "/etc/flightctl/conf.d/50-cockpit-labels.yaml"}, nil)
		Expect(err).ToNot(HaveOccurred())
		labelsContent := out.String()
		Expect(labelsContent).To(ContainSubstring("env"))
		Expect(labelsContent).To(ContainSubstring("test"))
		Expect(labelsContent).To(ContainSubstring("role"))
		Expect(labelsContent).To(ContainSubstring("edge"))

		By("Verifying NM connection profile was created (AC #4)")
		profileName := getOnboardingNMProfileOfType(harness, "802-3-ethernet")
		out, err = harness.VM.RunSSH([]string{
			"nmcli", "-t", "-f", "ipv4.addresses", "con", "show", profileName,
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring(slirpStaticIP + "/24"))

		By("Verifying NTP configuration")
		expectNTPServer(harness, "pool.ntp.org")

		By("Verifying proxy files were written (AC #5)")
		out, err = harness.VM.RunSSH([]string{"cat", "/etc/environment"}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("squid.local:3128"))

		out, err = harness.VM.RunSSH([]string{
			"ls", "/etc/systemd/system.conf.d/",
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("flightctl-onboarding"))

		By("Verifying completion marker exists")
		expectCompletionMarker(harness)
	})

	It("When reviewing it should display all collected config with the password masked", Label("90467"), func() {
		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Configuring every domain so the review screen has all of them to display")
		// Static IPv4 uses the SLIRP-matching values (see slirpStaticIP). This spec
		// never applies — it only reads the Review screen — so the address would not sever
		// the control channel here, but keeping the values consistent avoids surprises
		// and gives the review a known IP to assert on.
		Expect(browser.WizardSelectNIC()).To(Succeed())
		Expect(browser.WizardConfigureStaticIPv4(
			slirpStaticIP, slirpStaticMask, slirpStaticGateway, "8.8.8.8",
		)).To(Succeed())

		Expect(browser.WizardClickNext()).To(Succeed()) // → Network Services
		Expect(browser.WizardConfigureNTP("time.example.com")).To(Succeed())
		// The proxy password here is a throwaway literal typed into the wizard so
		// the assertion below can prove it is masked (never rendered) on the Review
		// screen — it is test data, not a real credential.
		Expect(browser.WizardConfigureProxy("proxy.example.com", "8080", "proxyuser", "secret123")).To(Succeed()) // gitleaks:allow -- dummy value asserted to be masked below

		Expect(browser.WizardClickNext()).To(Succeed()) // → Enrollment
		Expect(browser.WizardDisableEnrollment()).To(Succeed())

		Expect(browser.WizardClickNext()).To(Succeed()) // → Labels
		Expect(browser.WizardSetHostnameAndLabels("review-test", map[string]string{
			"env":  "review",
			"tier": "gold",
		})).To(Succeed())

		By("Navigating to Review and reading page content")
		Expect(browser.WizardClickNext()).To(Succeed()) // → Review

		reviewText, err := browser.WizardGetReviewText()
		Expect(err).ToNot(HaveOccurred())

		By("Verifying the proxy password is NOT shown in clear text")
		Expect(reviewText).ToNot(ContainSubstring("secret123"),
			"review page should not display proxy password in clear text")

		By("Verifying all collected configuration IS shown on the review page")
		// Hostname
		Expect(reviewText).To(ContainSubstring("review-test"), "hostname should appear on review")
		// Static IPv4 network config
		Expect(reviewText).To(ContainSubstring(slirpStaticIP), "static IPv4 address should appear on review")
		// NTP
		Expect(reviewText).To(ContainSubstring("time.example.com"), "NTP server should appear on review")
		// Proxy host and non-secret username (only the password is masked)
		Expect(reviewText).To(ContainSubstring("proxy.example.com"), "proxy host should appear on review")
		Expect(reviewText).To(ContainSubstring("proxyuser"), "proxy username should appear on review")
		// Labels
		Expect(reviewText).To(ContainSubstring("env"), "label key should appear on review")
		Expect(reviewText).To(ContainSubstring("review"), "label value should appear on review")
		Expect(reviewText).To(ContainSubstring("tier"), "label key should appear on review")
		Expect(reviewText).To(ContainSubstring("gold"), "label value should appear on review")
	})

	It("When apply fails it should allow navigating back, fixing, and re-applying", Label("90458"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Capturing the original hostname before the first apply")
		origOut, err := harness.VM.RunSSH([]string{"hostnamectl", "hostname"}, nil)
		Expect(err).ToNot(HaveOccurred())
		originalHostname := strings.TrimSpace(origOut.String())

		By("First attempt: selecting NIC (DHCP) and setting a hostname")
		// The nested VM has a single SLIRP NIC; reconfiguring its IP would sever the
		// SSH/cockpit control channel mid-apply (see slirpStaticIP). So the failure is
		// driven by the required connectivity test, not by a broken network profile.
		Expect(browser.WizardSelectNIC()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Network Services
		Expect(browser.WizardClickNext()).To(Succeed()) // → Enrollment
		Expect(browser.WizardDisableEnrollment()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Labels
		Expect(browser.WizardSetHostname("error-recovery-test")).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Review

		By("Forcing the apply to fail via a required, unresolvable connectivity host")
		// A hostname in the reserved .invalid TLD fails DNS resolution (a hostname,
		// unlike an IP, is resolved first). With the connectivity test left required
		// (the default), that makes the apply fail and exercises the recovery flow.
		Expect(browser.WizardSetConnectivityHost("connectivity-check.invalid")).To(Succeed())
		Expect(browser.WizardSetConnectivityRequired(true)).To(Succeed())
		Expect(browser.WizardClickApply()).To(Succeed())

		By("Verifying first attempt shows failure")
		Expect(browser.WizardWaitForFailure(wizardTimeout)).To(Succeed())

		By("Verifying applied changes were rolled back after the failure")
		// The installed onboarding package rolls back every applied step (including
		// hostname) when a required step fails: on failure the progress page builds a
		// rollback plan from the applied items and runs rollback-config.sh, which
		// restores the original hostname. So after a failed apply the hostname must
		// no longer be the attempted value; it is reverted to the pre-apply hostname.
		out, err := harness.VM.RunSSH([]string{"hostnamectl", "hostname"}, nil)
		Expect(err).ToNot(HaveOccurred())
		revertedHostname := strings.TrimSpace(out.String())
		Expect(revertedHostname).ToNot(Equal("error-recovery-test"),
			"hostname should have been rolled back after the failed apply")
		Expect(revertedHostname).To(Equal(originalHostname),
			"hostname should have been restored to its pre-apply value")

		By("Navigating back to the Review step to correct the connectivity setting")
		// Progress → Review = 1 click. The offending setting (the connectivity test)
		// lives on the Review step, so that is where we adjust and re-apply.
		Expect(browser.WizardNavigateBackwards(1)).To(Succeed())

		By("Second attempt: correcting connectivity and re-applying")
		Expect(browser.WizardSetConnectivityHost("127.0.0.1")).To(Succeed())
		Expect(browser.WizardSetConnectivityRequired(false)).To(Succeed())
		Expect(browser.WizardClickApply()).To(Succeed())
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())

		By("Verifying the corrected apply now applies the hostname")
		out, err = harness.VM.RunSSH([]string{"hostnamectl", "hostname"}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.TrimSpace(out.String())).To(Equal("error-recovery-test"))

		By("Verifying completion marker exists after successful re-apply")
		expectCompletionMarker(harness)
	})

	It("When only hostname is configured it should apply hostname", Label("90407"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Selecting NIC and navigating to Labels step")
		Expect(browser.WizardSelectNIC()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Network Services
		Expect(browser.WizardClickNext()).To(Succeed()) // → Enrollment
		Expect(browser.WizardDisableEnrollment()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Labels

		By("Setting hostname")
		Expect(browser.WizardSetHostname("hostname-only-test")).To(Succeed())

		By("Applying")
		Expect(browser.WizardClickNext()).To(Succeed()) // → Review
		Expect(browser.WizardSetConnectivityHost("127.0.0.1")).To(Succeed())
		Expect(browser.WizardSetConnectivityRequired(false)).To(Succeed())
		Expect(browser.WizardClickApply()).To(Succeed())
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())

		By("Verifying hostname was applied")
		out, err := harness.VM.RunSSH([]string{"hostnamectl", "hostname"}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(strings.TrimSpace(out.String())).To(Equal("hostname-only-test"))
	})

	It("When labels are configured it should write labels file with correct content", Label("90406"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Selecting NIC and navigating to Labels step")
		Expect(browser.WizardSelectNIC()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Network Services
		Expect(browser.WizardClickNext()).To(Succeed()) // → Enrollment
		Expect(browser.WizardDisableEnrollment()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Labels

		By("Setting hostname and adding labels")
		Expect(browser.WizardSetHostnameAndLabels("labels-test-host", map[string]string{
			"location": "factory-1",
			"tier":     "production",
			"os":       "centos",
		})).To(Succeed())

		By("Applying")
		Expect(browser.WizardClickNext()).To(Succeed()) // → Review
		Expect(browser.WizardSetConnectivityHost("127.0.0.1")).To(Succeed())
		Expect(browser.WizardSetConnectivityRequired(false)).To(Succeed())
		Expect(browser.WizardClickApply()).To(Succeed())
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())

		By("Verifying labels file was written with all key-value pairs")
		out, err := harness.VM.RunSSH([]string{
			"sudo", "cat", "/etc/flightctl/conf.d/50-cockpit-labels.yaml",
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		labelsContent := out.String()
		Expect(labelsContent).To(ContainSubstring("location"))
		Expect(labelsContent).To(ContainSubstring("factory-1"))
		Expect(labelsContent).To(ContainSubstring("tier"))
		Expect(labelsContent).To(ContainSubstring("production"))
		Expect(labelsContent).To(ContainSubstring("os"))
		Expect(labelsContent).To(ContainSubstring("centos"))
	})

	It("When static IPv4 is configured it should create a correct NM profile", Label("90411"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Selecting NIC and configuring static IPv4 with dual DNS")
		Expect(browser.WizardSelectNIC()).To(Succeed())
		// Use the SLIRP-matching address so the static profile keeps the guest
		// reachable (see slirpStaticIP). DNS values are arbitrary — they don't
		// affect SSH reachability — so keep two distinct servers to exercise dual DNS.
		Expect(browser.WizardConfigureStaticIPv4(
			slirpStaticIP, slirpStaticMask, slirpStaticGateway, "8.8.8.8",
		)).To(Succeed())
		Expect(browser.WizardConfigureSecondaryDNS("8.8.4.4")).To(Succeed())

		By("Applying")
		navigateFromNetworkToApply(browser)
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())

		By("Verifying NM profile type and method")
		profileName := getOnboardingNMProfileOfType(harness, "802-3-ethernet")

		out, err := harness.VM.RunSSH([]string{
			"nmcli", "-t", "-f", "connection.type", "con", "show", profileName,
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("802-3-ethernet"))

		out, err = harness.VM.RunSSH([]string{
			"nmcli", "-t", "-f", "ipv4.method", "con", "show", profileName,
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("manual"))

		By("Verifying IPv4 address, gateway, and DNS")
		out, err = harness.VM.RunSSH([]string{
			"nmcli", "-t", "-f", "ipv4.addresses", "con", "show", profileName,
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring(slirpStaticIP + "/24"))

		out, err = harness.VM.RunSSH([]string{
			"nmcli", "-t", "-f", "ipv4.gateway", "con", "show", profileName,
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring(slirpStaticGateway))

		out, err = harness.VM.RunSSH([]string{
			"nmcli", "-t", "-f", "ipv4.dns", "con", "show", profileName,
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		dnsOutput := out.String()
		Expect(dnsOutput).To(ContainSubstring("8.8.8.8"))
		Expect(dnsOutput).To(ContainSubstring("8.8.4.4"))

		By("Verifying profile is active")
		out, err = harness.VM.RunSSH([]string{
			"nmcli", "-t", "-f", "NAME", "con", "show", "--active",
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring(profileName))
	})

	It("When proxy is configured it should write proxy files correctly", Label("90409"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Selecting NIC and navigating to Network Services")
		Expect(browser.WizardSelectNIC()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Network Services

		By("Configuring proxy with authentication")
		// Throwaway proxy credentials typed into the wizard to exercise the
		// authenticated-proxy path; not a real secret.
		Expect(browser.WizardConfigureProxy("proxy.corp.com", "3128", "admin", "p@ss")).To(Succeed()) // gitleaks:allow -- dummy test credential

		By("Applying")
		Expect(browser.WizardClickNext()).To(Succeed()) // → Enrollment
		Expect(browser.WizardDisableEnrollment()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Labels
		Expect(browser.WizardClickNext()).To(Succeed()) // → Review
		Expect(browser.WizardSetConnectivityHost("127.0.0.1")).To(Succeed())
		Expect(browser.WizardSetConnectivityRequired(false)).To(Succeed())
		Expect(browser.WizardClickApply()).To(Succeed())
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())

		By("Verifying /etc/environment contains proxy variables")
		out, err := harness.VM.RunSSH([]string{"cat", "/etc/environment"}, nil)
		Expect(err).ToNot(HaveOccurred())
		envContent := out.String()
		// apply-proxy.sh writes uppercase variable names to /etc/environment
		// (HTTP_PROXY/HTTPS_PROXY/NO_PROXY), not the lowercase forms.
		Expect(envContent).To(ContainSubstring("HTTP_PROXY"))
		Expect(envContent).To(ContainSubstring("HTTPS_PROXY"))
		Expect(envContent).To(ContainSubstring("proxy.corp.com:3128"))
		Expect(envContent).To(ContainSubstring("NO_PROXY"))

		By("Verifying systemd proxy drop-in file exists")
		out, err = harness.VM.RunSSH([]string{
			"sudo", "cat", "/etc/systemd/system.conf.d/50-flightctl-onboarding-proxy.conf",
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		dropinContent := out.String()
		Expect(dropinContent).To(ContainSubstring("[Manager]"))
		Expect(dropinContent).To(ContainSubstring("DefaultEnvironment"))
		Expect(dropinContent).To(ContainSubstring("proxy.corp.com:3128"))
	})

	It("When VLAN is configured it should create a VLAN NM profile", Label("90412"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Selecting NIC and configuring VLAN")
		Expect(browser.WizardSelectNIC()).To(Succeed())
		Expect(browser.WizardConfigureVLAN("100")).To(Succeed())

		By("Applying")
		navigateFromNetworkToApply(browser)
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())

		By("Verifying NM profile is VLAN type")
		profileName := getOnboardingNMProfileOfType(harness, "vlan")

		out, err := harness.VM.RunSSH([]string{
			"nmcli", "-t", "-f", "connection.type", "con", "show", profileName,
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("vlan"))

		By("Verifying VLAN ID")
		out, err = harness.VM.RunSSH([]string{
			"nmcli", "-t", "-f", "vlan.id", "con", "show", profileName,
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("100"))
	})

	It("When NTP is configured it should update NTP configuration", Label("90416"), func() {
		harness := e2e.GetWorkerHarness()
		browser, cleanup := startBrowserSession()
		defer cleanup()

		By("Selecting NIC and navigating to Network Services")
		Expect(browser.WizardSelectNIC()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Network Services

		By("Configuring manual NTP server")
		Expect(browser.WizardConfigureNTP("time.example.com")).To(Succeed())

		By("Applying")
		Expect(browser.WizardClickNext()).To(Succeed()) // → Enrollment
		Expect(browser.WizardDisableEnrollment()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Labels
		Expect(browser.WizardClickNext()).To(Succeed()) // → Review
		Expect(browser.WizardSetConnectivityHost("127.0.0.1")).To(Succeed())
		Expect(browser.WizardSetConnectivityRequired(false)).To(Succeed())
		Expect(browser.WizardClickApply()).To(Succeed())
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())

		By("Verifying NTP config contains the configured server")
		expectNTPServer(harness, "time.example.com")

		By("Verifying NTP is active")
		out, err := harness.VM.RunSSH([]string{"timedatectl", "show", "--property=NTP"}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(out.String()).To(ContainSubstring("NTP=yes"))
	})

	It("When wizard has completed it should prevent re-running", Label("90454"), func() {
		workerID := GinkgoParallelProcess()
		sshPort := sshPortBase + workerID

		cockpitAddr, tunnelCleanup, err := e2e.StartCockpitTunnel(sshPort, vmUser, vmPassword)
		Expect(err).ToNot(HaveOccurred())
		defer tunnelCleanup()

		// This spec drives two browser sessions over the same tunnel, so it manages
		// the tunnel itself and creates each session with the shared helper rather
		// than using startBrowserSession (which bundles one browser with the tunnel).
		browser := newLoggedInBrowser(cockpitAddr)
		defer browser.Close()

		By("Running a minimal wizard flow to completion")
		Expect(browser.WizardSelectNIC()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Network Services
		Expect(browser.WizardClickNext()).To(Succeed()) // → Enrollment
		Expect(browser.WizardDisableEnrollment()).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Labels
		Expect(browser.WizardSetHostname("completion-marker-test")).To(Succeed())
		Expect(browser.WizardClickNext()).To(Succeed()) // → Review
		Expect(browser.WizardSetConnectivityHost("127.0.0.1")).To(Succeed())
		Expect(browser.WizardSetConnectivityRequired(false)).To(Succeed())
		Expect(browser.WizardClickApply()).To(Succeed())
		Expect(browser.WizardWaitForCompletion(wizardTimeout)).To(Succeed())

		By("Opening a new browser session to the same VM")
		browser.Close()

		browser2 := newLoggedInBrowser(cockpitAddr)
		defer browser2.Close()
		// Registered after browser2.Close() so it runs first (LIFO) and captures
		// the live browser before its context is cancelled.
		defer saveScreenshotOnFailure(browser2, "already-complete")

		By("Verifying the wizard shows the already-complete message")
		found, err := browser2.WizardIsAlreadyComplete()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue(), "expected #system-onboarding-already-complete to be visible")
	})
})
