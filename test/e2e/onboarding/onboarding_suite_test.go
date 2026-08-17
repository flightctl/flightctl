//go:build linux

package onboarding_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"

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
	harness, ctx, err := setupVMOnlyHarness(workerID)
	Expect(err).ToNot(HaveOccurred(), "failed to create fresh VM for onboarding suite")
	_ = ctx

	logrus.Infof("Worker %d: fresh VM created, installing cockpit + onboarding RPM", workerID)
	installCockpitAndOnboarding(harness)

	// Take a snapshot so each test starts from a clean post-install state.
	logrus.Infof("Worker %d: creating %s snapshot", workerID, onboardingSnapshotID)
	err = harness.VM.CreateSnapshot(onboardingSnapshotID)
	Expect(err).ToNot(HaveOccurred(), "failed to create pre-onboarding snapshot")
	logrus.Infof("Worker %d: onboarding suite setup complete", workerID)
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

// setupVMOnlyHarness creates a worker harness backed by a fresh VM that is
// booted and reachable via SSH but does NOT start the flightctl-agent.
// The onboarding wizard is responsible for configuring and starting the agent.
func setupVMOnlyHarness(workerID int) (*e2e.Harness, context.Context, error) {
	suiteCtx := context.Background()
	harness, err := e2e.NewTestHarnessWithVMOnly(suiteCtx, workerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create VM-only harness for worker %d: %w", workerID, err)
	}

	e2e.StoreWorkerHarness(workerID, harness, suiteCtx)
	return harness, suiteCtx, nil
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
	_, err = h.VM.RunSSH([]string{"sudo", "tee", onboardingConfigPath}, bytes.NewBufferString(onboardingConfig))
	Expect(err).ToNot(HaveOccurred(), "failed to write onboarding config with password")

	const createUserScript = "/usr/libexec/flightctl-onboarding/create-onboarding-user.sh"
	_, err = h.VM.RunSSH([]string{"sudo", createUserScript}, nil)
	Expect(err).ToNot(HaveOccurred(), "failed to create onboarding user (required for polkit-authorized apply)")

	// Verify cockpit is listening.
	out, err := h.VM.RunSSH([]string{
		"sudo", "ss", "-tlnp", "sport", "=", ":9090",
	}, nil)
	Expect(err).ToNot(HaveOccurred(), "cockpit not listening on port 9090")
	logrus.Infof("cockpit listening: %s", out.String())
}
