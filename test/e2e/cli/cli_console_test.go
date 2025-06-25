package cli_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	"github.com/sirupsen/logrus"
)

// -----------------------------------------------------------------------------
// Console test-suite
// -----------------------------------------------------------------------------

var _ = Describe("CLI - device console", Serial, func() {
	var (
		ctx      context.Context
		harness  *e2e.Harness
		deviceID string
	)

	BeforeEach(func() {
		ctx = util.StartSpecTracerForGinkgo(suiteCtx)
		harness = e2e.NewTestHarness(ctx)
		login.LoginToAPIWithToken(harness)

		By("booting a VM and enrolling the device")
		deviceID = harness.StartVMAndEnroll()
	})

	AfterEach(func() { harness.Cleanup(false) })

	It("connects to a device and executes a simple command", Label("80483", "sanity"), func() {
		cs := harness.NewConsoleSession(deviceID)
		cs.MustSend("ls")
		cs.MustExpect(".*bin")
		cs.Close()
	})

	It("supports multiple simultaneous console sessions", Label("81737", "sanity"), func() {
		cs1 := harness.NewConsoleSession(deviceID)
		cs2 := harness.NewConsoleSession(deviceID)

		cs2.MustSend("pwd")
		cs2.MustExpect("/")

		cs1.MustSend("echo Session1 > /var/home/user/file.txt")
		cs2.MustSend("cat /var/home/user/file.txt")
		cs2.MustExpect("Session1")

		cs2.MustSend("echo Session2 >> /var/home/user/file.txt")
		cs1.MustSend("cat /var/home/user/file.txt")
		cs1.MustExpect("(?s).*Session1.*Session2.*")

		cs1.Close()
		cs2.Close()
	})

	It("keeps console sessions open during a device update", Label("81786", "sanity"), func() {
		const sessionsToOpen = 4
		const expectedRenderedVersion = 2 + sessionsToOpen*2

		// kick off an update
		device, _ := harness.WaitForBootstrapAndUpdateToVersion(deviceID, ":v4")
		Eventually(resources.GetJSONByName[*v1alpha1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			Should(WithTransform((*v1alpha1.Device).IsUpdating, BeTrue()))
		Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusOnline))

		sessions := make([]*e2e.ConsoleSession, 0, sessionsToOpen)
		for i := range sessionsToOpen {
			By(fmt.Sprintf("opening console session %d/%d", i+1, sessionsToOpen))
			sessions = append(sessions, harness.NewConsoleSession(deviceID))
		}
		for i, cs := range sessions {
			By(fmt.Sprintf("closing console session %d/%d", i+1, sessionsToOpen))
			cs.Close()
		}

		By("waiting for the update to finish")
		eventuallySlow(resources.GetJSONByName[*v1alpha1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			Should(WithTransform((*v1alpha1.Device).IsUpdatedToDeviceSpec, BeTrue()))

		// ensure applications become healthy
		Eventually(resources.GetJSONByName[*v1alpha1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			Should(WithTransform(func(d *v1alpha1.Device) v1alpha1.ApplicationsSummaryStatusType {
				return d.Status.ApplicationsSummary.Status
			}, Equal(v1alpha1.ApplicationsSummaryStatusHealthy)))

		Expect(harness.GetCurrentDeviceRenderedVersion(deviceID)).To(Equal(expectedRenderedVersion))

		By("returns a helpful error when the device is not found")
		out, err := harness.CLI("console", "device/nonexistent")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("not found"))
	})

	It("allows tuning spec-fetch-interval", Label("82538", "sanity"), func() {
		const (
			cfgFile              = "/etc/flightctl/config.yaml"
			specFetchKey         = "spec-fetch-interval"
			specFetchIntervalSec = 20
			rootPwd              = "user"
		)

		sendAsRoot := func(cs *e2e.ConsoleSession, cmd string) {
			cs.MustSend(fmt.Sprintf("echo '%s' | sudo -S %s", rootPwd, cmd))
		}

		cs := harness.NewConsoleSession(deviceID)

		// show current config & ensure the key is present
		sendAsRoot(cs, "cat "+cfgFile)
		cs.MustExpect(specFetchKey)

		// patch config
		sedExpr := fmt.Sprintf("sed -i -E 's/%s: .+m.+s/%s: 0m%ds/g' %s && cat %s", specFetchKey, specFetchKey,
			specFetchIntervalSec, cfgFile, cfgFile)
		sendAsRoot(cs, sedExpr)
		cs.MustExpect(fmt.Sprintf("%s: 0m%ds", specFetchKey, specFetchIntervalSec))
		sendAsRoot(cs, fmt.Sprintf("sh -c \"echo 'log-level: debug' >> %s\" && cat %s", cfgFile, cfgFile))
		cs.MustExpect("log-level: debug")

		sendAsRoot(cs, "systemctl restart flightctl-agent")
		cs.Close()

		By("waiting for publisher logs with the new interval")
		// Wait for the target log messages to appear
		eventuallySlow(harness.ReadPrimaryVMAgentLogs).
			WithArguments("2 minutes ago").
			Should(And(
				ContainSubstring("No new template version from management service"),
				ContainSubstring("publisher.go"),
			))

		// Now validate the timing intervals
		logWithTsRe := regexp.MustCompile(`.*time="([^"]+).*No new template version from management service.*publisher\.go.*"`)
		Eventually(func() bool {
			logs, err := harness.ReadPrimaryVMAgentLogs("2 minutes ago")
			Expect(err).ToNot(HaveOccurred())

			lines := strings.Split(strings.TrimSpace(logs), "\n")
			logrus.Infof("Read %d log lines from agent journal", len(lines))

			var validTimestamps []time.Time

			for _, line := range lines {
				if m := logWithTsRe.FindStringSubmatch(line); m != nil {
					if t, err := time.Parse(time.RFC3339Nano, m[1]); err == nil {
						validTimestamps = append(validTimestamps, t)
						logrus.Infof("Found matching log line with timestamp: %s", t.Format(time.RFC3339))
					} else {
						logrus.Warnf("Failed to parse timestamp %q: %v", m[1], err)
					}
				}
			}

			logrus.Infof("Found %d lines matching the pattern", len(validTimestamps))

			if len(validTimestamps) < 2 {
				logrus.Infof("Need at least 2 matching timestamps, only have %d - waiting for more logs", len(validTimestamps))
				return false
			}

			logrus.Infof("Validating intervals between %d timestamps", len(validTimestamps))
			for i := 1; i < len(validTimestamps); i++ {
				delta := validTimestamps[i].Sub(validTimestamps[i-1])
				expectedInterval := specFetchIntervalSec * time.Second
				deviation := (delta - expectedInterval).Abs()

				logrus.Infof("Timestamp %d->%d: delta=%v, expected=%v, deviation=%v",
					i-1, i, delta, expectedInterval, deviation)

				if deviation > time.Second {
					logrus.Infof("Interval not yet stable - deviation %v > 1s threshold", deviation)
					return false // interval not yet stable
				}
			}

			logrus.Infof("All %d intervals are stable within 1s tolerance", len(validTimestamps)-1)
			return true
		}, 2*time.Minute, 10*time.Second).Should(BeTrue())
	})

	It("recovers from image pull failures", Label("82541", "sanity"), func() {
		_, _ = harness.WaitForBootstrapAndUpdateToVersion(deviceID, ":v4")

		Eventually(resources.GetJSONByName[*v1alpha1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			Should(WithTransform((*v1alpha1.Device).IsUpdating, BeTrue()))

		err := harness.SimulateNetworkFailure()
		Expect(err).ToNot(HaveOccurred())

		// Use the new journalctl logging function to wait for image pull activity
		eventuallySlow(harness.ReadPrimaryVMAgentLogs).
			WithArguments("2 minutes ago").
			Should(ContainSubstring("Pulling image"))

		logrus.Infof("Waiting for image pull failure. It will take a while...")

		// Wait for the retriable error in the logs
		eventuallySlow(harness.ReadPrimaryVMAgentLogs).
			WithArguments("2 minutes ago").
			Should(ContainSubstring("retriable error"))

		err = harness.FixNetworkFailure()
		Expect(err).ToNot(HaveOccurred())

		eventuallySlow(resources.GetJSONByName[*v1alpha1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			Should(WithTransform((*v1alpha1.Device).IsUpdatedToDeviceSpec, BeTrue()))
	})

	It("provides console --help and auxiliary features", Label("81866", "sanity"), func() {
		Expect(harness.CLI("console", "--help")).To(
			And(
				ContainSubstring("Usage:"),
				ContainSubstring("flightctl console device/NAME [-- COMMAND [ARG...]]"),
			),
		)

		By("verifying that the ~. sequence exits the shell")
		cs := harness.NewConsoleSession(deviceID)
		Expect(cs.Stdin.Write([]byte("\n~.\n"))).To(BeNumerically(">", 0))
		Eventually(cs.Stdout.Closed).Should(BeTrue())

		By("running a command without opening a shell")
		Expect(harness.CLI("console", "device/"+deviceID, "--", "flightctl-agent", "system-info")).
			To(ContainSubstring("hostname"))

		By("running a background command without a TTY")
		Expect(harness.CLI("console", "device/"+deviceID, "--notty", "--", "pwd")).
			To(ContainSubstring("/"))

		By("generating a remote sos-report")
		// "sos: command not found" when running "console device/{device} -- sos" in a non-interactive shell. a bug?
		Expect(harness.CLI("console", "device/"+deviceID, "--", "/usr/sbin/sos", "report", "--batch", "--quiet")).
			To(ContainSubstring("sos report has been generated"))

		By("failing when required command args are missing")
		out, err := harness.CLI("console", "--tty")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("Error:"))
	})
})

func eventuallySlow(actual any) types.AsyncAssertion {
	return Eventually(actual).WithTimeout(LONG_TIMEOUT).WithPolling(LONG_POLLING)
}
