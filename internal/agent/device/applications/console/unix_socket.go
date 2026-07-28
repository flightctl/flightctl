package console

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
)

const (
	// killNCSocketHoldersTimeout bounds best-effort cleanup of leftover `nc -U` processes
	// left behind when the host-side `podman exec` is killed (Podman does not reliably
	// signal the in-container process).
	killNCSocketHoldersTimeout = 5 * time.Second

	// maxDialAttempts covers the race where pkill has sent SIGTERM but the old nc has
	// not yet released QEMU's single-client socket when the first dial runs.
	maxDialAttempts = 3
)

// dialRetryDelay is the pause between failed dial attempts. Mutable for tests.
var dialRetryDelay = 200 * time.Millisecond

// dialVMUnixSocket connects to a Unix socket inside the container via `nc -U`.
// It reaps any leftover `nc` clients for that socket before each dial attempt so a
// previous crashed or cancelled session cannot permanently occupy QEMU's single-client
// serial/VNC chardev. Cleanup runs only before dial (not on Close) so a --force
// takeover cannot kill the successor session's newly started nc. Dial is retried a
// limited number of times if ExecStream fails while the chardev is still freeing.
func dialVMUnixSocket(ctx context.Context, exec ExecStreamer, containerName, socketPath string, logger *log.PrefixLogger) (io.ReadWriteCloser, error) {
	if exec == nil {
		return nil, fmt.Errorf("dial VM Unix socket %q: no exec streamer", socketPath)
	}

	var lastErr error
	for attempt := 1; attempt <= maxDialAttempts; attempt++ {
		killNCSocketHolders(ctx, exec, containerName, socketPath, logger)

		conn, err := exec.ExecStream(ctx, containerName, "nc", "-U", socketPath)
		if err == nil {
			if attempt > 1 {
				logger.Debugf("dialed %s in %s on attempt %d/%d", socketPath, containerName, attempt, maxDialAttempts)
			}
			return conn, nil
		}
		lastErr = err
		logger.Debugf("dial attempt %d/%d failed for %s in %s: %v", attempt, maxDialAttempts, socketPath, containerName, err)

		if attempt == maxDialAttempts {
			break
		}
		if err := sleepWithContext(ctx, dialRetryDelay); err != nil {
			return nil, fmt.Errorf("dial VM Unix socket %q in container %q: %w", socketPath, containerName, err)
		}
	}
	return nil, fmt.Errorf("dial VM Unix socket %q in container %q after %d attempts: %w", socketPath, containerName, maxDialAttempts, lastErr)
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func killNCSocketHolders(ctx context.Context, exec ExecStreamer, containerName, socketPath string, logger *log.PrefixLogger) {
	if exec == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, killNCSocketHoldersTimeout)
	defer cancel()

	// Anchor the pattern so virt-serial0 does not match virt-serial0-log.
	pattern := "^nc -U " + regexp.QuoteMeta(socketPath) + "$"
	script := fmt.Sprintf("pkill -f %q || true", pattern)
	if err := exec.Exec(ctx, containerName, "sh", "-c", script); err != nil {
		logger.Debugf("failed to kill leftover nc for %s in %s: %v", socketPath, containerName, err)
	}
}
