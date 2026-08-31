package client

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	ociDeltaCmd    = "oci-delta"
	ostreeRepoPath = "/ostree/repo"
)

type OCIDelta struct {
	exec executer.Executer
	log  *log.PrefixLogger
}

func NewOCIDelta(log *log.PrefixLogger, exec executer.Executer) *OCIDelta {
	return &OCIDelta{
		log:  log,
		exec: exec,
	}
}

// Apply reconstructs an OCI image from a pulled delta artifact into dest.
// dest is an oci-delta output reference (oci:PATH or oci-archive:PATH).
func (d *OCIDelta) Apply(ctx context.Context, deltaRef, dest string) error {
	args := []string{
		"apply",
		"--ostree-repo",
		ostreeRepoPath,
		deltaRef,
		dest,
	}
	_, stderr, exitCode := d.exec.ExecuteWithContext(ctx, ociDeltaCmd, args...)
	if exitCode != 0 {
		return fmt.Errorf("oci-delta apply: %w", errors.FromStderr(stderr, exitCode))
	}
	return nil
}
