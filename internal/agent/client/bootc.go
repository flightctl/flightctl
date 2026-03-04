package client

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	BootcCmd = "bootc"
)

type bootc struct {
	executer executer.Executer
	log      *log.PrefixLogger
}

// NewBootc creates a new bootc client.
func NewBootc(log *log.PrefixLogger, executer executer.Executer) Bootc {
	return &bootc{
		executer: executer,
		log:      log,
	}
}

// Status returns the current bootc host status.
func (b *bootc) Status(ctx context.Context) (*container.BootcHost, error) {
	args := []string{"status", "--json"}
	stdout, stderr, exitCode := b.executer.ExecuteWithContext(ctx, BootcCmd, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("bootc status: %w", errors.FromStderr(stderr, exitCode))
	}

	if !isJSON(stdout) {
		b.log.Warnf("Non-JSON output from bootc status: %q", stdout)
		return nil, errors.ErrBootcStatusInvalidJSON
	}

	var bootcHost container.BootcHost
	if err := json.Unmarshal([]byte(stdout), &bootcHost); err != nil {
		return nil, fmt.Errorf("unmarshalling bootc status: %w", err)
	}

	return &bootcHost, nil
}

// Switch pulls the specified image and stages it for the next boot while retaining a copy of the most recently booted image.
// The status will be updated in logger. Switch assumes the os image is available in local container storage.
func (b *bootc) Switch(ctx context.Context, image string) error {
	target, err := container.ImageToBootcTarget(image)
	if err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		args := []string{
			"switch",
			"--transport",
			"containers-storage",
			"--retain",
			target,
		}
		_, stderr, exitCode := b.executer.ExecuteWithContext(ctx, BootcCmd, args...)
		if exitCode != 0 {
			done <- fmt.Errorf("%w: %w", errors.ErrStageImage, errors.FromStderr(stderr, exitCode))
			return
		}
		done <- nil
	}()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	start := time.Now()
	// log progress
	for {
		select {
		case err := <-done:
			b.log.Infof("Switching image complete took: %v", time.Since(start))
			return err
		case <-ticker.C:
			b.log.Info("Switching image, please wait...")
		case <-ctx.Done():
			b.log.Infof("Switching image failed after: %v", time.Since(start))
			return ctx.Err()
		}
	}
}

// Apply restart or reboot into the new target image.
func (b *bootc) Apply(ctx context.Context) error {
	args := []string{"upgrade", "--apply"}
	_, stderr, exitCode := b.executer.ExecuteWithContext(ctx, BootcCmd, args...)
	if exitCode != 0 && exitCode != 137 { // 137 is the exit code for SIGKILL and is expected during reboot 128 + SIGKILL (9)
		return fmt.Errorf("%w: %w", errors.ErrApplyImage, errors.FromStderr(stderr, exitCode))
	}
	return nil
}

func (b *bootc) Rollback(ctx context.Context) error {
	args := []string{"rollback"}
	_, stderr, exitCode := b.executer.ExecuteWithContext(ctx, BootcCmd, args...)
	if exitCode != 0 && exitCode != 137 { // 137 is expected during reboot
		return fmt.Errorf("bootc rollback: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

// UsrOverlay adds a transient writable overlayfs on `/usr` that will be discarded on reboot.
func (b *bootc) UsrOverlay(ctx context.Context) error {
	args := []string{"usr-overlay"}
	_, stderr, exitCode := b.executer.ExecuteWithContext(ctx, BootcCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("overlay image: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}

// isJSON checks whether a string is valid JSON
func isJSON(s string) bool {
	var js json.RawMessage
	return json.Unmarshal([]byte(s), &js) == nil
}
