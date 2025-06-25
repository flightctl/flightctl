package e2e

import (
	"io"
	"time"

	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gbytes"
	"github.com/sirupsen/logrus"
)

// ConsoleSession represents a PTY console session to a device
type ConsoleSession struct {
	Stdin  io.WriteCloser
	Stdout *Buffer
}

// NewConsoleSession starts a PTY console session to the specified device.
func (h *Harness) NewConsoleSession(deviceID string) *ConsoleSession {
	in, out, err := h.RunInteractiveCLI("console", "--tty", "device/"+deviceID)
	Expect(err).ToNot(HaveOccurred())

	cs := &ConsoleSession{Stdin: in, Stdout: BufferReader(out)}

	// Trigger prompt and wait for it.
	cs.MustSend("")
	cs.MustExpect(".*root@.*#")

	return cs
}

// MustSend sends a command to the console session
func (cs *ConsoleSession) MustSend(cmd string) {
	logrus.Infof("console> %s", cmd)
	_, err := io.WriteString(cs.Stdin, cmd+"\n")
	Expect(err).NotTo(HaveOccurred())
	Expect(cs.Stdout.Clear()).To(Succeed())
}

// MustExpect waits for a pattern to appear in the console output
func (cs *ConsoleSession) MustExpect(pattern string) {
	logrus.Infof("console EXPECT %q", pattern)
	Eventually(cs.Stdout).Should(Say(pattern))
	Expect(cs.Stdout.Clear()).To(Succeed())
}

// Close terminates the console session
func (cs *ConsoleSession) Close() {
	cs.MustSend("exit")
	Consistently(cs.Stdout, 2*time.Second).ShouldNot(Say(".*panic:"))

	if err := cs.Stdin.Close(); err != nil {
		logrus.WithError(err).Warn("failed to close console stdin")
	}
	if err := cs.Stdout.Close(); err != nil {
		logrus.WithError(err).Warn("failed to close console stdout")
	}
}
