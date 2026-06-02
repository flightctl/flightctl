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
)

var _ = Describe("Bootc timer masking", func() {
	It("should automatically mask bootc timer on installation", Label("89237", "bootc-timer", "sanity", "agent"), func() {
		harness := e2e.GetWorkerHarness()

		By("Enrolling a device")
		deviceName := harness.StartVMAndEnroll()

		By("Waiting for device to come online")
		_, err := harness.CheckDeviceStatus(deviceName, v1beta1.DeviceSummaryStatusOnline)
		Expect(err).ToNot(HaveOccurred())

		By("Checking if bootc timer file exists on the device")
		stdout, err := harness.VM.RunSSH([]string{"sh", "-c", "test -f " + bootcTimerUnitFile + " && echo exists || echo not-exists"}, nil)
		Expect(err).ToNot(HaveOccurred())
		timerFileExists := strings.TrimSpace(stdout.String()) == "exists"

		if !timerFileExists {
			Skip("Bootc timer unit file does not exist on this device (CentOS Stream doesn't include bootc timer, only RHEL does)")
		}

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
