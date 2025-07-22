package e2e

import (
	"fmt"
	"os/exec"
)

// reuseQcowImageWithOverlay creates an overlay qcow2 disk at output path that reuses the
// disk at backingPath.
func reuseQcowImageWithOverlay(backingPath string, output string) error {
	// Create a qcow2 overlay that uses the base image as backing file
	cmd := exec.Command( //nolint:gosec
		"qemu-img", "create",
		"-f", "qcow2",
		"-o", "backing_file="+backingPath,
		"-F", "qcow2",
		output)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create overlay disk from %s: %w", backingPath, err)
	}
	return nil
}
