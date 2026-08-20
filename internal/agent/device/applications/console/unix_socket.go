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
	killSocketHoldersTimeout = 5 * time.Second
	maxDialAttempts          = 3
	ncProbeScript            = "command -v nc >/dev/null"
)

// dialRetryDelay is the pause between failed dial attempts. Mutable for tests.
var dialRetryDelay = 200 * time.Millisecond

// dialVMUnixSocket connects to a Unix socket inside the container via `nc -U`,
// or `socat` when nc is absent. Leftover clients are reaped before each dial,
// not on Close. Dial is retried if ExecStream fails while the chardev is freeing.
func dialVMUnixSocket(ctx context.Context, exec ExecStreamer, containerName, socketPath string, logger *log.PrefixLogger) (io.ReadWriteCloser, error) {
	if exec == nil {
		return nil, fmt.Errorf("dial VM Unix socket %q: no exec streamer", socketPath)
	}

	args := unixSocketDialArgs(socketPath, containerHasNC(ctx, exec, containerName))

	var lastErr error
	for attempt := 1; attempt <= maxDialAttempts; attempt++ {
		killSocketHolders(ctx, exec, containerName, socketPath, logger)

		conn, err := exec.ExecStream(ctx, containerName, args...)
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

func containerHasNC(ctx context.Context, exec ExecStreamer, containerName string) bool {
	return exec.Exec(ctx, containerName, "sh", "-c", ncProbeScript) == nil
}

func unixSocketDialArgs(socketPath string, useNC bool) []string {
	if useNC {
		return []string{"nc", "-U", socketPath}
	}
	return []string{"socat", "-", "UNIX-CONNECT:" + socketPath}
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

func killSocketHolders(ctx context.Context, exec ExecStreamer, containerName, socketPath string, logger *log.PrefixLogger) {
	if exec == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, killSocketHoldersTimeout)
	defer cancel()

	quoted := regexp.QuoteMeta(socketPath)
	ncPat := "^nc -U " + quoted + "$"
	socatPat := "^socat - UNIX-CONNECT:" + quoted + "$"
	script := fmt.Sprintf("pkill -f %q || true; pkill -f %q || true", ncPat, socatPat)
	if err := exec.Exec(ctx, containerName, "sh", "-c", script); err != nil {
		logger.Debugf("failed to kill leftover socket clients for %s in %s: %v", socketPath, containerName, err)
	}
}
