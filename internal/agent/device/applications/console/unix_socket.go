package console

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
)

// killNCSocketHoldersTimeout bounds best-effort cleanup of leftover `nc -U` processes
// left behind when the host-side `podman exec` is killed (Podman does not reliably
// signal the in-container process).
const killNCSocketHoldersTimeout = 5 * time.Second

// dialVMUnixSocket connects to a Unix socket inside the container via `nc -U`.
// It reaps any leftover `nc` clients for that socket before dialing so a previous
// crashed or cancelled session cannot permanently occupy QEMU's single-client
// serial/VNC chardev. Cleanup runs only before dial (not on Close) so a --force
// takeover cannot kill the successor session's newly started nc.
func dialVMUnixSocket(ctx context.Context, exec ExecStreamer, containerName, socketPath string, logger *log.PrefixLogger) (io.ReadWriteCloser, error) {
	if exec == nil {
		return nil, fmt.Errorf("dial VM Unix socket %q: no exec streamer", socketPath)
	}
	killNCSocketHolders(ctx, exec, containerName, socketPath, logger)

	conn, err := exec.ExecStream(ctx, containerName, "nc", "-U", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial VM Unix socket %q in container %q: %w", socketPath, containerName, err)
	}
	return conn, nil
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
