package agent_test

import (
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	bootcTimerUnit     = "bootc-fetch-apply-updates.timer"
	bootcTimerUnitFile = "/usr/lib/systemd/system/bootc-fetch-apply-updates.timer"
	// Composefs-safe probe: unit file path, find under /usr/lib/systemd, or list-unit-files.
	bootcTimerDetectShell = "test -f " + bootcTimerUnitFile + " && echo exists || " +
		"(find /usr/lib/systemd -name 'bootc-fetch-apply-updates.timer' -quit 2>/dev/null | grep -q . && echo exists) || " +
		"(systemctl list-unit-files 'bootc-fetch-apply-updates.timer' 2>/dev/null | grep -q '^bootc-fetch-apply-updates.timer' && echo exists) || echo not-exists"
)

var _ = Describe("Bootc timer masking", func() {
	It("should automatically mask bootc timer on installation", Label("89237", "bootc-timer", "sanity", "agent"), func() {
		harness := e2e.GetWorkerHarness()

		By("Checking if bootc timer unit exists on the e2e device image")
		stdout, err := harness.VM.RunSSH([]string{"sh", "-c", bootcTimerDetectShell}, nil)
		Expect(err).ToNot(HaveOccurred())
		if strings.TrimSpace(stdout.String()) != "exists" {
			Skip("bootc timer unit not present on this e2e device VM (need bootc-based disk.qcow2 from make e2e-agent-images; non-bootc images skip)")
		}

		By("Enrolling a device")
		deviceName := harness.StartVMAndEnroll()

		By("Waiting for device to come online")
		_, err = harness.CheckDeviceStatus(deviceName, v1beta1.DeviceSummaryStatusOnline)
		Expect(err).ToNot(HaveOccurred())

		By("Verifying bootc timer is masked after agent installation")
		// Use a single SSH call that produces enough output to survive SLIRP's small TCP window
		// (a bare readlink over user-mode QEMU networking can silently drop its ~8-byte reply).
		stdout, err = harness.VM.RunSSH([]string{"sh", "-c",
			"ls -la /etc/systemd/system/" + bootcTimerUnit + " 2>&1 && " +
				"echo SYMLINK_TARGET=$(readlink /etc/systemd/system/" + bootcTimerUnit + " 2>/dev/null)"}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(stdout.String()).To(ContainSubstring("/dev/null"), "bootc timer should be masked (symlink to /dev/null)")
	})
})
