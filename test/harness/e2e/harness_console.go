package e2e

import (
	"fmt"
	"io"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gbytes"
)

// ConsoleSession represents a PTY console session to a device
type ConsoleSession struct {
	Stdin            io.WriteCloser
	Stdout           *Buffer
	closeOnce        sync.Once
	skipGracefulExit bool // when true, Close only releases fds (e.g. after flightctl "~." disconnect)
}

// NewConsoleSession starts a PTY console session to the specified device.
func (h *Harness) NewConsoleSession(deviceID string) *ConsoleSession {
	in, out, err := h.RunInteractiveCLI("console", "--tty", "device/"+deviceID)
	Expect(err).ToNot(HaveOccurred())

	cs := &ConsoleSession{Stdin: in, Stdout: BufferReader(out)}

	// Trigger prompt and wait for it.
	cs.MustSend("")
	cs.MustExpect(`.*flightctl-console@.*\$`)

	return cs
}

// MustSend sends a command to the console session
func (cs *ConsoleSession) MustSend(cmd string) {
	Expect(cs.Stdout.Clear()).To(Succeed())
	GinkgoWriter.Printf("console> %s\n", cmd)
	_, err := io.WriteString(cs.Stdin, cmd+"\n")
	Expect(err).NotTo(HaveOccurred())
}

// MustExpect waits for a pattern to appear in the console output
func (cs *ConsoleSession) MustExpect(pattern string) {
	GinkgoWriter.Printf("console EXPECT %q\n", pattern)
	Eventually(cs.Stdout).Should(Say(pattern))
	Expect(cs.Stdout.Clear()).To(Succeed())
}

// When called set Close function not to send the "exit" command before disconnecting the client
// (e.g. flightctl "~." escape) and only release local PTY fds.
func (cs *ConsoleSession) SkipGracefulExitOnClose() {
	cs.skipGracefulExit = true
}

// Close terminates the console session. When SkipGracefulExitOnClose was set (e.g. after "~."),
// it only closes stdin/stdout without sending "exit".
func (cs *ConsoleSession) Close() {
	cs.closeOnce.Do(func() {
		if !cs.skipGracefulExit {
			cs.sendExit()
		}

		if cs.Stdin != nil {
			if err := cs.Stdin.Close(); err != nil {
				GinkgoWriter.Printf("failed to close console stdin: %v\n", err)
			}
		}
		if cs.Stdout != nil {
			if err := cs.Stdout.Close(); err != nil {
				GinkgoWriter.Printf("failed to close console stdout: %v\n", err)
			}
		}
	})
}

// RunConsoleCommand executes the flightctl console command for the given
// device.
//
//	flags – optional CLI flags that go before "--" (e.g. "--notty").
//	cmd   – remote command (and its args) to execute after "--". Must contain
//	        at least one string; for interactive sessions use NewConsoleSession.
func (h *Harness) RunConsoleCommand(deviceID string, flags []string, cmd ...string) (string, error) {
	// Build the argument list. The first two elements must be the sub-command
	// and the target device. After that we append any additional flags
	// provided by the caller. If a command needs to be executed we append the
	// "--" separator and finally the command with its arguments.
	args := []string{"console", fmt.Sprintf("device/%s", deviceID)}
	args = append(args, flags...)
	if len(cmd) > 0 {
		args = append(args, "--")
		args = append(args, cmd...)
	}

	return h.CLI(args...)
}

// sendExit attempts to gracefully close the remote console without failing cleanup.
func (cs *ConsoleSession) sendExit() {
	if cs.Stdout == nil {
		GinkgoWriter.Printf("console stdout is nil; sending graceful exit without clearing stdout\n")
	} else if cs.Stdout.Closed() {
		GinkgoWriter.Printf("console stdout is already closed; sending graceful exit without clearing stdout\n")
	} else if err := cs.Stdout.Clear(); err != nil {
		GinkgoWriter.Printf("failed to clear console stdout before graceful exit: %v\n", err)
	}
	if cs.Stdin == nil {
		GinkgoWriter.Printf("console stdin is nil; skipping graceful exit\n")
		return
	}

	GinkgoWriter.Printf("console> exit\n")
	if _, err := io.WriteString(cs.Stdin, "exit\n"); err != nil {
		GinkgoWriter.Printf("failed to send graceful console exit: %v\n", err)
	}
}
