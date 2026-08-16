package e2e

import (
	"fmt"
	"io"
	"regexp"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gbytes"
)

// ConsoleSession represents a PTY console session to a device
type ConsoleSession struct {
	Stdin     io.WriteCloser
	Stdout    *Buffer
	closeOnce sync.Once
}

// NewAppConsoleSession starts a PTY console session to a VM application's serial or VNC console.
func (h *Harness) NewAppConsoleSession(deviceID, appName, consoleType string) *ConsoleSession {
	in, out, err := h.RunInteractiveCLI(
		"app", "console", fmt.Sprintf("device/%s", deviceID),
		"--name", appName,
		"--type", consoleType,
		"--tty",
	)
	Expect(err).ToNot(HaveOccurred())

	return &ConsoleSession{Stdin: in, Stdout: BufferReader(out)}
}

// NewAppConsoleSessionWaitingForLogin opens a serial console and retries until the guest
// login prompt appears. The VM app may report Running before getty is ready; connecting
// too early can drop the session before boot completes.
func (h *Harness) NewAppConsoleSessionWaitingForLogin(deviceID, appName string, timeout, polling time.Duration) *ConsoleSession {
	if polling <= 0 {
		polling = time.Second
	}

	deadline := time.Now().Add(timeout)
	loginRE := regexp.MustCompile(`(?i)login:`)
	var cs *ConsoleSession

	for time.Now().Before(deadline) {
		if cs == nil || cs.Stdout.Closed() {
			if cs != nil {
				cs.Close()
				cs = nil
			}
			in, out, err := h.RunInteractiveCLI(
				"app", "console", fmt.Sprintf("device/%s", deviceID),
				"--name", appName,
				"--type", "serial",
				"--tty",
			)
			if err != nil {
				GinkgoWriter.Printf("app serial console start failed: %v\n", err)
				time.Sleep(polling)
				continue
			}
			cs = &ConsoleSession{Stdin: in, Stdout: BufferReader(out)}
		}

		contents := cs.Stdout.Contents()
		if loginRE.Match(contents) {
			Expect(cs.Stdout.Clear()).To(Succeed())
			return cs
		}

		time.Sleep(polling)
	}

	if cs != nil {
		cs.Close()
	}
	Expect(fmt.Errorf("serial console login prompt not seen within %s", timeout)).NotTo(HaveOccurred())
	return nil
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

// MustExpect waits for a pattern to appear in the console output.
// Uses the suite's default Eventually timeout (e.g. cli_suite_test sets 1m/1s).
func (cs *ConsoleSession) MustExpect(pattern string) {
	GinkgoWriter.Printf("console EXPECT %q\n", pattern)
	Eventually(cs.Stdout).Should(Say(pattern))
	Expect(cs.Stdout.Clear()).To(Succeed())
}

// MustExpectWithin waits up to timeout for a pattern to appear in the console output.
// Use a long timeout when the remote end may still be booting (e.g. VM serial console).
func (cs *ConsoleSession) MustExpectWithin(pattern string, timeout, polling time.Duration) {
	GinkgoWriter.Printf("console EXPECT %q (timeout %s)\n", pattern, timeout)
	Eventually(cs.Stdout).WithTimeout(timeout).WithPolling(polling).Should(Say(pattern))
	Expect(cs.Stdout.Clear()).To(Succeed())
}

func mustParseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	Expect(err).NotTo(HaveOccurred(), "parsing duration %q", s)
	return d
}

// Disconnect sends the flightctl ~. escape sequence and waits for the CLI to exit.
func (cs *ConsoleSession) Disconnect() {
	Expect(cs.Stdin.Write([]byte("\n~.\n"))).To(BeNumerically(">", 0))
	Eventually(cs.Stdout.Closed).WithTimeout(mustParseDuration(TIMEOUT)).WithPolling(mustParseDuration(POLLING)).Should(BeTrue())
}

// Close releases the console session. Sends exit when the session is still open;
// after Disconnect() the buffer is already closed so only local fds are released.
func (cs *ConsoleSession) Close() {
	cs.closeOnce.Do(func() {
		if cs.Stdout != nil && !cs.Stdout.Closed() {
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

// sendExit attempts to gracefully close the remote console without failing cleanup.
func (cs *ConsoleSession) sendExit() {
	if cs.Stdout == nil {
		GinkgoWriter.Printf("console stdout is nil; sending graceful exit without clearing stdout\n")
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
