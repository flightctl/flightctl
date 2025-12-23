package cli_test

import (
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Test constants
const (
	logLookbackDuration = "10 minutes ago"
)

// -----------------------------------------------------------------------------
// Console test-suite
// -----------------------------------------------------------------------------

var _ = Describe("CLI - device console", func() {
	var (
		deviceID string
	)

	BeforeEach(func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()

		login.LoginToAPIWithToken(harness)

		By("enrolling the device")
		deviceID, _ = harness.EnrollAndWaitForOnlineStatus()
	})

	It("connects to a device and executes a simple command", Label("80483", "sanity"), func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()

		cs := harness.NewConsoleSession(deviceID)
		cs.MustSend("pwd")
		cs.MustExpect("/root")
		cs.Close()
	})

	It("supports multiple simultaneous console sessions", Label("81737", "sanity"), func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()

		cs1 := harness.NewConsoleSession(deviceID)
		cs2 := harness.NewConsoleSession(deviceID)

		cs2.MustSend("pwd")
		cs2.MustExpect("/root")

		cs1.MustSend("echo Session1 > /var/home/user/file.txt")
		cs2.MustSend("cat /var/home/user/file.txt")
		cs2.MustExpect("Session1")

		cs2.MustSend("echo Session2 >> /var/home/user/file.txt")
		cs1.MustSend("cat /var/home/user/file.txt")
		cs1.MustExpect("(?s).*Session1.*Session2.*")

		cs1.Close()
		cs2.Close()
	})

	It("keeps console sessions open during a device update", Label("81786"), func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()

		const sessionsToOpen = 4
		const expectedRenderedVersion = 2 + sessionsToOpen*2

		// kick off an update
		device, _, err := harness.WaitForBootstrapAndUpdateToVersion(deviceID, util.DeviceTags.V4)
		Expect(err).ToNot(HaveOccurred())
		Eventually(resources.GetJSONByName[*v1beta1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			Should(WithTransform((*v1beta1.Device).IsUpdating, BeTrue()))
		Expect(device.Status.Summary.Status).To(Equal(v1beta1.DeviceSummaryStatusOnline))

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
		util.EventuallySlow(resources.GetJSONByName[*v1beta1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			Should(WithTransform((*v1beta1.Device).IsUpdatedToDeviceSpec, BeTrue()))

		// ensure applications become healthy
		Eventually(resources.GetJSONByName[*v1beta1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			Should(WithTransform(func(d *v1beta1.Device) v1beta1.ApplicationsSummaryStatusType {
				return d.Status.ApplicationsSummary.Status
			}, Equal(v1beta1.ApplicationsSummaryStatusHealthy)))

		currentRenderedVersion, err := harness.GetCurrentDeviceRenderedVersion(deviceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(currentRenderedVersion).To(Equal(expectedRenderedVersion))

		By("returns a helpful error when the device is not found")
		out, err := harness.CLI("console", "device/nonexistent")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("not found"))
	})

	// Commenting since this feature is deprecated
	// It("allows tuning spec-fetch-interval", Label("82538"), func() {
	// 	// Get harness directly - no shared package-level variable
	// 	harness := e2e.GetWorkerHarness()

	// 	const (
	// 		cfgFile              = "/etc/flightctl/config.yaml"
	// 		specFetchKey         = "spec-fetch-interval"
	// 		specFetchIntervalSec = 20
	// 		rootPwd              = "user"
	// 	)

	// 	sendAsRoot := func(cs *e2e.ConsoleSession, cmd string) {
	// 		cs.MustSend(fmt.Sprintf("echo '%s' | sudo -S %s", rootPwd, cmd))
	// 	}

	// 	cs := harness.NewConsoleSession(deviceID)

	// 	// show current config & ensure the key is present
	// 	sendAsRoot(cs, "cat "+cfgFile)
	// 	cs.MustExpect(specFetchKey)

	// 	// patch config
	// 	sedExpr := fmt.Sprintf("sed -i -E 's/%s: .+m.+s/%s: 0m%ds/g' %s && cat %s", specFetchKey, specFetchKey,
	// 		specFetchIntervalSec, cfgFile, cfgFile)
	// 	sendAsRoot(cs, sedExpr)
	// 	cs.MustExpect(fmt.Sprintf("%s: 0m%ds", specFetchKey, specFetchIntervalSec))
	// 	sendAsRoot(cs, fmt.Sprintf("sh -c \"echo 'log-level: debug' >> %s\" && cat %s", cfgFile, cfgFile))
	// 	cs.MustExpect("log-level: debug")

	// 	sendAsRoot(cs, "systemctl restart flightctl-agent")
	// 	cs.Close()

	// 	By("waiting for publisher logs with the new interval")
	// 	// Wait for the target log messages to appear
	// 	opts := vm.JournalOpts{
	// 		Unit:     "flightctl-agent",
	// 		Since:    logLookbackDuration,
	// 		LastBoot: true,
	// 	}
	// 	util.EventuallySlow(harness.VM.JournalLogs).
	// 		WithArguments(opts).
	// 		Should(And(
	// 			ContainSubstring("No new template version from management service"),
	// 			ContainSubstring("publisher.go"),
	// 		))

	// 	// Now validate the timing intervals
	// 	logPattern := regexp.MustCompile(`.*time="([^"]+).*No new template version from management service.*publisher\.go.*"`)
	// 	expectedInterval := time.Duration(specFetchIntervalSec) * time.Second
	// 	Eventually(func() bool {
	// 		logs, err := harness.VM.JournalLogs(vm.JournalOpts{
	// 			Unit:     "flightctl-agent",
	// 			Since:    logLookbackDuration,
	// 			LastBoot: true,
	// 		})
	// 		Expect(err).ToNot(HaveOccurred())

	// 		return validateTimestampIntervals(logs, logPattern, expectedInterval)
	// 	}, 2*time.Minute, 10*time.Second).Should(BeTrue())
	// })

	It("recovers from image pull network disruption", Label("82541"), func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()

		const disruptionTime = 1 * time.Minute
		_, _, err := harness.WaitForBootstrapAndUpdateToVersion(deviceID, util.DeviceTags.V4)
		Expect(err).ToNot(HaveOccurred())

		Eventually(resources.GetJSONByName[*v1beta1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			Should(WithTransform((*v1beta1.Device).IsUpdating, BeTrue()))

		GinkgoWriter.Printf("Simulating network failure\n")
		DeferCleanup(func() { _ = harness.FixNetworkFailure() })
		err = harness.SimulateNetworkFailure()
		Expect(err).ToNot(HaveOccurred())

		GinkgoWriter.Printf("Waiting for image pull activity\n")
		util.EventuallySlow(harness.ReadPrimaryVMAgentLogs).
			WithArguments(logLookbackDuration, util.FLIGHTCTL_AGENT_SERVICE).
			Should(ContainSubstring("Pulling image"))

		GinkgoWriter.Printf("Simulating network disruption for %s\n", disruptionTime)
		Consistently(resources.GetJSONByName[*v1beta1.Device]).
			WithTimeout(disruptionTime).
			WithPolling(disruptionTime/10).
			WithArguments(harness, resources.Devices, deviceID).
			Should(WithTransform((*v1beta1.Device).IsUpdating, BeTrue()))

		err = harness.FixNetworkFailure()
		Expect(err).ToNot(HaveOccurred())

		GinkgoWriter.Printf("Network disruption fixed. Waiting for the device to finish updating\n")
		util.EventuallySlow(resources.GetJSONByName[*v1beta1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			Should(WithTransform((*v1beta1.Device).IsUpdatedToDeviceSpec, BeTrue()))
	})

	It("recovers from image pull network connection error", Label("83029"), func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()

		GinkgoWriter.Printf("Simulating network failure\n")
		DeferCleanup(func() { _ = harness.FixNetworkFailure() })
		err := harness.SimulateNetworkFailure()
		Expect(err).ToNot(HaveOccurred())

		_, _, err = harness.WaitForBootstrapAndUpdateToVersion(deviceID, util.DeviceTags.V4)
		Expect(err).ToNot(HaveOccurred())

		GinkgoWriter.Printf("Waiting for image pull activity\n")
		Eventually(resources.GetJSONByName[*v1beta1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			Should(WithTransform((*v1beta1.Device).IsUpdating, BeTrue()))

		util.EventuallySlow(harness.ReadPrimaryVMAgentLogs).
			WithArguments(logLookbackDuration, util.FLIGHTCTL_AGENT_SERVICE).
			Should(ContainSubstring("Pulling image"))

		GinkgoWriter.Printf("Waiting for image pull failure. It will take a while...\n")
		util.EventuallySlow(harness.ReadPrimaryVMAgentLogs).
			WithArguments(logLookbackDuration, util.FLIGHTCTL_AGENT_SERVICE).
			Should(And(
				ContainSubstring("Error"),
				Or(
					ContainSubstring("i/o timeout"),
					ContainSubstring("refused"),
				),
			),
			)

		GinkgoWriter.Printf("Image pull failure detected!\n")
		err = harness.FixNetworkFailure()
		Expect(err).ToNot(HaveOccurred())

		GinkgoWriter.Printf("Waiting for the device to finish updating\n")
		util.EventuallySlow(resources.GetJSONByName[*v1beta1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			Should(WithTransform((*v1beta1.Device).IsUpdatedToDeviceSpec, BeTrue()))
	})

	It("provides console --help and auxiliary features", Label("81866", "sanity"), func() {
		// Get harness directly - no shared package-level variable
		harness := e2e.GetWorkerHarness()

		out, err := harness.CLI("console", "--help")
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(
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
		out, err = harness.RunConsoleCommand(deviceID, nil, "flightctl-agent", "system-info")
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(ContainSubstring("hostname"))

		By("running a background command without a TTY")
		out, err = harness.RunConsoleCommand(deviceID, []string{"--notty"}, "pwd")
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(ContainSubstring("/"))

		By("generating a remote sos-report")
		// "sos: command not found" when running "console device/{device} -- sos" in a non-interactive shell. a bug?
		out, err = harness.RunConsoleCommand(deviceID, nil, "/usr/sbin/sos", "report", "--batch", "--quiet")
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(ContainSubstring("sos report has been generated"))

		By("failing when required command args are missing")
		out, err = harness.CLI("console", "--tty")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("Error:"))
	})
})

// -----------------------------------------------------------------------------
// Helper functions for deprecated test
// -----------------------------------------------------------------------------

// extractTimestampsFromLogs extracts and parses timestamps from log lines that match the given regex pattern.
// Returns a slice of valid timestamps found in the logs.
// func extractTimestampsFromLogs(logs string, logPattern *regexp.Regexp) []time.Time {
// 	lines := strings.Split(strings.TrimSpace(logs), "\n")
// 	GinkgoWriter.Printf("Read %d log lines from agent journal\n", len(lines))

// 	var validTimestamps []time.Time

// 	for _, line := range lines {
// 		if m := logPattern.FindStringSubmatch(line); m != nil {
// 			if t, err := time.Parse(time.RFC3339Nano, m[1]); err != nil {
// 				GinkgoWriter.Printf("Failed to parse timestamp %q: %v\n", m[1], err)
// 			} else {
// 				validTimestamps = append(validTimestamps, t)
// 				GinkgoWriter.Printf("Found matching log line with timestamp: %s\n", t.Format(time.RFC3339))
// 			}
// 		}
// 	}

// 	GinkgoWriter.Printf("Found %d lines matching the pattern\n", len(validTimestamps))
// 	return validTimestamps
// }

// validateIntervalTiming checks if intervals between consecutive timestamps are within tolerance.
// Returns true if all intervals are within 1 second of the expected interval.
// func validateIntervalTiming(timestamps []time.Time, expectedInterval time.Duration) bool {
// 	const toleranceThreshold = time.Second

// 	GinkgoWriter.Printf("Validating intervals between %d timestamps\n", len(timestamps))

// 	for i := 1; i < len(timestamps); i++ {
// 		delta := timestamps[i].Sub(timestamps[i-1])
// 		deviation := (delta - expectedInterval).Abs()

// 		GinkgoWriter.Printf("Timestamp %d->%d: delta=%v, expected=%v, deviation=%v\n",
// 			i-1, i, delta, expectedInterval, deviation)

// 		if deviation > toleranceThreshold {
// 			GinkgoWriter.Printf("Interval not as expected - deviation %v > %v threshold\n", deviation, toleranceThreshold)
// 			return false
// 		}
// 	}

// 	GinkgoWriter.Printf("All %d intervals are stable within %v tolerance\n", len(timestamps)-1, toleranceThreshold)
// 	return true
// }

// validateTimestampIntervals validates that the intervals between log timestamps match the expected interval.
// It returns true if at least 2 timestamps are found and all intervals are within 1 second of the expected interval.
// func validateTimestampIntervals(logs string, logPattern *regexp.Regexp, expectedInterval time.Duration) bool {
// 	timestamps := extractTimestampsFromLogs(logs, logPattern)

// 	const minRequired = 2
// 	if len(timestamps) < minRequired {
// 		GinkgoWriter.Printf("Need at least %d matching timestamps, only have %d - waiting for more logs\n", minRequired, len(timestamps))
// 		return false
// 	}

// 	return validateIntervalTiming(timestamps, expectedInterval)
// }
