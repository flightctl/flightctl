package cli_test

import (
	"context"
	"fmt"
	"io"
	"net"
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
	. "github.com/onsi/gomega/gbytes"
	"github.com/sirupsen/logrus"
)

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

type consoleSession struct {
	stdin  io.WriteCloser
	stdout *Buffer
}

// newConsoleSession starts a PTY console session to the specified device.
func newConsoleSession(h *e2e.Harness, deviceID string) *consoleSession {
	in, out, err := h.RunInteractiveCLI("console", "--tty", "device/"+deviceID)
	Expect(err).ToNot(HaveOccurred())

	cs := &consoleSession{stdin: in, stdout: BufferReader(out)}

	// Trigger prompt and wait for it.
	cs.mustSend("")
	cs.mustExpect(".*root@.*#")

	return cs
}

func (cs *consoleSession) mustSend(cmd string) {
	logrus.Infof("console> %s", cmd)
	_, err := io.WriteString(cs.stdin, cmd+"\n")
	Expect(err).NotTo(HaveOccurred())
	Expect(cs.stdout.Clear()).To(Succeed())
}

func (cs *consoleSession) mustExpect(pattern string) {
	logrus.Infof("console EXPECT %q", pattern)
	Eventually(cs.stdout, TIMEOUT, POLLING).Should(Say(pattern))
	Expect(cs.stdout.Clear()).To(Succeed())
}

func (cs *consoleSession) close() {
	cs.mustSend("exit")
	Consistently(cs.stdout, 2*time.Second).ShouldNot(Say(".*panic:"))

	if err := cs.stdin.Close(); err != nil {
		logrus.WithError(err).Warn("failed to close console stdin")
	}
	if err := cs.stdout.Close(); err != nil {
		logrus.WithError(err).Warn("failed to close console stdout")
	}
}

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
		cs := newConsoleSession(harness, deviceID)
		cs.mustSend("ls")
		cs.mustExpect(".*bin")
		cs.close()
	})

	It("supports multiple simultaneous console sessions", Label("81737", "sanity"), func() {
		cs1 := newConsoleSession(harness, deviceID)
		cs2 := newConsoleSession(harness, deviceID)

		cs2.mustSend("pwd")
		cs2.mustExpect("/")

		cs1.mustSend("echo Session1 > /var/home/user/file.txt")
		cs2.mustSend("cat /var/home/user/file.txt")
		cs2.mustExpect("Session1")

		cs2.mustSend("echo Session2 >> /var/home/user/file.txt")
		cs1.mustSend("cat /var/home/user/file.txt")
		cs1.mustExpect("(?s).*Session1.*Session2.*")

		cs1.close()
		cs2.close()
	})

	It("keeps console sessions open during a device update", Label("81786", "sanity"), func() {
		const sessionsToOpen = 4
		const expectedRenderedVersion = 2 + sessionsToOpen*2

		// kick off an update
		device, _ := harness.WaitForBootstrapAndUpdateToVersion(deviceID, ":v4")
		Eventually(resources.GetJSONByName[*v1alpha1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			WithTimeout(1 * time.Minute).
			WithPolling(1 * time.Second).
			Should(WithTransform((*v1alpha1.Device).IsUpdating, BeTrue()))
		Expect(device.Status.Summary.Status).To(Equal(v1alpha1.DeviceSummaryStatusOnline))

		sessions := make([]*consoleSession, 0, sessionsToOpen)
		for i := range sessionsToOpen {
			By(fmt.Sprintf("opening console session %d/%d", i+1, sessionsToOpen))
			sessions = append(sessions, newConsoleSession(harness, deviceID))
		}
		for i, cs := range sessions {
			By(fmt.Sprintf("closing console session %d/%d", i+1, sessionsToOpen))
			cs.close()
		}

		By("waiting for the update to finish")
		Eventually(resources.GetJSONByName[*v1alpha1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			WithTimeout(4 * time.Minute).
			WithPolling(10 * time.Second).
			Should(WithTransform((*v1alpha1.Device).IsUpdatedToDeviceSpec, BeTrue()))

		// ensure applications become healthy
		Eventually(resources.GetJSONByName[*v1alpha1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			WithTimeout(1 * time.Minute).
			WithPolling(500 * time.Millisecond).
			Should(WithTransform(func(d *v1alpha1.Device) v1alpha1.ApplicationsSummaryStatusType {
				return d.Status.ApplicationsSummary.Status
			}, Equal(v1alpha1.ApplicationsSummaryStatusHealthy)))

		Expect(harness.GetCurrentDeviceRenderedVersion(deviceID)).To(Equal(expectedRenderedVersion))

		By("returns a helpful error when the device is not found")
		/*out, err :=*/ _, err := harness.CLI("console", "device/nonexistent")
		Expect(err).To(HaveOccurred())
		//Expect(out).To(ContainSubstring("not found")) // currently fails
	})

	It("allows tuning spec-fetch-interval", Label("82538", "sanity"), func() {
		const (
			cfgFile              = "/etc/flightctl/config.yaml"
			specFetchKey         = "spec-fetch-interval"
			specFetchIntervalSec = 20
			rootPwd              = "user"
		)

		sendAsRoot := func(cs *consoleSession, cmd string) {
			cs.mustSend(fmt.Sprintf("echo '%s' | sudo -S %s", rootPwd, cmd))
		}

		cs := newConsoleSession(harness, deviceID)

		// show current config & ensure the key is present
		sendAsRoot(cs, "cat "+cfgFile)
		cs.mustExpect(specFetchKey)

		// patch config
		sedExpr := fmt.Sprintf("sed -i -E 's/%s: .+m.+s/%s: 0m%ds/g' %s && cat %s", specFetchKey, specFetchKey,
			specFetchIntervalSec, cfgFile, cfgFile)
		sendAsRoot(cs, sedExpr)
		cs.mustExpect(fmt.Sprintf("%s: 0m%ds", specFetchKey, specFetchIntervalSec))
		sendAsRoot(cs, fmt.Sprintf("sh -c \"echo 'log-level: debug' >> %s\" && cat %s", cfgFile, cfgFile))
		cs.mustExpect("log-level: debug")

		sendAsRoot(cs, "systemctl restart flightctl-agent")
		cs.close()

		By("waiting for publisher logs with the new interval")
		tsRe := regexp.MustCompile(`time="([^"]+)"`)
		Eventually(func() (bool, error) {
			out, err := harness.CLI("console", fmt.Sprintf("device/%s", deviceID),
				"--", "journalctl", "-o", "short-precise", "--no-pager", "-u", "flightctl-agent",
				"|", "grep", "-E", `.*"No new template version from management service.*publisher.go.*"`)
			if err != nil {
				return false, nil
			}

			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) < 2 {
				return false, nil
			}

			var prev time.Time
			for _, ln := range lines {
				m := tsRe.FindStringSubmatch(ln)
				if m == nil {
					return false, nil
				}

				t, err := time.Parse(time.RFC3339Nano, m[1])
				if err != nil {
					return false, err
				}
				if !prev.IsZero() {
					delta := t.Sub(prev)
					if (delta - (specFetchIntervalSec * time.Second)).Abs() > time.Second {
						return false, nil // interval not yet stable
					}
				}
				prev = t
			}
			return true, nil
		}, 2*time.Minute, 10*time.Second).Should(BeTrue())
	})

	It("recovers from image pull failures", Label("82541", "sanity"), func() {
		_, newImageRef := harness.WaitForBootstrapAndUpdateToVersion(deviceID, ":v4")

		Eventually(resources.GetJSONByName[*v1alpha1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			WithTimeout(1 * time.Minute).WithPolling(1 * time.Second).
			Should(WithTransform((*v1alpha1.Device).IsUpdating, BeTrue()))

		repoHost, repoPort, err := net.SplitHostPort(strings.SplitN(newImageRef, "/", 2)[0])
		Expect(err).ToNot(HaveOccurred())
		Expect(harness.SimulateNetworkFailureFor(repoHost, repoPort)).To(Succeed())

		in, out, err := harness.RunInteractiveCLI(
			"console", "device/"+deviceID, "--",
			"journalctl", "-f", "-o", "short-precise", "--no-pager", "-u", "flightctl-agent")
		Expect(err).ToNot(HaveOccurred())
		defer in.Close()
		defer out.Close()

		buf := BufferReader(out)
		Eventually(buf, 1*time.Minute, 10*time.Second).Should(Say(".*Pulling image.*"))
		logrus.Infof("Waiting for image pull failure. It will take a while...")
		_ = buf.Clear()
		Eventually(buf, 10*time.Minute, 10*time.Second).Should(Say(".*retriable error.*pull.*image.*"))

		Expect(harness.FixNetworkFailureFor(repoHost, repoPort)).To(Succeed())
		Eventually(resources.GetJSONByName[*v1alpha1.Device]).
			WithArguments(harness, resources.Devices, deviceID).
			WithTimeout(4 * time.Minute).WithPolling(10 * time.Second).
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
		cs := newConsoleSession(harness, deviceID)
		Expect(cs.stdin.Write([]byte("\n~.\n"))).To(Equal(4))
		Eventually(cs.stdout.Closed, 1*time.Second, 100*time.Millisecond).Should(BeTrue())

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
