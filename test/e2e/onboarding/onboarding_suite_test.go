//go:build linux

package onboarding_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

const (
	onboardingRPMEnv     = "ONBOARDING_RPM_PATH"
	onboardingSnapshotID = "pre-onboarding"
)

// suiteHarness holds the fresh VM harness created in BeforeSuite so AfterSuite
// can tear it down. CreateFreshVMWithTPM does not register the VM in the pool
// maps, so pool-level cleanup would never see it; we must destroy it explicitly.
var suiteHarness *e2e.Harness

// dnfInstallFlags make dnf installs non-interactive and robust inside the test VM.
// The VM's SSH sessions have no stdin, so any dnf prompt (transaction confirm or
// GPG key import) yields EOF and dnf aborts with "Operation aborted." even with -y.
//   - -y: assume yes for the transaction
//   - --transient: the device image is a read-only bootc system; a normal dnf
//     install aborts ("This bootc system is configured to be read-only"). --transient
//     writes into an in-memory overlay. The suite never reboots the VM between install
//     and the tests, and the pre-onboarding snapshot captures VM memory (external
//     snapshot with <memory>), so the transient packages survive snapshot reverts.
//   - --nogpgcheck: skip GPG key import prompts (acceptable for an ephemeral test VM)
//   - --disableplugin=subscription-manager: silence "Unable to read consumer identity"
//     on the unregistered CentOS Stream base image
//   - install_weak_deps=False: skip optional deps (PackageKit, cockpit-storaged,
//     webkit2gtk3-jsc, etc.) the onboarding wizard does not need
var dnfInstallFlags = []string{
	"-y",
	"--transient",
	"--nogpgcheck",
	"--disableplugin=subscription-manager",
	"--setopt=install_weak_deps=False",
}

func TestOnboarding(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Onboarding E2E Suite")
}

var _ = BeforeSuite(func() {
	// The onboarding suite only needs the registry (for agent image bundles and
	// enrollment). It does not use the git server, so start a scoped set to avoid
	// building the git-server image, whose local Dockerfile context can fail to
	// unpack under restrictive rootless subuid ranges.
	auxFuture := e2e.StartAuxServicesAsyncWith(context.Background(), auxiliary.ServiceRegistry)
	defer auxFuture.Wait()

	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	Expect(e2e.AgentConfigDirExists()).To(BeTrue(),
		"agent config dir must exist (bin/agent/etc/flightctl); run make prepare-e2e-test first")

	workerID := GinkgoParallelProcess()

	// Create a fresh VM (booted, SSH ready) without starting the agent.
	harness, err := setupVMOnlyHarness(workerID)
	Expect(err).ToNot(HaveOccurred(), "failed to create fresh VM for onboarding suite")
	suiteHarness = harness

	logrus.Infof("Worker %d: fresh VM created, installing cockpit + onboarding RPM", workerID)
	installCockpitAndOnboarding(harness)

	// Scrub any pre-onboarding agent enrollment before snapshotting so the snapshot
	// is pristine (see resetAgentEnrollmentState for why this matters).
	resetAgentEnrollmentState(harness)

	// Install the WiFi soft-AP stack so the WiFi specs run on the standard e2e
	// device image (no dedicated baked image). Done before the snapshot so the
	// transient packages are captured by the memory snapshot and survive reverts.
	logrus.Infof("Worker %d: installing WiFi soft-AP stack", workerID)
	installWifiStack(harness)

	// Take a snapshot so each test starts from a clean post-install state.
	logrus.Infof("Worker %d: creating %s snapshot", workerID, onboardingSnapshotID)
	err = harness.VM.CreateSnapshot(onboardingSnapshotID)
	Expect(err).ToNot(HaveOccurred(), "failed to create pre-onboarding snapshot")
	logrus.Infof("Worker %d: onboarding suite setup complete", workerID)
})

var _ = AfterSuite(func() {
	// The fresh VM is created outside the normal pool (CreateFreshVMWithTPM does
	// not populate the pool maps), so pool cleanup will not reach it. Destroy it
	// explicitly. e2e.Cleanup both force-deletes the VM and removes any pool
	// bookkeeping for it.
	if suiteHarness == nil {
		return
	}
	e2e.Cleanup(suiteHarness)
	suiteHarness = nil
})

var _ = BeforeEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	_, err := login.LoginToAPIWithToken(harness)
	Expect(err).ToNot(HaveOccurred())

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	// Revert to the pre-onboarding snapshot for test isolation.
	err = harness.VM.RevertToSnapshot(onboardingSnapshotID)
	Expect(err).ToNot(HaveOccurred(), "failed to revert to pre-onboarding snapshot")
	err = harness.VM.WaitForSSHToBeReady()
	Expect(err).ToNot(HaveOccurred(), "SSH not ready after snapshot revert")

	if syncErr := harness.SyncVMClock(); syncErr != nil {
		logrus.Warnf("failed to sync VM clock after snapshot revert: %v", syncErr)
	}
})

var _ = AfterEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()
	defer harness.SetTestContext(suiteCtx)

	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()
})

// resetAgentEnrollmentState scrubs any flightctl-agent enrollment the base image
// left behind on first boot, so the pre-onboarding snapshot is pristine.
//
// The base device image ships flightctl-agent enabled, so on first boot — before
// the onboarding RPM installs the gate drop-in that keeps the agent from starting
// until onboarding is confirmed — the agent runs, generates a CSR, and creates an
// EnrollmentRequest, logging an "/enroll/<id>" line to the persistent journal. If
// that state leaks into the snapshot, every reverted spec inherits it, and the
// damage is subtle: GetEnrollmentIDFromServiceLogs derives the device ID from the
// FIRST "/enroll/<id>" match in the agent journal (util.GetEnrollmentIdFromText),
// so it returns the STALE baked-in id rather than the current wizard-driven
// enrollment. The enrollment specs then approve the wrong device while their real
// device is never approved, and they time out in WaitForOnlineStatus.
//
// Stop the agent, remove its on-disk enrollment identity, and rotate+vacuum the
// journal so no stale "/enroll/<id>" survives. After this the gate drop-in keeps
// the agent inactive across reverts until the wizard starts it, so the only
// "/enroll/<id>" any spec ever sees is the one its own wizard run produced. All
// commands are plain argv (RunSSH re-joins args for the remote shell, so a
// `bash -c "<multi-word>"` would collapse to its first word).
func resetAgentEnrollmentState(h *e2e.Harness) {
	// Best-effort stop: the agent may already be gated/inactive.
	_, _ = h.VM.RunSSH([]string{"sudo", "systemctl", "stop", "flightctl-agent"}, nil)

	// Remove the enrollment identity and spec files the first-boot agent persisted.
	// Surface a failure here: if the stale state survives, every enrollment spec
	// then approves the wrong device and times out in WaitForOnlineStatus — exactly
	// the opaque failure this function exists to prevent, so don't discard the error.
	if _, err := h.VM.RunSSH([]string{
		"sudo", "rm", "-rf",
		"/var/lib/flightctl/certs",
		"/var/lib/flightctl/current.json",
		"/var/lib/flightctl/desired.json",
		"/var/lib/flightctl/rollback.json",
		"/var/lib/flightctl/system.json",
	}, nil); err != nil {
		logrus.Warnf("failed to remove persisted agent enrollment state: %v", err)
	}

	// Rotate then vacuum the persistent journal so the stale "/enroll/<id>" line is
	// gone. Rotating first seals the active journal file so the subsequent vacuum can
	// actually remove the archived records that hold the stale enrollment line.
	for _, argv := range [][]string{
		{"sudo", "journalctl", "--rotate"},
		{"sudo", "journalctl", "--vacuum-time=1s"},
	} {
		if _, err := h.VM.RunSSH(argv, nil); err != nil {
			logrus.Warnf("failed to clear stale journal enrollment records (%v): %v", argv, err)
		}
	}
}

// setupVMOnlyHarness creates a worker harness backed by a fresh VM that is
// booted and reachable via SSH but does NOT start the flightctl-agent.
// The onboarding wizard is responsible for configuring and starting the agent.
func setupVMOnlyHarness(workerID int) (*e2e.Harness, error) {
	suiteCtx := context.Background()
	harness, err := e2e.NewTestHarnessWithVMOnly(suiteCtx, workerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM-only harness for worker %d: %w", workerID, err)
	}

	// The suite context is stored for later retrieval via e2e.GetWorkerContext();
	// callers do not need it back from here.
	e2e.StoreWorkerHarness(workerID, harness, suiteCtx)
	return harness, nil
}

// installCockpitAndOnboarding installs cockpit, cockpit-ws, cockpit-bridge,
// and the flightctl-onboarding RPM into the VM. It also enables the
// cockpit.socket systemd unit so the wizard is reachable on port 9090.
func installCockpitAndOnboarding(h *e2e.Harness) {
	_, err := h.VM.RunSSH(append([]string{
		"sudo", "dnf", "install",
	}, append(dnfInstallFlags,
		"cockpit", "cockpit-ws", "cockpit-bridge")...), nil)
	Expect(err).ToNot(HaveOccurred(), "failed to install cockpit packages")

	rpmPath := os.Getenv(onboardingRPMEnv)
	if rpmPath != "" {
		rpmData, err := os.ReadFile(rpmPath)
		Expect(err).ToNot(HaveOccurred(), "failed to read onboarding RPM from %s", rpmPath)

		remotePath := "/tmp/flightctl-onboarding.rpm"
		_, err = h.VM.RunSSH(
			[]string{"sudo", "tee", remotePath},
			bytes.NewBuffer(rpmData),
		)
		Expect(err).ToNot(HaveOccurred(), "failed to copy onboarding RPM to VM")

		_, err = h.VM.RunSSH(append([]string{
			"sudo", "dnf", "install",
		}, append(dnfInstallFlags, remotePath)...), nil)
		Expect(err).ToNot(HaveOccurred(), "failed to install onboarding RPM")
	} else {
		// Enable the flightctl-dev COPR and install the package.
		_, err = h.VM.RunSSH([]string{
			"sudo", "dnf", "copr", "-y", "enable", "@redhat-et/flightctl-dev",
		}, nil)
		Expect(err).ToNot(HaveOccurred(), "failed to enable flightctl-dev COPR repo")

		_, err = h.VM.RunSSH(append([]string{
			"sudo", "dnf", "install",
		}, append(dnfInstallFlags, "flightctl-onboarding")...), nil)
		Expect(err).ToNot(HaveOccurred(),
			fmt.Sprintf("failed to install flightctl-onboarding from COPR (set %s to use a local RPM)", onboardingRPMEnv))
	}

	_, err = h.VM.RunSSH([]string{
		"sudo", "systemctl", "enable", "--now", "cockpit.socket",
	}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to enable cockpit.socket")

	// Create the onboarding user and apply the wizard's Cockpit config (polkit rule,
	// cockpit.conf, module overrides). We deliberately do NOT start the full
	// cockpit-system-onboarding-setup.service: it is ordered After=network-online.target
	// (so `systemctl start` blocks on NetworkManager-wait-online) and also runs
	// setup-network.sh, which reassigns the primary NIC to a well-known static IP
	// (192.168.100.1) and would sever our SSH access to the VM, plus setup-wifi-ap.sh
	// (no WiFi hardware). The config-flow wizard only needs the Cockpit plugin,
	// cockpit.socket, the onboarding user, and its polkit authorization, so we run just
	// create-onboarding-user.sh directly.
	//
	// The onboarding user MUST exist and the tests MUST log in to Cockpit as it: the
	// wizard's privileged apply operations are authorized by the polkit rule only for
	// subject.user == "onboarding". This is not optional, so failures here are fatal.
	// In production the onboarding account is left passwordless (Cockpit is configured
	// for passwordless login). For a deterministic headless-Chrome login we need a known
	// password. create-onboarding-user.sh reads .onboardingUser.password from
	// /etc/cockpit/system-onboarding/config.json and, if set, runs chpasswd itself as part
	// of the same root sequence as useradd/passwd. We write that config BEFORE running the
	// script rather than issuing a separate chpasswd afterward: a standalone chpasswd races
	// the account-lock (/etc/.pwd.lock) that useradd's follow-on work briefly holds and
	// fails with "cannot lock /etc/passwd". Setting a password does not affect polkit
	// authorization (which keys on the username) and SSH stays blocked via DenyUsers.
	const onboardingConfigDir = "/etc/cockpit/system-onboarding"
	const onboardingConfigPath = onboardingConfigDir + "/config.json"
	onboardingConfig := fmt.Sprintf(`{"onboardingUser":{"password":%q}}`, cockpitPassword)
	_, err = h.VM.RunSSH([]string{"sudo", "mkdir", "-p", onboardingConfigDir}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to create onboarding config dir")
	// Redirect tee's stdout to /dev/null: tee echoes its stdin (the password JSON)
	// to stdout, and RunSSH folds stdout into its error message, so on failure the
	// password would otherwise leak into the Ginkgo failure output.
	_, err = h.VM.RunSSH([]string{"sudo", "tee", onboardingConfigPath, ">", "/dev/null"}, bytes.NewBufferString(onboardingConfig))
	Expect(err).ToNot(HaveOccurred(), "failed to write onboarding config with password")

	const createUserScript = "/usr/libexec/flightctl-onboarding/create-onboarding-user.sh"
	_, err = h.VM.RunSSH([]string{"sudo", createUserScript}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to create onboarding user (required for polkit-authorized apply)")

	// Verify cockpit is listening. `ss` exits 0 even when no socket matches the
	// filter, so asserting only on the exit code would pass with nothing on 9090
	// and every spec would then fail later inside Chrome with an opaque tunnel or
	// login error. Assert on the output, and poll, because socket activation can
	// lag `systemctl enable --now`.
	Eventually(func() (string, error) {
		out, err := h.VM.RunSSH([]string{
			"sudo", "ss", "-tlnH", "sport", "=", ":9090",
		}, nil)
		if err != nil {
			return "", err
		}
		return out.String(), nil
	}, 60*time.Second, 2*time.Second).Should(ContainSubstring(":9090"),
		"cockpit is not listening on port 9090")
}

// installWifiStack transiently installs the WiFi soft-AP userspace and the
// kernel module needed to synthesize virtual radios, so the WiFi specs run on
// the standard e2e device image without a dedicated baked image. It mirrors the
// EDM-4205 feasibility spike:
//   - kernel-modules-extra must match the running kernel exactly. $(uname -r) is
//     expanded by the remote shell (RunSSH space-joins argv into one shell
//     command line); every argument here is a fixed literal, so nothing
//     untrusted is interpolated.
//   - depmod -a registers the freshly-dropped mac80211_hwsim so the per-spec
//     `modprobe mac80211_hwsim radios=2` (loadHwsimRadios) can find it.
//
// Called from BeforeSuite before the snapshot so the transient packages (written
// to the in-memory overlay by --transient) are captured by the memory snapshot
// and persist across every per-spec revert.
func installWifiStack(h *e2e.Harness) {
	_, err := h.VM.RunSSH(append([]string{
		"sudo", "dnf", "install",
	}, append(dnfInstallFlags,
		"kernel-modules-extra-$(uname -r)",
		"hostapd", "wpa_supplicant", "NetworkManager-wifi", "iw", "dnsmasq")...), nil)
	Expect(err).ToNot(HaveOccurred(), "failed to install WiFi soft-AP stack")

	// Rebuild modules.dep so modprobe can find the just-installed mac80211_hwsim.
	_, err = h.VM.RunSSH([]string{"sudo", "depmod", "-a"}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to run depmod after installing kernel-modules-extra")
}
