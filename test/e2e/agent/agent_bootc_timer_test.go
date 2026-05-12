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
	It("should automatically mask bootc timer on installation", Label("bootc-timer", "sanity", "agent"), func() {
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
		// Check for the actual mask symlink pointing to /dev/null
		stdout, err = harness.VM.RunSSH([]string{"sh", "-c", "readlink /etc/systemd/system/" + bootcTimerUnit + " 2>/dev/null || echo ''"}, nil)
		Expect(err).ToNot(HaveOccurred())
		symlinkTarget := strings.TrimSpace(stdout.String())
		Expect(symlinkTarget).To(Equal("/dev/null"), "bootc timer should be masked (symlink to /dev/null)")
	})
})
